package rpc

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"lanflow/internal/app"
	"lanflow/internal/applog"
	"lanflow/internal/model"
	"lanflow/internal/profile"
)

// Methods of the local interface that are not in app.API.
//
//   - PatchSettings(patch, dryRun): agents change settings field by field
//     (they read redacted settings, so writing a whole struct back would
//     store masked values); dryRun only returns the diff.
//   - Undo(): reverts the newest configuration write of ctl or MCP.
//   - AuditLog(n): the newest audit entries.
//   - Methods(): the manifest, for compatibility checks and tool lists.
//   - PatchUIState(u): the remote form of UpdateUIState (window only).

type agentOp struct {
	Method
	// level overrides Method.Level for given arguments (PatchSettings
	// with dryRun is a read).
	level func(args []json.RawMessage) Level
	// high reports a high-impact write (guard.checkHigh) for given
	// arguments: PatchSettings of a setting in patchHigh, Undo of a
	// high-impact write.
	high func(s *Server, args []json.RawMessage) bool
	// summary, when set, is what the consent dialog and the audit log say
	// of the call instead of its name and arguments.
	summary func(s *Server, args []json.RawMessage) string
	run     func(s *Server, ss *session, args []json.RawMessage) (any, error)
}

var agentOps = map[string]agentOp{
	"PatchSettings": {
		Method: Method{Name: "PatchSettings", Agent: true, Level: LevelWrite, Error: true,
			Params:  []Param{{Name: "patch", Type: "map[string]any"}, {Name: "dryRun", Type: "bool"}},
			Results: []string{"rpc.PatchResult"},
			Doc:     "PatchSettings merges patch (a JSON object of settings fields, nested like Settings and named as Settings returns them, e.g. {\"Mode\":\"rule\",\"Advanced\":{\"ipv6\":true}}) into the settings and applies them; a list is replaced whole; dryRun returns the diff without saving. High-impact settings (TUN, DNS, the ports, LAN access and proxy authentication, PAC, Wi-Fi subscription rules, geo database URLs) are confirmed by the user once per session; the core's source, the download mirror, backup, feedback and sync settings cannot be patched."},
		level: func(args []json.RawMessage) Level {
			var dry bool
			if len(args) > 1 && json.Unmarshal(args[1], &dry) == nil && dry {
				return LevelRead
			}
			return LevelWrite
		},
		high: func(_ *Server, args []json.RawMessage) bool { return len(args) > 0 && patchesHigh(args[0]) },
		run: func(s *Server, ss *session, args []json.RawMessage) (any, error) {
			var dry bool
			if err := json.Unmarshal(args[1], &dry); err != nil {
				return nil, &Error{Code: CodeInvalidParams, Message: "dryRun 应为 true 或 false"}
			}
			return s.patchSettings(ss, args[0], dry)
		},
	},
	"Undo": {
		Method: Method{Name: "Undo", Agent: true, Level: LevelWrite, Error: true, Results: []string{"rpc.UndoResult"},
			Doc: "Undo reverts the newest configuration write made through ctl or MCP in this run of Lanflow (settings, custom rules, overrides, proxy selection, current subscription). A setting changed again since that write is left as it is and named in the result. Reverting a high-impact write is confirmed by the user like the write itself."},
		// Taking a high-impact write back is one: undoing "TUN off" turns
		// TUN on. The newest entry decides; runUndo checks again what it
		// really takes off the stack.
		high: func(s *Server, _ []json.RawMessage) bool {
			e, ok := s.undo.peek()
			return ok && e.high
		},
		summary: func(s *Server, _ []json.RawMessage) string {
			if e, ok := s.undo.peek(); ok {
				return undoSummary(e)
			}
			return "Undo()"
		},
		run: func(s *Server, ss *session, _ []json.RawMessage) (any, error) {
			res, err := s.runUndo(ss)
			if res.Method != "" || err != errNothingToUndo {
				s.audit.addSummary(ss, "Undo", res.Method, err)
			}
			if err != nil {
				return nil, err
			}
			return []any{res}, nil
		},
	},
	"AuditLog": {
		Method: Method{Name: "AuditLog", Agent: true, Level: LevelRead,
			Params: []Param{{Name: "n", Type: "int"}}, Results: []string{"[]rpc.AuditEntry"},
			Doc: "AuditLog returns the newest n entries of the audit log (writes and refused calls of ctl and MCP), oldest first."},
		run: func(s *Server, _ *session, args []json.RawMessage) (any, error) {
			var n int
			_ = json.Unmarshal(args[0], &n)
			return []any{s.audit.recent(n)}, nil
		},
	},
	"PatchUIState": {
		Method: Method{Name: "PatchUIState", Agent: true, Level: LevelUI, Error: true,
			Params: []Param{{Name: "u", Type: "model.UIState"}},
			Doc:    "PatchUIState replaces the remembered UI state (the remote form of UpdateUIState)."},
		run: func(s *Server, _ *session, args []json.RawMessage) (any, error) {
			var u model.UIState
			if err := json.Unmarshal(args[0], &u); err != nil {
				return nil, &Error{Code: CodeInvalidParams, Message: err.Error()}
			}
			return []any{}, s.o.API.UpdateUIState(func(p *model.UIState) { *p = u })
		},
	},
}

