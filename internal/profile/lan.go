package profile

import (
	"fmt"
	"net"
	"net/netip"
	"strings"

	"gopkg.in/yaml.v3"
)

// What a subscription may listen on. 允许局域网 (Managed.AllowLAN) is the
// one switch that opens this machine's proxy to the network, and a
// subscription must not get around it (docs/architecture.md: a
// subscription cannot override the local listen addresses). mixed-port
// and the other inbound ports are managed or stripped (strippedKeys);
// the rest of what makes the core listen is confined here, while the
// switch is off, to the loopback interface:
//
//   - listeners: every entry's listen (Mihomo's default is all interfaces);
//   - tunnels: the local address of every entry;
//   - dns.listen, unless Lanflow writes the dns section itself.
//
// It runs on the subscription alone, before the override layers: an
// override is the user's own text and keeps what it says, and so do the
// listeners Lanflow adds later (the probe listener, HTTPS 解密, the 高级
// options).

const loopbackHost = "127.0.0.1"

// isLoopbackHost reports whether host names only this machine.
func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	a, err := netip.ParseAddr(strings.Trim(host, "[]"))
	return err == nil && a.IsLoopback()
}

// loopbackAddr returns addr ("host:port", ":port") with a loopback host;
// changed is false when it already had one, ok false when addr is not an
// address with a port.
func loopbackAddr(addr string) (out string, changed, ok bool) {
	host, port, err := net.SplitHostPort(strings.TrimSpace(addr))
	if err != nil || port == "" {
		return "", false, false
	}
	if isLoopbackHost(host) {
		return addr, false, true
	}
	return net.JoinHostPort(loopbackHost, port), true, true
}

// confineSubscription rewrites what the subscription in root listens on
// to the loopback interface and returns a warning per change. Entries it
// cannot read are removed rather than left listening.
func confineSubscription(root *yaml.Node, dnsTakenOver bool) []string {
	var warns []string
	warn := func(format string, args ...any) {
		warns = append(warns, layerSub+"："+fmt.Sprintf(format, args...))
	}
	label := func(it *yaml.Node, i int) string {
		if n := deref(mapGet(it, "name")); n != nil && n.Kind == yaml.ScalarNode && n.Value != "" {
			return n.Value
		}
		return fmt.Sprintf("#%d", i+1)
	}

	if ls := deref(mapGet(root, "listeners")); ls != nil && ls.Kind == yaml.SequenceNode {
		for i, raw := range ls.Content {
			it := deref(raw)
			if it == nil || it.Kind != yaml.MappingNode {
				continue // not a listener: the core says so
			}
			l := deref(mapGet(it, "listen"))
			if l != nil && l.Kind == yaml.ScalarNode && isLoopbackHost(l.Value) {
				continue
			}
			mapSet(it, "listen", strNode(loopbackHost))
			warn("未开启「允许局域网」，监听「%s」已改为只接受本机连接", label(it, i))
		}
	}

	if ts := deref(mapGet(root, "tunnels")); ts != nil && ts.Kind == yaml.SequenceNode {
		kept := ts.Content[:0]
		for i, raw := range ts.Content {
			it := deref(raw)
			switch {
			case it != nil && it.Kind == yaml.ScalarNode:
				// "tcp/udp,127.0.0.1:6553,114.114.114.114:53,proxy"
				parts := strings.Split(it.Value, ",")
				if len(parts) < 3 {
					kept = append(kept, raw) // malformed: the core says so
					continue
				}
				addr, changed, ok := loopbackAddr(parts[1])
				if !ok {
					warn("未开启「允许局域网」，已移除本地地址无法识别的转发隧道「%s」", it.Value)
					continue
				}
				if changed {
					parts[1] = addr
					it.Value = strings.Join(parts, ",")
					warn("未开启「允许局域网」，转发隧道 %s 已改为只接受本机连接", addr)
				}
			case it != nil && it.Kind == yaml.MappingNode:
				a := deref(mapGet(it, "address"))
				if a == nil || a.Kind != yaml.ScalarNode {
					kept = append(kept, raw)
					continue
				}
				addr, changed, ok := loopbackAddr(a.Value)
				if !ok {
					warn("未开启「允许局域网」，已移除本地地址无法识别的转发隧道「%s」", label(it, i))
					continue
				}
				if changed {
					mapSet(it, "address", strNode(addr))
					warn("未开启「允许局域网」，转发隧道 %s 已改为只接受本机连接", addr)
				}
			}
			kept = append(kept, raw)
		}
		ts.Content = kept
	}

	if d := deref(mapGet(root, "dns")); !dnsTakenOver && d != nil && d.Kind == yaml.MappingNode {
		if l := deref(mapGet(d, "listen")); l != nil {
			addr, changed, ok := "", false, false
			if l.Kind == yaml.ScalarNode {
				addr, changed, ok = loopbackAddr(l.Value)
			}
			switch {
			case !ok:
				mapDelete(d, "listen")
				warn("未开启「允许局域网」，已移除无法识别的 dns.listen")
			case changed:
				mapSet(d, "listen", strNode(addr))
				warn("未开启「允许局域网」，dns.listen 已改为 %s", addr)
			}
		}
	}
	return warns
}

// ---- alias expansion ----

// maxAliasNodes bounds the nodes resolveDanglingAliases may create. A
// subscription's own anchors expand to a few thousand nodes at most; a
// document built to grow tenfold with every level of nested aliases
// ("billion laughs") is cut off here instead of exhausting memory.
const maxAliasNodes = 1 << 20

// errAliasBomb is what composing such a document says.
var errAliasBomb = fmt.Errorf("订阅内容异常：YAML 锚点引用展开后超过 %d 个节点，已拒绝加载", maxAliasNodes)
