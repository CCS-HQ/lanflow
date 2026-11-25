package rpc

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"

	"lanflow/internal/app"
	"lanflow/internal/model"
)

// Undo (`lanflow ctl undo`): before an agent's configuration write the
// server snapshots what the write touches (settings, custom rules,
// overrides, proxy chains, a group's selection, the current subscription) and keeps a
// restore function; Undo runs the newest one. The stack lives in memory
// for the life of the app.

const undoKeep = 20

type undoEntry struct {
	at     time.Time
	client string
	method string
	desc   string
	// high: the write was a high-impact one (guard.checkHigh), and so is
	// taking it back.
	high bool
	// restore takes the write back. note, when not empty, says what it
	// left as it is and why; it is added to the description.
	restore func() (note string, err error)
}

type undoStack struct {
	mu      sync.Mutex
	entries []undoEntry
}

func (u *undoStack) push(e undoEntry) {
	e.at = time.Now()
	u.mu.Lock()
	defer u.mu.Unlock()
	u.entries = append(u.entries, e)
	if len(u.entries) > undoKeep {
		u.entries = append([]undoEntry(nil), u.entries[len(u.entries)-undoKeep:]...)
	}
}

// peek returns the newest entry without taking it.
func (u *undoStack) peek() (undoEntry, bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if len(u.entries) == 0 {
		return undoEntry{}, false
	}
	return u.entries[len(u.entries)-1], true
}

// putBack returns an entry pop took, with its time.
func (u *undoStack) putBack(e undoEntry) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.entries = append(u.entries, e)
}

func (u *undoStack) pop() (undoEntry, bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if len(u.entries) == 0 {
		return undoEntry{}, false
	}
	e := u.entries[len(u.entries)-1]
	u.entries = u.entries[:len(u.entries)-1]
	return e, true
}

// UndoResult says what Undo reverted.
type UndoResult struct {
	Method string    `json:"method"`
	Client string    `json:"client"`
	At     time.Time `json:"at"`
	Desc   string    `json:"desc"`
}

var errNothingToUndo = &Error{Code: CodeApp, Message: "没有可以撤销的配置写入（只记录本次运行中 ctl 和 MCP 的写入）"}

// undoSummary is what the consent dialog says of taking e back.
func undoSummary(e undoEntry) string {
	return truncate("Undo（"+e.method+"："+e.desc+"）", 300)
}

// runUndo takes the newest write back. An entry that cannot be restored
// goes back on the stack: the call can be made again, and the writes
// before it are not taken back out of order.
func (s *Server) runUndo(ss *session) (UndoResult, error) {
	e, ok := s.undo.pop()
	if !ok {
		return UndoResult{}, errNothingToUndo
	}
	if e.high {
		// callAgentOp asked for the entry that was newest then; another
		// may have been pushed since. Once confirmed on this connection
		// the check asks nothing more.
		if err := s.guard.check(ss, "Undo", LevelWrite, true, undoSummary(e)); err != nil {
			s.undo.putBack(e)
			return UndoResult{}, err
		}
	}
	note, err := e.restore()
	if err != nil {
		s.undo.putBack(e)
		return UndoResult{}, fmt.Errorf("撤销 %s 失败: %w", e.method, err)
	}
	return UndoResult{Method: e.method, Client: e.client, At: e.at, Desc: e.desc + note}, nil
}

// undoPrep finishes a snapshot once the write succeeded, given its
// results; nil restore means there is nothing to undo.
type undoPrep func(results []any) (restore func() (string, error), desc string)

// plain is a restore that leaves nothing out.
func plain(f func() error) func() (string, error) {
	return func() (string, error) { return "", f() }
}

// settingsWrites are the API methods that change settings, with the
// settings each one writes (nil: any). The settings are read before and
// after the call to know what it changed, and another writer may store
// something between the two reads: naming the method's own settings keeps
// that out of what undo takes back. PatchSettings is not among them: it
// knows exactly what it changed (patchSettings). The whole-settings
// methods are refused to agents and never get here.
var settingsWrites = map[string][]string{
	"SetMode": {"Mode"}, "SetMainGroup": {"MainGroup"}, "SetTUN": {"TUN"},
	"ApplyDNS": {"DNS"}, "ApplySniffer": {"Sniffer"},
	"SaveSettings": nil, "UpdateSettings": nil,
}

// onlyPaths is the filter of model.RevertSettings for paths (nil: all).
func onlyPaths(paths []string) func(string) bool {
	if paths == nil {
		return nil
	}
	return func(p string) bool { return pathIn(p, paths) }
}

var ruleWrites = map[string]bool{"SaveCustomRule": true, "DeleteCustomRule": true, "MoveCustomRule": true}