// Methods is registered in init: its result is the manifest, which lists
// agentOps itself.
func init() {
	agentOps["Methods"] = agentOp{
		Method: Method{Name: "Methods", Agent: true, Level: LevelRead, Results: []string{"[]rpc.Method"},
			Doc: "Methods returns the manifest of the local interface: every method with its parameters and permission level."},
		run: func(*Server, *session, []json.RawMessage) (any, error) { return []any{Manifest()}, nil },
	}
}

func (s *Server) callAgentOp(ss *session, name string, op agentOp, params json.RawMessage) (any, error) {
	var args []json.RawMessage
	if len(params) > 0 && string(params) != "null" {
		if err := json.Unmarshal(params, &args); err != nil {
			return nil, &Error{Code: CodeInvalidParams, Message: "参数应为数组"}
		}
	}
	if len(args) != len(op.Params) {
		return nil, &Error{Code: CodeInvalidParams, Message: fmt.Sprintf("需要 %d 个参数，收到 %d 个", len(op.Params), len(args))}
	}
	level := op.Level
	if op.level != nil {
		level = op.level(args)
	}
	id := ss.identity()
	if id.Agent() {
		vals := make([]any, len(args))
		for i, a := range args {
			vals[i] = json.RawMessage(a)
		}
		high := level == LevelWrite && op.high != nil && op.high(s, args)
		sum := summarizeAny(name, vals)
		if op.summary != nil {
			sum = op.summary(s, args)
		}
		if err := s.guard.check(ss, name, level, high, sum); err != nil {
			if level != LevelRead {
				s.audit.addSummary(ss, name, sum, err)
			}
			return nil, err
		}
	}
	return op.run(s, ss, args)
}

// PatchResult is what PatchSettings returns.
type PatchResult struct {
	Changed []string         `json:"changed"` // top-level settings fields that change
	Diff    []model.DiffLine `json:"diff"`    // redacted settings, before → after, with context
	Applied bool             `json:"applied"`
}

// patchLocked lists the settings agents may not patch, as paths (a path
// covers what is under it; a trailing * matches by prefix):
//
//   - the window's own state and the grants;
//   - where the core's binary comes from (CorePath, CoreVersion, the
//     download mirror, the update channel and installing updates unasked);
//   - where data leaves this machine to: the backup server (WebDAV), the
//     report service (Feedback: reports go there with the user's token)
//     and the sync folder (Sync, which SetConfigSync, a dangerous
//     operation, sets after its checks).
var patchLocked = []string{
	"UI", "AgentGrants", "ConnHiddenColumns",
	"CorePath", "CoreVersion", "DownloadMirror", "CoreChannel", "CoreAutoInstall",
	"WebDAV", "Feedback", "Sync",
}

