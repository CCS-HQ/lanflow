package remote

import (
	"container/list"
	"encoding/json"
	"reflect"
	"sync"

	"lanflow/internal/app"
	"lanflow/internal/rpc"
)

// The window reads the API on every frame (app.API: reads are cheap);
// over the local interface each read is a round trip, so the window's
// client keeps every read's result until a "changed" event of a topic the
// read depends on, and drops everything after an action (whose effect the
// events report a moment later).

const lookupIPCap = 2048

// topicReads lists the reads each topic invalidates. A read missing from
// every list is dropped on every event; staticReads are never dropped.
var topicReads = map[app.Topic][]string{
	app.TopicStatus:       {"Status", "TUNHelperInstalled", "CaptureStatus"},
	app.TopicTraffic:      {"Status", "TrafficHistory"},
	app.TopicMetrics:      {"Status"},
	app.TopicProxies:      {"Proxies", "ProxyProviders", "IconData", "ProbeRegions", "Status", "ManualNodes", "ProxyChains"},
	app.TopicSubs:         {"Subscriptions", "EffectiveDNS", "EffectiveSniffer", "ProbeRegions", "Status", "Proxies", "OverrideList", "Overrides", "ManualNodes", "ProxyChains"},
	app.TopicConns:        {"Connections", "ClosedConnections", "Status"},
	app.TopicLogs:         {"Logs", "AppLogs"},
	app.TopicRules:        {"Rules", "CustomRules", "RuleProviders", "Status"},
	app.TopicExits:        {"ExitRoutes", "You"},
	app.TopicNet:          {"NetInfo", "Sites", "LeakCheck"},
	app.TopicSettings:     {"Settings", "Overrides", "OverrideList", "EffectiveDNS", "EffectiveSniffer", "WebDAVInfo", "ConfigSyncStatus", "FeedbackInfo", "UIState", "GeoDataInfo", "Status", "Proxies", "CacheUsage", "CaptureStatus"},
	app.TopicCoreDownload: {"CoreDownload"},
	app.TopicCores:        {"InstalledCores", "Status"},
	app.TopicProbe:        {"Probe", "ProbeRegions"},
	app.TopicStats:        {"TrafficStats"},
	app.TopicProcIcons:    {"ProcessIcon"},
	app.TopicCapture:      {"CaptureStatus", "CaptureFlows"},
}

var staticReads = map[string]bool{"ConfigDir": true, "LogDir": true, "ATrustReference": true, "LookupIP": true}

// uncachedReads are reads with effects or that block.
var uncachedReads = map[string]bool{"TakeNotices": true, "TestMirrors": true, "CheckCaptureTarget": true}

var listedReads = func() map[string]bool {
	m := map[string]bool{}
	for _, ms := range topicReads {
		for _, x := range ms {
			m[x] = true
		}
	}
	return m
}()

// cacheable reports whether method is a read whose result may be kept.
func cacheable(method string) bool {
	if uncachedReads[method] {
		return false
	}
	m, ok := rpc.Lookup(method)
	return ok && !m.Agent && !m.Error && len(m.Results) > 0
}

func uncachedRead(method string) bool { return uncachedReads[method] }

type cacheEntry struct {
	method string
	vals   []reflect.Value
}

type cacheGen struct {
	reset    uint64
	clear    uint64
	method   uint64
	unlisted uint64
}

type readCache struct {
	mu          sync.Mutex
	resetGen    uint64
	clearGen    uint64
	unlistedGen uint64
	methGen     map[string]uint64
	m           map[string]cacheEntry
	lookupCap   int
	lookupList  *list.List
	lookupElems map[string]*list.Element
}

func newReadCache() *readCache {
	return &readCache{
		methGen:     map[string]uint64{},
		m:           map[string]cacheEntry{},
		lookupCap:   lookupIPCap,
		lookupList:  list.New(),
		lookupElems: map[string]*list.Element{},
	}
}

func cacheKey(method string, args []any) string {
	if len(args) == 0 {
		return method
	}
	b, _ := json.Marshal(args)
	return method + string(b)
}

