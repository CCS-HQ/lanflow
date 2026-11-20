package settings

import (
	"strings"

	"github.com/egoist/mygo/ui"

	"lanflow/internal/app"
	"lanflow/internal/i18n"
	"lanflow/internal/view/env"
)

// 缓存 row of 设置 › 数据: the sizes of what Lanflow caches on disk (proxy
// group and application icons, the core's release lists and cache.db,
// rotated logs) and 「清理缓存」. A backend offers it through an optional
// interface (app.Service does); without it the row is not shown.

// cacheAPI is the optional service side of the row.
type cacheAPI interface {
	CacheUsage() []app.CacheItem
	CleanCaches() (freed int64, err error)
}

// cacheState is the row's state, UI thread only. The sizes are read off
// the UI thread (the listing walks the cache directories), once each
// time the row comes into view (loaded is cleared when the tab or the
// page is left) and again after a cleaning; there is no polling.
type cacheState struct {
	items   []app.CacheItem
	loaded  bool // items are as of this showing of the row
	loading bool
	busy    bool // 清理缓存 runs
}

// loadCaches reads the sizes in the background.
func (p *Page) loadCaches(api cacheAPI) {
	cs := &p.caches
	if cs.loading {
		return
	}
	cs.loading = true
	var items []app.CacheItem
	p.e.Then(func() error {
		items = api.CacheUsage()
		if items == nil {
			items = []app.CacheItem{}
		}
		return nil
	}, func(error) {
		cs.items, cs.loaded, cs.loading = items, true, false
	})
}

// Hidden is called by the shell when the page goes out of sight: the
// cache sizes are read again when it shows next.
func (p *Page) Hidden() { p.caches.loaded = false }

// cacheRow draws the row.
func (p *Page) cacheRow(c *ui.Context) {
	api, ok := p.e.API.(cacheAPI)
	if !ok {
		return
	}
	e := p.e
	cs := &p.caches
	if !cs.loaded && !cs.busy {
		p.loadCaches(api)
	}
	desc := cacheSummary(cs.items)
	if cs.items == nil {
		desc = i18n.T("正在读取…")
	}
	e.Kit.Row(c, i18n.T("缓存"), desc, func() {
		b := ui.Button(c, i18n.T("清理缓存")).Disabled(cs.busy || cs.items == nil)
		if b.Clicked() {
			cs.busy = true
			var freed int64
			e.Then(func() (err error) {
				freed, err = api.CleanCaches()
				return err
			}, func(err error) {
				cs.busy, cs.loaded = false, false // read again: the sizes changed
				if err != nil {
					e.Toast(err.Error(), true)
					return
				}
				e.Toast(i18n.Tf("已清理 %s", env.FmtBytes(freed)), false)
			})
		}
	})
}

// cacheSummary is the row's description: each cache with a size, then
// the total, e.g. 「图标 1.2 MB · 内核缓存 128.0 KB · 共 1.3 MB」.
func cacheSummary(items []app.CacheItem) string {
	var parts []string
	var total int64
	for _, it := range items {
		total += it.Bytes
		if it.Bytes > 0 {
			parts = append(parts, it.Name+" "+env.FmtBytes(it.Bytes))
		}
	}
	if total == 0 {
		return i18n.T("没有可清理的缓存")
	}
	parts = append(parts, i18n.Tf("共 %s", env.FmtBytes(total)))
	return strings.Join(parts, " · ")
}
