package fake

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"lanflow/internal/app"
	"lanflow/internal/i18n"
	"lanflow/internal/model"
	"lanflow/internal/profile"
)

// fakeAggSource is what a link source "downloads" in the fake: a few
// nodes named after the link's host. A link containing "fail" fails.
func fakeAggSource(url string) profile.AggregateInput {
	if strings.Contains(url, "fail") {
		return profile.AggregateInput{Err: errors.New(i18n.T("下载失败：HTTP 403"))}
	}
	host := strings.TrimPrefix(strings.TrimPrefix(url, "https://"), "http://")
	host, _, _ = strings.Cut(host, "/")
	var b strings.Builder
	b.WriteString("proxies:\n")
	for i, r := range []string{"香港", "日本", "新加坡", "美国", "台湾", "剩余流量：88 GB"} {
		fmt.Fprintf(&b, "  - {name: %q, type: ss, server: n%d.%s, port: 8388, password: p, cipher: aes-128-gcm}\n", fmt.Sprintf("%s %02d", r, i+1), i, host)
	}
	return profile.AggregateInput{Data: []byte(b.String()), Fetched: profile.Fetched{Download: 12 << 30, Total: 100 << 30, Expire: time.Now().AddDate(0, 6, 0)}}
}

// FetchEnv set to 1 makes the fake download the link sources of an
// aggregate subscription for real (a development run that should show
// what a real subscription gives; no core is involved). Off, a link
// gives made-up nodes, as in the tests.
const FetchEnv = "LANFLOW_FAKE_FETCH"

// fetchAggSource downloads a link source.
func fetchAggSource(url, ua string) profile.AggregateInput {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	got, err := profile.FetchRaw(ctx, &http.Client{Timeout: 30 * time.Second}, url, ua)
	if err != nil {
		return profile.AggregateInput{Err: err}
	}
	return profile.AggregateInput{Data: got.Data, Fetched: got}
}

// aggInputs reads the sources of spec; it may download, so it runs
// without the fake's lock.
func (f *Fake) aggInputs(spec model.AggregateSpec, ua string) []profile.AggregateInput {
	var out []profile.AggregateInput
	for _, src := range spec.Sources {
		if src.Disabled {
			continue
		}
		var in profile.AggregateInput
		switch src.Kind {
		case model.AggSourceURL:
			if os.Getenv(FetchEnv) == "1" {
				in = fetchAggSource(src.URL, ua)
			} else {
				in = fakeAggSource(src.URL)
			}
		case model.AggSourceLinks:
			in.Data = []byte(src.Links)
		case model.AggSourceYAML:
			in.Data = []byte(src.YAML)
		case model.AggSourceSub:
			in = fakeAggSource("https://" + src.SubID + ".example.com")
		}
		in.Source = src
		out = append(out, in)
	}
	return out
}

func withIDs(spec model.AggregateSpec) model.AggregateSpec {
	spec = spec.Clone()
	for i := range spec.Sources {
		if spec.Sources[i].ID == "" {
			spec.Sources[i].ID = fmt.Sprintf("src%d", i+1)
		}
	}
	return spec
}

func (f *Fake) AggregateSpec(id string) (model.AggregateSpec, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	spec, ok := f.aggs[id]
	if !ok {
		return model.AggregateSpec{}, errors.New(i18n.T("这不是聚合订阅。"))
	}
	return profile.UpgradeAggregateSpec(spec.Clone()), nil
}

func (f *Fake) AggregateTemplates() ([]model.AggregateTemplate, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]model.AggregateTemplate{}, f.aggTpls...), nil
}

func (f *Fake) SaveAggregateTemplate(t model.AggregateTemplate) (out model.AggregateTemplate, err error) {
	err = f.do(func() error {
		t.Name = strings.TrimSpace(t.Name)
		if t.Name == "" {
			return errors.New(i18n.T("请填写模板名称。"))
		}
		t.Spec = t.Spec.Clone()
		t.Spec.Sources, t.Spec.Hidden, t.Spec.NodeOrder, t.Spec.Template = nil, nil, nil, ""
		if t.ID == "" {
			t.ID, t.Created = fmt.Sprintf("t%d", len(f.aggTpls)+1), time.Now()
			f.aggTpls = append(f.aggTpls, t)
		} else {
			for i := range f.aggTpls {
				if f.aggTpls[i].ID == t.ID {
					f.aggTpls[i] = t
				}
			}
		}
		out = t
		return nil
	}, app.TopicSubs)
	return out, err
}

func (f *Fake) DeleteAggregateTemplate(id string) error {
	return f.do(func() error {
		out := f.aggTpls[:0:0]
		for _, t := range f.aggTpls {
			if t.ID != id {
				out = append(out, t)
			}
		}
		f.aggTpls = out
		return nil
	}, app.TopicSubs)
}

func (f *Fake) PreviewAggregate(in model.Subscription, spec model.AggregateSpec) (pv model.AggregatePreview, err error) {
	spec = withIDs(spec)
	inputs := f.aggInputs(spec, in.UserAgent)
	err = f.do(func() error {
		pv, _, err = profile.BuildAggregate(spec, inputs, i18n.Tf)
		return err
	})
	return pv, err
}