func (c *readCache) genLocked(method string) cacheGen {
	if staticReads[method] {
		return cacheGen{reset: c.resetGen, method: c.methGen[method]}
	}
	if listedReads[method] {
		return cacheGen{reset: c.resetGen, clear: c.clearGen, method: c.methGen[method]}
	}
	return cacheGen{reset: c.resetGen, clear: c.clearGen, method: c.methGen[method], unlisted: c.unlistedGen}
}

// get fills outs from the cache.
func (c *readCache) get(method string, args []any, outs []any) bool {
	if !cacheable(method) {
		return false
	}
	key := cacheKey(method, args)
	c.mu.Lock()
	e, ok := c.m[key]
	if ok && len(e.vals) == len(outs) && method == "LookupIP" {
		if elem, exists := c.lookupElems[key]; exists {
			c.lookupList.MoveToFront(elem)
		}
	}
	c.mu.Unlock()
	if !ok || len(e.vals) != len(outs) {
		return false
	}
	for i, o := range outs {
		reflect.ValueOf(o).Elem().Set(copyTop(e.vals[i]))
	}
	return true
}

// generation is read before a call; putAt stores only when nothing invalidated
// that method meanwhile.
func (c *readCache) generation(method string) cacheGen {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.genLocked(method)
}

func (c *readCache) putAt(gen cacheGen, method string, args []any, outs []any) {
	vals := make([]reflect.Value, len(outs))
	for i, o := range outs {
		vals[i] = copyTop(reflect.ValueOf(o).Elem())
	}
	key := cacheKey(method, args)
	c.mu.Lock()
	defer c.mu.Unlock()
	if gen != c.genLocked(method) {
		return
	}
	c.m[key] = cacheEntry{method: method, vals: vals}
	if method == "LookupIP" {
		c.touchLookupLocked(key)
	}
}

func (c *readCache) touchLookupLocked(key string) {
	if elem, ok := c.lookupElems[key]; ok {
		c.lookupList.MoveToFront(elem)
		return
	}
	elem := c.lookupList.PushFront(key)
	c.lookupElems[key] = elem
	capLimit := c.lookupCap
	if capLimit <= 0 {
		capLimit = lookupIPCap
	}
	for c.lookupList.Len() > capLimit {
		back := c.lookupList.Back()
		if back == nil {
			break
		}
		evicted := back.Value.(string)
		c.lookupList.Remove(back)
		delete(c.lookupElems, evicted)
		delete(c.m, evicted)
	}
}

// invalidate drops the reads that depend on t and those depending on no
// topic in particular.
func (c *readCache) invalidate(t app.Topic) {
	drop := map[string]bool{}
	for _, m := range topicReads[t] {
		drop[m] = true
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, m := range topicReads[t] {
		c.methGen[m]++
	}
	c.unlistedGen++
	for k, e := range c.m {
		if drop[e.method] || (!listedReads[e.method] && !staticReads[e.method]) {
			delete(c.m, k)
		}
	}
}

// drop forgets every result of method.
func (c *readCache) drop(method string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.methGen[method]++
	for k, e := range c.m {
		if e.method == method {
			delete(c.m, k)
		}
	}
	if method == "LookupIP" {
		c.lookupList.Init()
		clear(c.lookupElems)
	}
}

// clear forgets everything but the static reads.
func (c *readCache) clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.clearGen++
	for k, e := range c.m {
		if !staticReads[e.method] {
			delete(c.m, k)
		}
	}
}

// reset forgets everything (a new connection: maybe a new service).
func (c *readCache) reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.resetGen++
	clear(c.m)
	c.lookupList.Init()
	clear(c.lookupElems)
}

// copyTop copies a slice's backing array, so that a view sorting the list
// it was given in place does not change the cached one.
func copyTop(v reflect.Value) reflect.Value {
	if v.Kind() != reflect.Slice || v.IsNil() {
		return v
	}
	out := reflect.MakeSlice(v.Type(), v.Len(), v.Len())
	reflect.Copy(out, v)
	return out
}
