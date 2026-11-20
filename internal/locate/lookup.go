package locate

import (
	"fmt"
	"net/netip"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/oschwald/maxminddb-golang/v2"

	"lanflow/internal/geo"
	"lanflow/internal/model"
)

// dbCheckEvery is how often a database file is looked at again for a
// newer version (or for having appeared at all).
const dbCheckEvery = 10 * time.Second

// maxDatabaseSize bounds what is read as a database.
const maxDatabaseSize = 512 << 20

// database is one MMDB file, read again when it changes on disk: the core
// replaces geoip.metadb and GeoLite2-ASN.mmdb when it updates them. The
// file is read into memory of its own (readOffHeap) rather than mapped,
// so replacing or truncating it can neither fault a lookup in progress
// nor (on Windows) be refused because the file is in use.
type database struct {
	path string

	// use holds rw for reading during a lookup; a newer reader takes its
	// place, and the old one's memory is given back, under the write lock.
	rw   sync.RWMutex
	r    *maxminddb.Reader // nil until the file could be read
	free func()

	mu      sync.Mutex
	checked time.Time // when the file was last looked at
	mtime   time.Time // of the file the reader was made from
	size    int64
}

// use runs fn with the current reader; fn is not called when the file is
// missing or unreadable so far. What fn keeps must not point into the
// reader's memory (strings.Clone): it is released when the file changes.
func (d *database) use(now time.Time, fn func(r *maxminddb.Reader)) {
	if d.path == "" {
		return
	}
	d.refresh(now)
	d.rw.RLock()
	defer d.rw.RUnlock()
	if d.r != nil {
		fn(d.r)
	}
}

// refresh reads the file again when it changed since the last look. A
// failure is never remembered as "opened": the reader in use stays, and
// the file is tried again at the next check.
func (d *database) refresh(now time.Time) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.checked.IsZero() && now.Sub(d.checked) >= 0 && now.Sub(d.checked) < dbCheckEvery {
		return
	}
	d.checked = now
	st, err := os.Stat(d.path)
	if err != nil {
		return // gone for the moment (being replaced): keep what we have
	}
	d.rw.RLock()
	have := d.r != nil
	d.rw.RUnlock()
	if have && st.ModTime().Equal(d.mtime) && st.Size() == d.size {
		return
	}
	data, free, err := readOffHeap(d.path)
	if err != nil {
		return
	}
	r, err := maxminddb.OpenBytes(data)
	if err != nil {
		free()
		return // half written or not a database: try again later
	}
	d.rw.Lock()
	old := d.free
	d.r, d.free = r, free
	d.rw.Unlock()
	if old != nil {
		old()
	}
	d.mtime, d.size = st.ModTime(), st.Size()
}

// lookup resolves an IP offline. Missing databases or unknown IPs leave
// the corresponding fields empty; the IP itself is always returned.
func (l *locator) lookup(ip string) model.EndpointInfo {
	ep := model.EndpointInfo{IP: ip}
	addr, err := netip.ParseAddr(strings.TrimSpace(ip))
	if err != nil {
		return ep
	}
	addr = addr.Unmap()
	now := l.now()

	// Strings decoded from a reader are cloned: they may point into its
	// memory, which is released when the file changes (database.use).
	code := ""
	l.geoDB.use(now, func(db *maxminddb.Reader) { code = strings.Clone(lookupCountry(db, addr)) })
	if code != "" {
		if p, ok := geo.Country(code); ok {
			// Only the country is known: show the country's name at
			// its main city's coordinates.
			p.City = geo.CountryName(code)
			ep.Place = &p
		} else {
			ep.Place = &model.Place{Country: code, City: code}
		}
	}
	l.asnDB.use(now, func(db *maxminddb.Reader) {
		num, org := lookupASN(db, addr)
		if num != "" {
			ep.ASN = "AS" + strings.Clone(num)
		}
		ep.ISP = strings.Clone(shortenOrg(org))
	})
	return ep
}

// lookupCountry returns the upper-case ISO code for addr, understanding
// both MaxMind-style records ({country:{iso_code}}) and MetaCubeX's
// Meta-geoip0 (a bare lower-case code or a list of codes and tags).
func lookupCountry(r *maxminddb.Reader, addr netip.Addr) string {
	res := r.Lookup(addr)
	if !res.Found() {
		return ""
	}
	if r.Metadata.DatabaseType != "Meta-geoip0" {
		var rec struct {
			Country struct {
				ISOCode string `maxminddb:"iso_code"`
			} `maxminddb:"country"`
			RegisteredCountry struct {
				ISOCode string `maxminddb:"iso_code"`
			} `maxminddb:"registered_country"`
		}
		if err := res.Decode(&rec); err == nil {
			if rec.Country.ISOCode != "" {
				return strings.ToUpper(rec.Country.ISOCode)
			}
			if rec.RegisteredCountry.ISOCode != "" {
				return strings.ToUpper(rec.RegisteredCountry.ISOCode)
			}
		}
	}
	var any_ any
	if err := res.Decode(&any_); err != nil {
		return ""
	}
	return metaCountry(any_)
}

// metaCountry extracts a country code from a decoded Meta-geoip0 record
// (string or []any). Tags such as "cloudflare" are skipped.
func metaCountry(v any) string {
	switch rec := v.(type) {
	case string:
		if isISO2(rec) {
			return strings.ToUpper(rec)
		}
	case []any:
		for _, item := range rec {
			if s, ok := item.(string); ok && isISO2(s) {
				return strings.ToUpper(s)
			}
		}
	case []string:
		for _, s := range rec {
			if isISO2(s) {
				return strings.ToUpper(s)
			}
		}
	}
	return ""
}

func isISO2(s string) bool {
	if len(s) != 2 {
		return false
	}
	for i := 0; i < 2; i++ {
		c := s[i] | 0x20
		if c < 'a' || c > 'z' {
			return false
		}
	}
	return true
}

// lookupASN reads GeoLite2-ASN style records (also DB-IP compat) and the
// ipinfo free ASN database.
func lookupASN(r *maxminddb.Reader, addr netip.Addr) (num, org string) {
	res := r.Lookup(addr)
	if !res.Found() {
		return "", ""
	}
	if strings.HasPrefix(r.Metadata.DatabaseType, "ipinfo") {
		var rec struct {
			ASN  string `maxminddb:"asn"`
			Name string `maxminddb:"name"`
		}
		if res.Decode(&rec) == nil && rec.ASN != "" {
			return strings.TrimPrefix(rec.ASN, "AS"), rec.Name
		}
		return "", ""
	}
	var rec struct {
		Number uint   `maxminddb:"autonomous_system_number"`
		Org    string `maxminddb:"autonomous_system_organization"`
	}
	if res.Decode(&rec) != nil || rec.Number == 0 {
		return "", ""
	}
	return fmt.Sprint(rec.Number), rec.Org
}
