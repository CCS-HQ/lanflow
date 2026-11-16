// The exit map's data: the routes with their places, colours and eased
// speeds, where 「你」 is, and the destination countries of each route.

package overview

import (
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"

	"lanflow/internal/geo"
	"lanflow/internal/i18n"
	"lanflow/internal/model"
)

// routeColors are the colors of the routes after the main one (系统紫、粉、青、橙).
var routeColors = []ui.Color{ui.Hex("#AF52DE"), ui.Hex("#FF2D55"), ui.Hex("#30B0C7"), ui.Hex("#FF9500")}

// dest is one destination country of a route.
type dest struct {
	place model.Place
	conns int
	speed int64
	seen  time.Time
}

// destState caches the destination countries by route and the IP lookups.
type destState struct {
	at    time.Time
	by    map[string]map[string]*dest // group → country → dest
	lists map[string][]dest           // group → top destinations
	geo   map[string]*model.Place     // IP → country place (nil = unknown)
}

// route is one line of the map and the list: the groups whose traffic
// leaves through the same node, merged. Group is the one that names the
// route (the main group when it is among them), Groups all of them in the
// core's order, Speed and Connections their sums.
type route struct {
	model.ExitRoute
	Groups []string
	place  *model.Place
	color  ui.Color
}

// merged reports whether the route stands for more than one group.
func (r route) merged() bool { return len(r.Groups) > 1 }

// connsQuery is what the connections page is filtered by for this route:
// its group, or for merged groups the node they share, which every one of
// their connections carries in its chain.
func (r route) connsQuery() string {
	if r.merged() && r.Node != "" {
		return r.Node
	}
	return r.Group
}

// mergeRoutes merges the groups that leave through the same node: several
// rules hitting one exit are one line on the map and one entry in the
// list. The first route of a node keeps its place in the order; the main
// group names the route, and a measured exit wins over a guessed one.
func mergeRoutes(rs []model.ExitRoute) []route {
	out := make([]route, 0, len(rs))
	at := map[string]int{}
	for _, r := range rs {
		i, ok := at[r.Node]
		if r.Node == "" || !ok {
			if r.Node != "" {
				at[r.Node] = len(out)
			}
			out = append(out, route{ExitRoute: r, Groups: []string{r.Group}, place: r.Exit.Place})
			continue
		}
		m := &out[i]
		m.Groups = append(m.Groups, r.Group)
		m.Speed += r.Speed
		m.Connections += r.Connections
		if r.Main && !m.Main {
			m.Group, m.Main = r.Group, true
		}
		if better(r.Exit, m.Exit) {
			m.Exit, m.place = r.Exit, r.Exit.Place
		}
	}
	// The main route stays first (当前出口).
	for i := range out {
		if out[i].Main && i > 0 {
			m := out[i]
			copy(out[1:i+1], out[:i])
			out[0] = m
			break
		}
	}
	return out
}

// better reports whether exit a says more than b: a place where b has
// none, or a measurement where b is a guess.
func better(a, b model.ExitInfo) bool {
	if a.Place == nil {
		return false
	}
	if b.Place == nil {
		return true
	}
	return a.Source == model.LocateProbe && b.Source != model.LocateProbe
}

// youPlace returns the user's place and whether it is a guess. Only an
// endpoint measured over the physical interface (BoundIface) is the
// user's location: anything else may be a TUN's proxy exit.
func (p *Page) youPlace() (*model.Place, bool) {
	if y := p.e.API.You(); y != nil && y.Place != nil && y.BoundIface != "" {
		return y.Place, false
	}
	// Before a measurement 「你」 sits at the system time zone's city,
	// marked 「推测」 (docs/design.md §5.1 页面状态); geo.LocalZone knows
	// where each OS keeps the zone, Windows registry included.
	if pl := localPlace(); pl != nil {
		return pl, true
	}
	return nil, true
}

// localPlace is the city of the system time zone, nil if unknown; looked
// up once, as finding the zone reads files (and on Windows the registry).
var localPlace = sync.OnceValue(func() *model.Place {
	if pl, ok := geo.FromTimeZone(geo.LocalZone()); ok {
		return &pl
	}
	return nil
})

// routes collects the exit routes with their places and colors, and
// eases the smoothed speeds towards the measured ones by the time since
// the last call (k = 1 − e^(−dt/τ)), so the easing does not depend on
// how often the page is built.
func (p *Page) routes(now time.Time) []route {
	out := mergeRoutes(p.e.API.ExitRoutes())
	k := 0
	seen := map[string]bool{}
	for i := range out {
		r := &out[i]
		if i == 0 || r.Main {
			r.color = ui.Color{}
		} else {
			r.color = routeColors[k%len(routeColors)]
			k++
		}
		seen[r.Group] = true
		f := p.flows[r.Group]
		if f == nil {
			f = &flow{speed: float64(r.Speed), eased: now}
			p.flows[r.Group] = f
		}
		f.ease(float64(r.Speed), now)
	}
	for g := range p.flows {
		if !seen[g] {
			delete(p.flows, g)
		}
	}
	return out
}