// patchHigh are the settings whose change through PatchSettings is a
// high-impact write (guard.checkHigh: confirmed once per connection,
// whatever the agent is allowed "always"), like the methods of their own
// are (SetTUN, SetSystemProxy). Without it an agent allowed "always"
// would turn TUN on by patching the setting and skip that confirmation.
//
//   - TUN and how it routes (the helper restarts the core as root);
//   - who may use the proxy: the LAN switch, its address lists and the
//     inbound authentication;
//   - the listening ports (programs and the system proxy point at them);
//   - DNS, which decides where every lookup goes;
//   - the system proxy: its form (PAC), turning it on at the next launch,
//     and the outbound interface;
//   - which subscription is in use by Wi-Fi network (UseSubscription is
//     a high-impact write) and where the geo databases the rules match
//     against are downloaded from.
//
// A new setting is a plain write until it is listed here or in
// patchLocked; TestPatchFieldClasses holds the list of plain ones, so
// adding a setting means deciding.
var patchHigh = []string{
	"TUN", "TUNStack", "TUNSetDNS", "Advanced.TUN*", "Advanced.InterfaceName",
	"AllowLAN", "Advanced.LANAllowedIPs", "Advanced.LANDisallowedIPs", "Advanced.Authentication", "Advanced.SkipAuthPrefixes",
	"MixedPort", "Advanced.HTTPPort", "Advanced.SocksPort",
	"DNS", "Advanced.DNS*",
	"AutoSystemProxy", "Advanced.PAC*",
	"SSIDRules", "SSIDDefaultSub",
	"Geo.GeoIP", "Geo.GeoSite", "Geo.MMDB", "Geo.ASN",
}

// pathIn reports whether the settings path is one of patterns or under one.
func pathIn(path string, patterns []string) bool {
	for _, p := range patterns {
		if pre, ok := strings.CutSuffix(p, "*"); ok {
			if strings.HasPrefix(path, pre) {
				return true
			}
		} else if path == p || strings.HasPrefix(path, p+".") {
			return true
		}
	}
	return false
}

func anyPathIn(paths, patterns []string) bool {
	for _, p := range paths {
		if pathIn(p, patterns) {
			return true
		}
	}
	return false
}

// patchesHigh reports whether patch names a setting of patchHigh,
// whether or not it changes its value.
func patchesHigh(patch json.RawMessage) bool {
	var touched []string
	scratch := model.Settings{}
	// A patch applyPatch will refuse may still name paths before its
	// error: asking for it does no harm.
	_ = patchInto(reflect.ValueOf(&scratch).Elem(), reflect.ValueOf(model.Settings{}), patch, "", &touched)
	return anyPathIn(touched, patchHigh)
}

func (s *Server) patchSettings(ss *session, patch json.RawMessage, dry bool) (any, error) {
	api := s.o.API
	// A copy of its own: the patch is applied to another one, and this
	// one is what the diff shows and what undo goes back to.
	cur := model.CloneSettings(api.Settings())
	next, changed, err := applyPatch(cur, patch, ss.identity().Agent())
	if err != nil {
		return nil, &Error{Code: CodeInvalidParams, Message: err.Error()}
	}
	res := PatchResult{Changed: changed, Diff: SettingsDiff(cur, next)}
	if dry || len(changed) == 0 {
		return []any{res}, nil
	}
	id := ss.identity()
	// Only what the patch changed is saved (UpdateSettings merges it into
	// the settings stored then): the user may be changing others.
	err = api.UpdateSettings(cur, next)
	s.audit.addSummary(ss, "PatchSettings", truncate("PatchSettings("+strings.Join(changed, ", ")+")", 300), err)
	if err != nil {
		return nil, err
	}
	// Undo is the patch the other way round: it takes back these options
	// and leaves what was stored since. What the service made of the patch
	// counts as written (a port that clears another one set to the same).
	wrote := app.NormalizeSettings(next)
	s.undo.push(undoEntry{client: id.String(), method: "PatchSettings", desc: "恢复之前的设置（" + strings.Join(changed, ", ") + "）",
		high:    anyPathIn(model.SettingsChanged(cur, wrote), patchHigh),
		restore: func() (string, error) { return restoreSettings(api, wrote, cur, nil) }})
	res.Applied = true
	return []any{res}, nil
}