func (f *Fake) SaveAggregate(in model.Subscription, spec model.AggregateSpec, use bool) (sub model.Subscription, err error) {
	spec = withIDs(spec)
	inputs := f.aggInputs(spec, in.UserAgent)
	err = f.do(func() error {
		pv, data, err := profile.BuildAggregate(spec, inputs, i18n.Tf)
		if err != nil {
			return err
		}
		if strings.TrimSpace(in.Name) == "" {
			return errors.New(i18n.T("请填写订阅名称。"))
		}
		s := f.sub(in.ID)
		if s == nil {
			f.subs = append(f.subs, model.Subscription{ID: fmt.Sprintf("s%d", len(f.subs)+10), Source: model.SourceAggregate})
			s = &f.subs[len(f.subs)-1]
		}
		s.Name, s.AutoUpdateHours, s.UserAgent, s.ViaProxy, s.TimeoutSec = strings.TrimSpace(in.Name), in.AutoUpdateHours, in.UserAgent, in.ViaProxy, in.TimeoutSec
		s.Upload, s.Download, s.Total, s.Expire = 0, 0, 0, time.Time{}
		for _, r := range pv.Sources {
			s.Download += r.Download
			s.Total += r.Total
			if !r.Expire.IsZero() && (s.Expire.IsZero() || r.Expire.Before(s.Expire)) {
				s.Expire = r.Expire
			}
		}
		s.UpdatedAt, s.LastError = time.Now(), ""
		if f.aggs == nil {
			f.aggs = map[string]model.AggregateSpec{}
		}
		f.aggs[s.ID] = spec
		if f.aggYAML == nil {
			f.aggYAML = map[string][]byte{}
		}
		f.aggYAML[s.ID] = data
		f.st.HasSubscriptions = true
		if use {
			f.setCurrent(s.ID)
		}
		if s.Current {
			f.showAggProxies(s.ID)
		}
		sub = *s
		return nil
	}, app.TopicSubs)
	return sub, err
}

// showAggProxies makes the proxies those of the profile aggregate
// subscription id generated, or the sample ones again for another
// subscription. The caller holds f.mu.
func (f *Fake) showAggProxies(id string) {
	data, ok := f.aggYAML[id]
	if !ok {
		if f.pxSample != nil {
			f.px, f.pxSample = *f.pxSample, nil
		}
		return
	}
	var doc struct {
		Proxies []struct {
			Name string `yaml:"name"`
			Type string `yaml:"type"`
			UDP  bool   `yaml:"udp"`
		} `yaml:"proxies"`
		Groups []struct {
			Name    string   `yaml:"name"`
			Type    string   `yaml:"type"`
			Proxies []string `yaml:"proxies"`
		} `yaml:"proxy-groups"`
	}
	if yaml.Unmarshal(data, &doc) != nil {
		return
	}
	if f.pxSample == nil {
		sample := f.px
		f.pxSample = &sample
	}
	pm := map[string]model.Proxy{"DIRECT": {Name: "DIRECT", Type: "Direct"}, "REJECT": {Name: "REJECT", Type: "Reject"}}
	var names []string
	for _, p := range doc.Proxies {
		pm[p.Name] = model.Proxy{Name: p.Name, Type: fakeProxyType(p.Type), UDP: p.UDP}
		names = append(names, p.Name)
	}
	var groups []model.ProxyGroup
	global := []string{"DIRECT", "REJECT"}
	for _, g := range doc.Groups {
		pg := model.ProxyGroup{Name: g.Name, Type: model.GroupType(g.Type), All: g.Proxies}
		if len(g.Proxies) > 0 {
			pg.Now = g.Proxies[0]
		}
		groups = append(groups, pg)
		pm[g.Name] = model.Proxy{Name: g.Name, Type: string(pg.Type)}
		global = append(global, g.Name)
	}
	groups = append(groups, model.ProxyGroup{Name: "GLOBAL", Type: model.GroupSelect, Now: "DIRECT", All: append(global, names...)})
	main := ""
	if len(doc.Groups) > 0 {
		main = doc.Groups[0].Name
	}
	f.px = model.ProxySnapshot{Groups: groups, Proxies: pm, MainGroup: main}
}

// fakeProxyType is the core's name of a proxy type ("ss" → Shadowsocks).
func fakeProxyType(t string) string {
	switch strings.ToLower(t) {
	case "ss":
		return "Shadowsocks"
	case "ssr":
		return "ShadowsocksR"
	case "vmess":
		return "Vmess"
	case "vless":
		return "Vless"
	case "trojan":
		return "Trojan"
	case "hysteria2":
		return "Hysteria2"
	case "hysteria":
		return "Hysteria"
	case "tuic":
		return "Tuic"
	case "anytls":
		return "AnyTLS"
	case "socks5":
		return "Socks5"
	case "http":
		return "Http"
	case "direct":
		return "Direct"
	}
	return t
}