var overrideWrites = map[string]bool{
	"SaveOverride": true, "SaveOverrideEntry": true, "DeleteOverrideEntry": true,
	"MoveOverrideEntry": true, "UpdateOverrideEntry": true,
}

func argString(args []reflect.Value, i int) string {
	if i < len(args) && args[i].Kind() == reflect.String {
		return args[i].String()
	}
	return ""
}

// prepareUndo snapshots the state method is about to change.
func (s *Server) prepareUndo(method string, args []reflect.Value) undoPrep {
	api := s.o.API
	only, isSettings := settingsWrites[method]
	switch {
	case isSettings:
		old := model.CloneSettings(api.Settings())
		return func([]any) (func() (string, error), string) {
			wrote := model.CloneSettings(api.Settings())
			return func() (string, error) { return restoreSettings(api, wrote, old, onlyPaths(only)) }, "恢复之前的设置"
		}
	case ruleWrites[method]:
		old := api.CustomRules()
		return func([]any) (func() (string, error), string) {
			return plain(func() error { return restoreRules(api, old) }), "恢复之前的自定义规则"
		}
	case overrideWrites[method]:
		old := api.OverrideList()
		return func([]any) (func() (string, error), string) {
			return plain(func() error { return restoreOverrides(api, old) }), "恢复之前的覆写"
		}
	case method == "ApplyConfigFile":
		if len(args) > 1 && args[1].Kind() == reflect.Bool && args[1].Bool() {
			return nil // a dry run changes nothing
		}
		// A configuration file may change any setting, so all that
		// differs between the two reads counts as loaded from it; what
		// was changed again by the time of the undo is left (restoreSettings).
		oldSet, oldRules, oldOvs := model.CloneSettings(api.Settings()), api.CustomRules(), api.OverrideList()
		oldChains := api.ProxyChains()
		return func([]any) (func() (string, error), string) {
			wrote := model.CloneSettings(api.Settings())
			return func() (string, error) {
				if err := restoreOverrides(api, oldOvs); err != nil {
					return "", err
				}
				if _, err := api.SetProxyChains(oldChains); err != nil {
					return "", err
				}
				if err := restoreRules(api, oldRules); err != nil {
					return "", err
				}
				return restoreSettings(api, wrote, oldSet, nil)
			}, "恢复加载配置文件之前的设置、覆写、链式代理和自定义规则（订阅不恢复）"
		}
	case method == "SetProxyChains":
		// a rename rewrites custom rules too: both come back
		oldChains, oldRules := api.ProxyChains(), api.CustomRules()
		return func([]any) (func() (string, error), string) {
			return plain(func() error {
				if _, err := api.SetProxyChains(oldChains); err != nil {
					return err
				}
				return restoreRules(api, oldRules)
			}), "恢复之前的链式代理"
		}
	case method == "SetRuleDisabled":
		idx := int(args[0].Int())
		rules := api.Rules()
		if idx < 0 || idx >= len(rules) {
			return nil
		}
		prev := rules[idx].Disabled
		return func([]any) (func() (string, error), string) {
			return plain(func() error { return api.SetRuleDisabled(idx, prev) }), fmt.Sprintf("恢复第 %d 条规则的启用状态", idx+1)
		}
	case method == "SetSystemProxy":
		prev := api.Status().SystemProxy
		return func([]any) (func() (string, error), string) {
			return plain(func() error { return api.SetSystemProxy(prev) }), "恢复系统代理开关"
		}
	case method == "SelectProxy":
		group := argString(args, 0)
		prev := ""
		for _, g := range api.Proxies().Groups {
			if g.Name == group {
				prev = g.Now
			}
		}
		if prev == "" {
			return nil
		}
		return func([]any) (func() (string, error), string) {
			return plain(func() error { return api.SelectProxy(group, prev) }), fmt.Sprintf("「%s」改回「%s」", group, prev)
		}
	case method == "UseSubscription":
		prev := api.Status().CurrentSubscription
		if prev == "" {
			return nil
		}
		return func([]any) (func() (string, error), string) {
			return plain(func() error { return api.UseSubscription(prev) }), "改回之前的订阅"
		}
	case method == "EditSubscription":
		in, _ := args[0].Interface().(model.Subscription)
		var prev *model.Subscription
		for _, sub := range api.Subscriptions() {
			if sub.ID == in.ID {
				sub := sub
				prev = &sub
			}
		}
		if prev == nil {
			return nil
		}
		return func([]any) (func() (string, error), string) {
			return plain(func() error { return api.EditSubscription(*prev) }), "恢复订阅「" + prev.Name + "」的设置"
		}
	case method == "ImportSubscriptionURL" || method == "ImportSubscriptionFile":
		prevCur := api.Status().CurrentSubscription
		return func(res []any) (func() (string, error), string) {
			if len(res) == 0 {
				return nil, ""
			}
			sub, ok := res[0].(model.Subscription)
			if !ok || sub.ID == "" {
				return nil, ""
			}
			return plain(func() error {
				if api.Status().CurrentSubscription == sub.ID {
					if prevCur == "" {
						return errors.New("新导入的订阅正在使用，且之前没有订阅可以切回")
					}
					if err := api.UseSubscription(prevCur); err != nil {
						return err
					}
				}
				return api.DeleteSubscription(sub.ID)
			}), "删除刚导入的订阅「" + sub.Name + "」"
		}
	}
	return nil
}