// applyPatch merges patch into a copy of cur, which it leaves alone, and
// names the top-level settings that change. Keys are the Go field names
// or the JSON names agents read the settings with (Advanced.HTTPPort,
// Advanced.httpPort), whatever their case; unknown keys are errors. An
// object patches a section option by option; any other value replaces
// the option, a list as a whole. A masked value an agent read and sends
// back (the password of Advanced.Authentication, what applog.Redact
// hides) stands for the stored one. With agent, locked settings are
// refused.
func applyPatch(cur model.Settings, patch json.RawMessage, agent bool) (model.Settings, []string, error) {
	next := model.CloneSettings(cur)
	var touched []string
	if err := patchInto(reflect.ValueOf(&next).Elem(), reflect.ValueOf(cur), patch, "", &touched); err != nil {
		return cur, nil, err
	}
	if agent {
		for _, p := range touched {
			if pathIn(p, patchLocked) {
				return cur, nil, fmt.Errorf("设置项 %s 不能经 agent 修改", p)
			}
		}
	}
	var changed []string
	for _, p := range model.SettingsChanged(cur, next) {
		top, _, _ := strings.Cut(p, ".")
		if len(changed) == 0 || changed[len(changed)-1] != top {
			changed = append(changed, top)
		}
	}
	sort.Strings(changed)
	return next, changed, nil
}

// patchInto applies the JSON object raw to the struct dst; old is the
// same struct of the settings before the patch. touched collects the
// paths of the options the patch sets.
func patchInto(dst, old reflect.Value, raw json.RawMessage, path string, touched *[]string) error {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil || obj == nil {
		if path == "" {
			return fmt.Errorf("补丁应为 JSON 对象，例如 {\"Mode\":\"global\"}")
		}
		return fmt.Errorf("设置项 %s 应为 JSON 对象", path)
	}
	if len(obj) == 0 && path != "" {
		*touched = append(*touched, path) // names the section, sets nothing
	}
	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	seen := map[string]string{}
	for _, k := range keys {
		f, ok := fieldFold(dst.Type(), k)
		if !ok {
			return fmt.Errorf("没有设置项 %s%s", dotted(path), k)
		}
		p := dotted(path) + f.Name
		if other, dup := seen[f.Name]; dup {
			return fmt.Errorf("设置项 %s 在补丁里出现了两次（%s 和 %s）", p, other, k)
		}
		seen[f.Name] = k
		fv, ov := dst.FieldByIndex(f.Index), old.FieldByIndex(f.Index)
		if sectionType(f.Type) {
			if err := patchInto(fv, ov, obj[k], p, touched); err != nil {
				return err
			}
			continue
		}
		*touched = append(*touched, p)
		// Decoded into a value of its own: decoding into the current one
		// would write into the list the settings before the patch hold.
		nv := reflect.New(f.Type)
		dec := json.NewDecoder(bytes.NewReader(obj[k]))
		dec.DisallowUnknownFields()
		if err := dec.Decode(nv.Interface()); err != nil {
			return fmt.Errorf("设置项 %s 的值无效: %v", p, err)
		}
		if err := unmask(nv.Elem(), ov, p); err != nil {
			return err
		}
		fv.Set(nv.Elem())
	}
	return nil
}

func dotted(path string) string {
	if path == "" {
		return ""
	}
	return path + "."
}