// ease moves the shown speed towards target by the time since the last
// easing; a flow drawn before it had a speed (eased zero) takes it at once.
func (f *flow) ease(target float64, now time.Time) {
	if f.eased.IsZero() {
		f.speed, f.eased = target, now
		return
	}
	dt := now.Sub(f.eased)
	if dt <= 0 {
		return
	}
	f.eased = now
	k := 1 - math.Exp(-float64(dt)/float64(speedTau))
	f.speed += (target - f.speed) * k
}

// destinations returns the destination countries of each route's live
// connections (docs/exit-map.md §4 layer B), resolved offline through the
// GeoIP database and cached by IP. It recomputes at most every destEvery.
func (p *Page) destinations(rs []route, mode model.Mode, now time.Time) map[string][]dest {
	d := &p.dests
	if d.by == nil {
		d.by = map[string]map[string]*dest{}
		d.geo = map[string]*model.Place{}
	}
	if !d.at.IsZero() && now.Sub(d.at) < destEvery {
		return d.lists
	}
	d.at = now
	// A connection counts for the route its group was merged into.
	routeOf := map[string]string{}
	for _, r := range rs {
		for _, g := range r.Groups {
			routeOf[g] = r.Group
		}
		routeOf[r.Group] = r.Group
	}
	for _, m := range d.by {
		for _, x := range m {
			x.conns, x.speed = 0, 0
		}
	}
	for _, cn := range p.e.API.Connections() {
		if len(cn.Chains) == 0 || cn.DstIP == "" {
			continue
		}
		g := cn.Chains[0]
		if mode == model.ModeGlobal {
			g = "GLOBAL"
		}
		g, ok := routeOf[g]
		if !ok {
			continue
		}
		pl := p.countryOf(cn.DstIP)
		if pl == nil {
			continue
		}
		m := d.by[g]
		if m == nil {
			m = map[string]*dest{}
			d.by[g] = m
		}
		x := m[pl.Country]
		if x == nil {
			x = &dest{place: *pl}
			m[pl.Country] = x
		}
		x.conns++
		x.speed += cn.UpSpeed + cn.DownSpeed
		x.seen = now
	}
	lists := map[string][]dest{}
	for g, m := range d.by {
		if routeOf[g] != g {
			delete(d.by, g)
			continue
		}
		var ds []dest
		for c, x := range m {
			if now.Sub(x.seen) > destKeep {
				delete(m, c)
				continue
			}
			ds = append(ds, *x)
		}
		sort.Slice(ds, func(i, j int) bool {
			if ds[i].conns != ds[j].conns {
				return ds[i].conns > ds[j].conns
			}
			if !ds[i].seen.Equal(ds[j].seen) {
				return ds[i].seen.After(ds[j].seen)
			}
			return ds[i].place.Country < ds[j].place.Country
		})
		if len(ds) > maxDests {
			ds = ds[:maxDests]
		}
		if len(ds) > 0 {
			lists[g] = ds
		}
	}
	d.lists = lists
	return lists
}

// countryOf resolves a destination IP to its country's place, nil when
// unknown; results are cached per IP.
func (p *Page) countryOf(ip string) *model.Place {
	if pl, ok := p.dests.geo[ip]; ok {
		return pl
	}
	if len(p.dests.geo) > 4096 {
		p.dests.geo = map[string]*model.Place{}
	}
	var pl *model.Place
	if ep := p.e.API.LookupIP(ip); ep.Place != nil && ep.Place.Country != "" {
		cp := *ep.Place
		pl = &cp
	}
	p.dests.geo[ip] = pl
	return pl
}

func mapDescription(you *model.Place, rs []route, dests map[string][]dest) string {
	var b strings.Builder
	b.WriteString(i18n.T("出口地图"))
	if you != nil {
		b.WriteString(i18n.Tf("：从你所在的%s", you.City))
	}
	var cities, countries []string
	seen := map[string]bool{}
	for _, r := range rs {
		if r.place != nil {
			cities = append(cities, r.place.City)
		}
		for _, d := range dests[r.Group] {
			if !seen[d.place.Country] {
				seen[d.place.Country] = true
				countries = append(countries, d.place.City)
			}
		}
	}
	if len(cities) > 0 {
		b.WriteString(i18n.Tf("到%s", strings.Join(cities, i18n.T("、"))))
	}
	if len(countries) > 0 {
		b.WriteString(i18n.Tf("；目标国家：%s", strings.Join(countries, i18n.T("、"))))
	}
	return b.String()
}

func routeColor(r route, t *ui.Theme) ui.Color {
	if r.color == (ui.Color{}) {
		return t.Accent
	}
	return r.color
}