// restoreSettings takes a settings write back: wrote is what the settings
// were right after it, old what they were before. Only the options the
// write changed (of those only names, when not nil) go back to old, and
// of them only those that still have the value the write gave them: what
// the user or another client set since stays and is named in note, as
// does what agents never change (UI state, the agent grants).
func restoreSettings(api app.API, wrote, old model.Settings, only func(string) bool) (note string, err error) {
	old.UI = wrote.UI
	old.AgentGrants = wrote.AgentGrants
	next, skipped := model.RevertSettings(api.Settings(), wrote, old, only)
	if err := api.UpdateSettings(wrote, next); err != nil {
		return "", err
	}
	if len(skipped) > 0 {
		note = "；以下设置项之后已在别处修改，未恢复：" + strings.Join(skipped, ", ")
	}
	return note, nil
}

func sameRule(a, b model.CustomRule) bool {
	return a.Type == b.Type && a.Payload == b.Payload && a.Target == b.Target
}

// restoreRules turns the custom rules back into old with the API's own
// operations: delete what is new, re-add or re-save what changed, then
// move rules into the old order.
func restoreRules(api app.API, old []model.CustomRule) error {
	keep := map[string]bool{}
	for _, r := range old {
		keep[r.ID] = true
	}
	for _, r := range api.CustomRules() {
		if !keep[r.ID] {
			if err := api.DeleteCustomRule(r.ID); err != nil {
				return err
			}
		}
	}
	cur := api.CustomRules()
	byID := map[string]model.CustomRule{}
	for _, r := range cur {
		byID[r.ID] = r
	}
	for i := len(old) - 1; i >= 0; i-- {
		r := old[i]
		c, ok := byID[r.ID]
		if ok && sameRule(c, r) {
			continue
		}
		if !ok {
			r.ID = "" // a deleted rule comes back as a new one
		}
		if _, err := api.SaveCustomRule(r); err != nil {
			return err
		}
	}
	for i, want := range old {
		cur := api.CustomRules()
		if j := indexRule(cur, want, i); j > i {
			if err := api.MoveCustomRule(cur[j].ID, i-j); err != nil {
				return err
			}
		}
	}
	return nil
}

// indexRule finds want in rules from index from on: by ID, else by content
// (a rule re-added under a new ID).
func indexRule(rules []model.CustomRule, want model.CustomRule, from int) int {
	for k := from; k < len(rules); k++ {
		if rules[k].ID == want.ID {
			return k
		}
	}
	for k := from; k < len(rules); k++ {
		if sameRule(rules[k], want) {
			return k
		}
	}
	return -1
}

// restoreOverrides turns the override list back into old.
func restoreOverrides(api app.API, old []model.Override) error {
	keep := map[string]bool{}
	for _, o := range old {
		keep[o.ID] = true
	}
	for _, o := range api.OverrideList() {
		if !keep[o.ID] {
			if err := api.DeleteOverrideEntry(o.ID); err != nil {
				return err
			}
		}
	}
	byID := map[string]model.Override{}
	for _, o := range api.OverrideList() {
		byID[o.ID] = o
	}
	ids := make([]string, len(old))
	for i, o := range old {
		c, ok := byID[o.ID]
		if ok && c.Name == o.Name && c.Enabled == o.Enabled && c.Global == o.Global && c.Source == o.Source && c.URL == o.URL && c.Text == o.Text {
			ids[i] = o.ID
			continue
		}
		if !ok {
			o.ID = "" // deleted entries come back under a new ID
		}
		saved, err := api.SaveOverrideEntry(o)
		if err != nil {
			return err
		}
		ids[i] = saved.ID
	}
	for i, id := range ids {
		cur := api.OverrideList()
		for j, o := range cur {
			if o.ID == id && j != i {
				if err := api.MoveOverrideEntry(id, i-j); err != nil {
					return err
				}
				break
			}
		}
	}
	return nil
}
