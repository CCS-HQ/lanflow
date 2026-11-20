package locate

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"sync"
	"time"

	"lanflow/internal/fsx"
	"lanflow/internal/geo"
	"lanflow/internal/model"
	"lanflow/internal/netx"
)

// cacheTTL is how long a probe result stays valid.
const cacheTTL = 24 * time.Hour

// probeTimeout bounds one trace request through a node.
const probeTimeout = 8 * time.Second

// cacheEntry is a probe result as stored in the JSON cache: the ExitInfo
// plus the probe's wall-clock duration, used for the latency sanity check.
type cacheEntry struct {
	model.ExitInfo
	ProbeRTT time.Duration `json:",omitempty"`
}

// cacheFlushEvery is the shortest time between two writes of the cache
// file: probing every node of a subscription stores hundreds of results in
// a row, and each write is the whole file.
const cacheFlushEvery = 2 * time.Second

type locator struct {
	cachePath string
	traceURL  string

	now func() time.Time

	// Injection points for tests.
	proxyClient  func(port int, timeout time.Duration) *http.Client
	directClient func(ctx context.Context, timeout time.Duration) (*http.Client, string, error)
	resolve      func(ip string) model.EndpointInfo

	mu     sync.Mutex // guards cache
	loaded bool
	cache  map[string]cacheEntry
	// The file is written outside mu (readers are not held up by the disk)
	// and at most once per flushEvery: a result stored sooner is written by
	// a timer when the interval is over.
	flushEvery time.Duration
	lastFlush  time.Time
	flushTimer *time.Timer

	writeMu sync.Mutex // one writer of the file at a time, in order

	probeMu sync.Mutex // one probe at a time

	geoDB, asnDB database
}

func newLocator(cachePath, mmdbPath, asnPath string) *locator {
	l := &locator{
		cachePath:    cachePath,
		geoDB:        database{path: mmdbPath},
		asnDB:        database{path: asnPath},
		flushEvery:   cacheFlushEvery,
		traceURL:     TraceURL,
		now:          time.Now,
		proxyClient:  netx.ViaPort,
		directClient: physicalClient,
	}
	l.resolve = l.lookup
	return l
}

// physicalClient is netx.PhysicalDirect, reporting the interface name.
func physicalClient(ctx context.Context, timeout time.Duration) (*http.Client, string, error) {
	c, ifc, err := netx.PhysicalDirect(ctx, timeout)
	return c, ifc.Name, err
}

// guess answers offline: a valid cached probe, else the node-name guess,
// else unknown.
func (l *locator) guess(node string) model.ExitInfo {
	if e, ok := l.cached(node); ok {
		return e.ExitInfo
	}
	if p, ok := geo.FromName(node); ok {
		return model.ExitInfo{Node: node, Place: &p, Source: model.LocateName}
	}
	return model.ExitInfo{Node: node, Source: model.LocateUnknown}
}

// cached returns the cache entry for node if it is still valid.
func (l *locator) cached(node string) (cacheEntry, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.loadLocked()
	e, ok := l.cache[node]
	if !ok || !l.valid(e) {
		return cacheEntry{}, false
	}
	return e, true
}

func (l *locator) valid(e cacheEntry) bool {
	if e.MeasuredAt.IsZero() {
		return false
	}
	age := l.now().Sub(e.MeasuredAt)
	return age >= 0 && age < cacheTTL
}

// loadLocked reads the cache file once; a missing or corrupt file yields
// an empty cache. Caller holds l.mu.
func (l *locator) loadLocked() {
	if l.loaded {
		return
	}
	l.loaded = true
	l.cache = map[string]cacheEntry{}
	if l.cachePath == "" {
		return
	}
	data, err := os.ReadFile(l.cachePath)
	if err != nil {
		return
	}
	var m map[string]cacheEntry
	if json.Unmarshal(data, &m) == nil && m != nil {
		l.cache = m
	}
}

// store saves a probe result, dropping expired entries on the way, and
// rewrites the cache file atomically: at once when the last write is
// flushEvery or more ago (a write error is then returned), otherwise by a
// timer at the end of the interval.
func (l *locator) store(e cacheEntry) error {
	l.mu.Lock()
	l.loadLocked()
	for k, v := range l.cache {
		if !l.valid(v) {
			delete(l.cache, k)
		}
	}
	l.cache[e.Node] = e
	if l.cachePath == "" {
		l.mu.Unlock()
		return nil
	}
	now := l.now()
	if since := now.Sub(l.lastFlush); !l.lastFlush.IsZero() && since >= 0 && since < l.flushEvery {
		if l.flushTimer == nil {
			l.flushTimer = time.AfterFunc(l.flushEvery-since, func() { l.flush() })
		}
		l.mu.Unlock()
		return nil
	}
	l.mu.Unlock()
	return l.flush()
}

// flush writes the cache file.
func (l *locator) flush() error {
	l.writeMu.Lock()
	defer l.writeMu.Unlock()
	l.mu.Lock()
	if l.flushTimer != nil {
		l.flushTimer.Stop()
		l.flushTimer = nil
	}
	l.lastFlush = l.now()
	data, err := json.MarshalIndent(l.cache, "", "  ")
	l.mu.Unlock()
	if err != nil {
		return err
	}
	return fsx.WriteFile(l.cachePath, data, 0o600)
}

// reset forgets every cached probe.
func (l *locator) reset() error {
	l.mu.Lock()
	l.loaded, l.cache = true, map[string]cacheEntry{}
	path := l.cachePath
	l.mu.Unlock()
	if path == "" {
		return nil
	}
	return l.flush()
}