// sectionType reports whether t is a section of the settings patched
// option by option: a struct of exported fields (model.MergeSettings
// draws the same line; a time is one value).
func sectionType(t reflect.Type) bool {
	if t.Kind() != reflect.Struct || t.NumField() == 0 {
		return false
	}
	for i := 0; i < t.NumField(); i++ {
		if !t.Field(i).IsExported() {
			return false
		}
	}
	return true
}

// maskFor is how an agent sees the string s of the option at path
// (redactSettings, then RedactValue's pass over every string).
func maskFor(path, s string) string {
	if path == "Advanced.Authentication" {
		s = maskAuth(s)
	}
	return applog.Redact(s)
}

// unmask puts the stored strings back where the new value of the option
// at path holds their masked form: an agent that reads a list, adds an
// entry and sends the list back has only the masks of the others. A mask
// that stands for nothing stored is refused where it can be told (the
// inbound authentication): it would be saved as the password.
func unmask(nv, old reflect.Value, path string) error {
	stored := map[string]bool{}
	byMask := map[string][]string{}
	walkStringValues(old, func(v reflect.Value) {
		s := v.String()
		stored[s] = true
		if m := maskFor(path, s); m != s {
			byMask[m] = append(byMask[m], s)
		}
	})
	var err error
	walkStringValues(nv, func(v reflect.Value) {
		s := v.String()
		if stored[s] {
			return
		}
		if orig := byMask[s]; len(orig) > 0 {
			v.SetString(orig[0])
			if len(orig) > 1 {
				byMask[s] = orig[1:]
			}
			return
		}
		if path == "Advanced.Authentication" && (s == "***" || strings.HasSuffix(s, ":***")) && err == nil {
			err = fmt.Errorf("设置项 %s 的条目 %s 是脱敏后的占位符，不对应任何已保存的条目：新条目请写出完整的 用户名:密码", path, s)
		}
	})
	return err
}

// walkStringValues calls f for every string in v (lists, structs and
// maps gone through); strings of a settable v can be set.
func walkStringValues(v reflect.Value, f func(reflect.Value)) {
	switch v.Kind() {
	case reflect.String:
		f(v)
	case reflect.Slice, reflect.Array:
		for i := 0; i < v.Len(); i++ {
			walkStringValues(v.Index(i), f)
		}
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			if v.Type().Field(i).IsExported() {
				walkStringValues(v.Field(i), f)
			}
		}
	case reflect.Pointer:
		if !v.IsNil() {
			walkStringValues(v.Elem(), f)
		}
	}
}

// fieldFold finds the field of struct t that name means: its Go name or
// its JSON name, whatever the case.
func fieldFold(t reflect.Type, name string) (reflect.StructField, bool) {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return reflect.StructField{}, false
	}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		tag, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if strings.EqualFold(f.Name, name) || (tag != "" && tag != "-" && strings.EqualFold(tag, name)) {
			return f, true
		}
	}
	return reflect.StructField{}, false
}

// SettingsDiff is the redacted line diff of two settings, as indented JSON
// with two lines of context around changes.
func SettingsDiff(before, after model.Settings) []model.DiffLine {
	a, _ := json.MarshalIndent(RedactValue(before), "", "  ")
	b, _ := json.MarshalIndent(RedactValue(after), "", "  ")
	return CompactDiff(profile.Diff(a, b), 2)
}

// CompactDiff keeps the changed lines and ctx lines around them; skipped
// runs become one "…" line.
func CompactDiff(lines []model.DiffLine, ctx int) []model.DiffLine {
	keep := make([]bool, len(lines))
	for i, l := range lines {
		if l.Op == '+' || l.Op == '-' {
			for j := max(0, i-ctx); j <= min(len(lines)-1, i+ctx); j++ {
				keep[j] = true
			}
		}
	}
	var out []model.DiffLine
	skipped := false
	for i, l := range lines {
		if !keep[i] {
			skipped = true
			continue
		}
		if skipped && len(out) > 0 {
			out = append(out, model.DiffLine{Op: ' ', Text: "…"})
		}
		skipped = false
		out = append(out, l)
	}
	return out
}
