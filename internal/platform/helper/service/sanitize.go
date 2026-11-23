package service

import (
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// maxConfigSize bounds the config the helper reads as root/SYSTEM. A
// subscription with thousands of nodes and inline rules is one or two
// megabytes.
const maxConfigSize = 16 << 20

// sanitizeTimeout bounds checking one config. The check is linear in the
// config's size (see sanitizeConfig) and takes well under a second for
// maxConfigSize; the limit is there so that nothing a config can contain
// keeps a start waiting.
const sanitizeTimeout = 10 * time.Second

// maxConfigVisits bounds how many mapping entries the check looks at
// while following aliases and merges, which a config can nest to make a
// small file expand without end.
const maxConfigVisits = 2_000_000

// strippedConfigKeys are top-level keys the helper removes from the config
// before the privileged core reads it. The controller is given on the
// command line (a unix socket or a pipe only the installing user can
// open); a config must not add a second one, least of all a TCP one that
// lets the network drive a core running as root, nor make that core fetch
// and unpack a web UI. Lanflow's own configs never carry these keys
// (internal/profile strips them as well), so they pass unchanged.
var strippedConfigKeys = map[string]bool{
	"external-controller":      true,
	"external-controller-tls":  true,
	"external-controller-unix": true,
	"external-controller-pipe": true,
	"external-controller-cors": true,
	"external-doh-server":      true,
	"external-ui":              true,
	"external-ui-url":          true,
	"external-ui-name":         true,
	"secret":                   true,
}

// strippedNestedKeys are keys removed from a top-level section.
//
// ntp.write-to-system makes Mihomo set the system clock from the NTP
// server the config names, which only a core with privileges can do: the
// user's config would then move root's clock (the key is in the bundled
// core's binary; Lanflow never writes it).
var strippedNestedKeys = map[string]map[string]bool{
	"ntp": {"write-to-system": true},
}

// providerSections are the top-level maps whose entries may name a file
// the core reads and writes ("path").
var providerSections = map[string]bool{"proxy-providers": true, "rule-providers": true}

// reservedName reports whether first, the first element of a provider
// path (whole: its only one), is an entry of the helper's work directory
// that a provider path must not name or go through: the config the core
// loads (a provider file of that name would be copied over the cleaned
// config), the databases and the cache (a path below one would turn it
// into a directory nobody but root could repair), the directories of
// providers without a path, and the helper's temporary files.
func reservedName(first string, whole bool) bool {
	first = strings.ToLower(first)
	switch {
	case first == strings.ToLower(stagedConfigName), first == "cache.db", geoNames[first]:
		return true
	case strings.HasPrefix(first, tempPrefix):
		return true
	case whole && (first == "proxies" || first == "rules"):
		// As a file it would take the directory's place.
		return true
	}
	return false
}

// sanitized is a config the privileged core may load.
type sanitized struct {
	// YAML is the config to write. It is the input itself, byte for byte,
	// unless keys had to be removed.
	YAML []byte
	// Removed lists the keys that were removed ("ntp.write-to-system" for
	// a nested one), sorted.
	Removed []string
	// ProviderPaths are the provider files the config names, relative to
	// the work directory, cleaned and sorted.
	ProviderPaths []string
}

// sanitizeConfig checks a Mihomo config written by the user (or by
// anything running as the user) before the core that runs as root/SYSTEM
// loads it from the helper's work directory:
//
//   - it must be a YAML mapping without duplicate keys;
//   - strippedConfigKeys and strippedNestedKeys are removed. One that
//     arrives through a merge key ("<<") cannot be removed without
//     rewriting what it merges, so such a config is refused;
//   - a provider's path must stay inside the work directory (relative, no
//     "..", no drive or root) and must not be, or lie below, one of the
//     helper's own entries there (reservedName). Anything else is refused
//     rather than rewritten, so a provider never silently loads another
//     file. A path a merge would bring in is checked like a written one.
//
// The config is decoded into yaml.Node, not into Go maps: yaml.v3 checks a
// map's keys for duplicates by comparing every pair, which takes minutes
// for a config of a megabyte with many keys. Duplicates are found here
// with a set, aliases are followed by hand, and the whole check is linear
// in the size of the file (maxConfigVisits bounds what aliases add).
//
// This is the helper's own line of defence for the config a run starts
// with. It does not make the config trusted: the user's processes can
// reach the controller and replace the running config, which this code
// never sees. What holds then is that the core's work directory belongs to
// root/SYSTEM, so the core's own check that every path stays inside it
// cannot be led elsewhere through a link.
func sanitizeConfig(in []byte) (sanitized, error) {
	if len(in) > maxConfigSize {
		return sanitized{}, fmt.Errorf("配置文件过大（%d 字节，上限 %d）", len(in), maxConfigSize)
	}
	type result struct {
		out sanitized
		err error
	}
	done := make(chan result, 1)
	go func() {
		out, err := sanitizeNow(in)
		done <- result{out, err}
	}()
	timer := time.NewTimer(sanitizeTimeout)
	defer timer.Stop()
	select {
	case r := <-done:
		return r.out, r.err
	case <-timer.C:
		// The goroutine ends on its own: the work is bounded by the size
		// limit and maxConfigVisits.
		return sanitized{}, errors.New("检查配置超时")
	}
}

// checker carries the visit budget of one sanitizeNow.
type checker struct{ visits int }

var errTooComplex = errors.New("配置中的别名或合并嵌套过多")

func (c *checker) visit(n int) error {
	c.visits += n
	if c.visits > maxConfigVisits {
		return errTooComplex
	}
	return nil
}

// deref follows aliases to the node they name.
func deref(n *yaml.Node) *yaml.Node {
	for i := 0; n != nil && n.Kind == yaml.AliasNode && i < 64; i++ {
		n = n.Alias
	}
	return n
}

func isMergeKey(k *yaml.Node) bool {
	return k.Kind == yaml.ScalarNode && k.Value == "<<" && (k.Tag == "" || k.Tag == "!" || k.Tag == "!!merge")
}

// pairs calls fn for every key/value of mapping m itself, refusing a
// duplicate key. Merge keys are passed to merge instead (nil: ignored).
func (c *checker) pairs(m *yaml.Node, what string, fn func(i int, key string, v *yaml.Node) error, merge func(v *yaml.Node) error) error {
	if err := c.visit(len(m.Content) / 2); err != nil {
		return err
	}
	seen := make(map[string]struct{}, len(m.Content)/2)
	for i := 0; i+1 < len(m.Content); i += 2 {
		k, v := m.Content[i], m.Content[i+1]
		if isMergeKey(k) {
			if merge != nil {
				if err := merge(v); err != nil {
					return err
				}
			}
			continue
		}
		if k.Kind != yaml.ScalarNode {
			if what == "" {
				return errors.New("配置的顶层键必须是字符串")
			}
			continue
		}
		if _, dup := seen[k.Value]; dup {
			if what == "" {
				return fmt.Errorf("配置中有重复的键 %s", k.Value)
			}
			return fmt.Errorf("%s 中有重复的键 %s", what, k.Value)
		}
		seen[k.Value] = struct{}{}
		if err := fn(i, k.Value, v); err != nil {
			return err
		}
	}
	return nil
}

// merged calls fn for every mapping the value of a merge key brings in
// (one mapping or a sequence of them, usually aliases).
func merged(v *yaml.Node, fn func(m *yaml.Node) error) error {
	v = deref(v)
	if v == nil {
		return nil
	}
	switch v.Kind {
	case yaml.MappingNode:
		return fn(v)
	case yaml.SequenceNode:
		for _, item := range v.Content {
			if item = deref(item); item != nil && item.Kind == yaml.MappingNode {
				if err := fn(item); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// field calls fn for every value key has in mapping m: the one written
// there and those merges bring in, at any depth. YAML lets the written
// one win; checking all of them is the stricter reading.
func (c *checker) field(m *yaml.Node, what, key string, depth int, fn func(v *yaml.Node, viaMerge bool) error) error {
	if depth > 32 {
		return errTooComplex
	}
	return c.pairs(m, what, func(_ int, k string, v *yaml.Node) error {
		if k != key {
			return nil
		}
		return fn(v, depth > 0)
	}, func(v *yaml.Node) error {
		return merged(v, func(sub *yaml.Node) error {
			return c.field(sub, what, key, depth+1, func(v *yaml.Node, _ bool) error { return fn(v, true) })
		})
	})
}

func sanitizeNow(in []byte) (sanitized, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(in, &doc); err != nil {
		return sanitized{}, fmt.Errorf("配置文件不是有效的 YAML: %w", err)
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) != 1 {
		return sanitized{}, errors.New("配置文件为空或不是 YAML 映射")
	}
	top := deref(doc.Content[0])
	if top == nil || top.Kind != yaml.MappingNode {
		return sanitized{}, errors.New("配置文件为空或不是 YAML 映射")
	}
	c := &checker{}
	out := sanitized{YAML: in}
	paths := map[string]bool{}
	var drop []int // indexes of top-level keys to remove

	// section handles one top-level key, written or merged in.
	section := func(i int, key string, v *yaml.Node, viaMerge bool) error {
		lower := strings.ToLower(key)
		if strippedConfigKeys[lower] {
			if viaMerge {
				return fmt.Errorf("配置通过合并键（<<）带入了 %s，权限组件无法去除，请直接删除它", key)
			}
			out.Removed = append(out.Removed, key)
			drop = append(drop, i)
			return nil
		}
		v = deref(v)
		if v == nil || v.Kind != yaml.MappingNode {
			return nil
		}
		if nested := strippedNestedKeys[lower]; nested != nil {
			if err := c.stripNested(v, key, nested, &out); err != nil {
				return err
			}
		}
		if providerSections[key] {
			return c.providers(v, key, paths)
		}
		return nil
	}
	var walk func(m *yaml.Node, depth int) error
	walk = func(m *yaml.Node, depth int) error {
		if depth > 32 {
			return errTooComplex
		}
		return c.pairs(m, "", func(i int, key string, v *yaml.Node) error {
			return section(i, key, v, depth > 0)
		}, func(v *yaml.Node) error {
			return merged(v, func(sub *yaml.Node) error { return walk(sub, depth+1) })
		})
	}
	if err := walk(top, 0); err != nil {
		return sanitized{}, err
	}
	for p := range paths {
		out.ProviderPaths = append(out.ProviderPaths, p)
	}
	sort.Strings(out.ProviderPaths)
	sort.Strings(out.Removed)
	if len(out.Removed) == 0 {
		return out, nil
	}
	// Highest index first, so the others stay valid.
	sort.Sort(sort.Reverse(sort.IntSlice(drop)))
	for _, i := range drop {
		top.Content = append(top.Content[:i], top.Content[i+2:]...)
	}
	b, err := yaml.Marshal(&doc)
	if err != nil {
		return sanitized{}, fmt.Errorf("无法重新生成配置: %w", err)
	}
	out.YAML = b
	return out, nil
}

// stripNested removes keys from the section m (the value of top-level
// key name). A section that is an alias is edited where it is defined,
// which is what the core would read through the alias.
func (c *checker) stripNested(m *yaml.Node, name string, keys map[string]bool, out *sanitized) error {
	var drop []int
	err := c.pairs(m, name, func(i int, key string, _ *yaml.Node) error {
		if keys[strings.ToLower(key)] {
			drop = append(drop, i)
			out.Removed = append(out.Removed, name+"."+key)
		}
		return nil
	}, func(v *yaml.Node) error {
		return merged(v, func(sub *yaml.Node) error {
			for key := range keys {
				err := c.field(sub, name, key, 1, func(*yaml.Node, bool) error {
					return fmt.Errorf("配置通过合并键（<<）带入了 %s.%s，权限组件无法去除，请直接删除它", name, key)
				})
				if err != nil {
					return err
				}
			}
			return nil
		})
	})
	if err != nil {
		return err
	}
	for j := len(drop) - 1; j >= 0; j-- {
		i := drop[j]
		m.Content = append(m.Content[:i], m.Content[i+2:]...)
	}
	return nil
}

// providers checks the path of every provider in section m and adds the
// accepted ones to paths.
func (c *checker) providers(m *yaml.Node, section string, paths map[string]bool) error {
	var each func(m *yaml.Node, depth int) error
	each = func(m *yaml.Node, depth int) error {
		if depth > 32 {
			return errTooComplex
		}
		return c.pairs(m, section, func(_ int, name string, entry *yaml.Node) error {
			entry = deref(entry)
			if entry == nil || entry.Kind != yaml.MappingNode {
				return nil
			}
			what := section + "." + name
			return c.field(entry, what, "path", 0, func(v *yaml.Node, _ bool) error {
				v = deref(v)
				if v == nil || (v.Kind == yaml.ScalarNode && v.Tag == "!!null") {
					return nil
				}
				if v.Kind != yaml.ScalarNode {
					return fmt.Errorf("%s 的 path 不是字符串", what)
				}
				rel, err := providerPath(v.Value)
				if err != nil {
					return fmt.Errorf("%s 的 path %w: %s", what, err, v.Value)
				}
				if rel != "" {
					paths[rel] = true
				}
				return nil
			})
		}, func(v *yaml.Node) error {
			return merged(v, func(sub *yaml.Node) error { return each(sub, depth+1) })
		})
	}
	return each(m, 0)
}

// providerPath returns the cleaned form of a provider's path, "" for none.
func providerPath(p string) (string, error) {
	if p == "" {
		return "", nil
	}
	rel := filepath.Clean(filepath.FromSlash(p))
	if !filepath.IsLocal(rel) || rel == "." {
		return "", errors.New("必须是内核工作目录内的相对路径，不能是绝对路径或包含 ..")
	}
	first, _, nested := strings.Cut(rel, string(filepath.Separator))
	if reservedName(first, !nested) {
		return "", errors.New("不能使用内核自己的文件名（运行时配置、地理数据库、cache.db）或位于其下")
	}
	return rel, nil
}
