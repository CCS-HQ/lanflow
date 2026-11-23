package rpc

import (
	"context"
	"fmt"
	"log"
	"reflect"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"

	"lanflow/internal/app"
	"lanflow/internal/model"
)

// Answer is the user's reply to a consent request.
type Answer string

const (
	AnswerOnce   Answer = "once"   // allow for this connection
	AnswerAlways Answer = "always" // allow and remember (writes only)
	AnswerDeny   Answer = "deny"   // refuse (remembered for writes)
)

// Consent is what the window asks the user.
type Consent struct {
	Client string // "ctl" or "mcp:<name>"
	Agent  string // the MCP client's name; "" for ctl
	// Kind is LevelWrite (answers: once, always, deny), or LevelSensitive,
	// LevelDanger or LevelHigh (answers: once, deny). LevelHigh holds for
	// the connection, LevelDanger for the one call. A LevelHigh request of
	// Client "ctl" also takes "always" (CtlHighGrant).
	Kind   Level
	Method string
	// Summary is the call, parameters redacted, followed by a line that
	// names the peer process (PeerLine) when it is known.
	Summary string
	// PID and Path are the process that connected and its executable (0
	// and "" when unknown). Client and Agent are what the connection says
	// it is; these are what the system says. For `lanflow ctl` and
	// `lanflow mcp` they are Lanflow itself.
	PID  int
	Path string
}

// PeerLine is the line of Consent.Summary that names the peer process.
func PeerLine(pid int, path string) string {
	if pid == 0 {
		return ""
	}
	s := "pid " + strconv.Itoa(pid)
	if path != "" {
		s += " · " + path
	}
	return s
}

// PromptFunc asks the user and returns the answer; it returns an error
// when it cannot ask (no window). It may block until the user answers or
// ctx ends.
type PromptFunc func(ctx context.Context, c Consent) (Answer, error)

// consentTimeout bounds how long a call waits for the user; the call is
// refused then, whatever the prompt is doing.
var consentTimeout = time.Minute

// guard applies the permission table of docs/service-split.md §4:
//
//	level      ctl                         mcp
//	read       yes                         yes
//	sensitive  --reveal + confirmation     confirmation, per connection
//	write      at a terminal: yes          first write asks: once/always/deny
//	           otherwise: like mcp
//	  high     at a terminal: yes          confirmation, per connection,
//	           otherwise: like mcp         whatever is allowed "always"
//	danger     confirmation every time     no
//	ui         no                          no
//
// All are audited. "ctl at a terminal" is Hello.Interactive from
// Lanflow's own executable; a script or an agent that runs `lanflow ctl`
// (or speaks to the socket itself) is asked like an MCP client.
type guard struct {
	api    app.API
	prompt PromptFunc
	mu     sync.Mutex // serializes grant updates
}

func denied(format string, args ...any) error {
	return &Error{Code: CodeDenied, Message: fmt.Sprintf(format, args...)}
}

func refused(format string, args ...any) error {
	return &Error{Code: CodeConsent, Message: fmt.Sprintf(format, args...)}
}

// highImpact are the writes that replace the configuration in use or cut
// the network: an "always allow" given for switching nodes does not cover
// them (the user confirms once per connection).
var highImpact = map[string]bool{
	"ImportSubscriptionURL": true, "ImportSubscriptionFile": true, "UseSubscription": true,
	"SaveOverride": true, "SaveOverrideEntry": true, "DeleteOverrideEntry": true,
	"StartCore": true, "StopCore": true, "RestartCore": true,
	"SetSystemProxy": true, "SetTUN": true,
	"ApplyConfigFile": true,
}

// HighImpact reports whether the write method is confirmed once per
// connection whatever the agent was allowed "always".
func HighImpact(method string) bool { return highImpact[method] }

// isHighImpact is HighImpact for one call: a dry run of ApplyConfigFile
// changes nothing.
func isHighImpact(method string, args []reflect.Value) bool {
	if !highImpact[method] {
		return false
	}
	if method == "ApplyConfigFile" && len(args) == 2 && args[1].Kind() == reflect.Bool && args[1].Bool() {
		return false
	}
	return true
}

// CtlGrant is the name under which the answer for `lanflow ctl` away from
// a terminal is remembered (model.AgentGrant.Agent); no MCP client may
// call itself that (ParseIdentity).
const CtlGrant = "lanflow ctl"

// CtlHighGrant is the name under which "always" for the high-impact
// writes of `lanflow ctl` away from a terminal is remembered. Every run of
// ctl is a connection of its own, so "once per connection" would ask at
// every run of a script (a cron job, a launcher action) and refuse it when
// no window can ask; the user may allow those for good instead. MCP
// clients keep being asked per connection.
const CtlHighGrant = "lanflow ctl (high impact)"

// grantName is the name id's answer is remembered under.
func grantName(id Identity) string {
	if id.Kind == KindCtl {
		return CtlGrant
	}
	return id.Name
}

func (g *guard) check(ss *session, method string, level Level, high bool, summary string) error {
	id := ss.identity()
	if !id.Agent() {
		return nil
	}
	if err := g.isolate(ss, method, level, summary); err != nil {
		return err
	}
	switch level {
	case LevelRead:
		return nil
	case LevelUI:
		return denied("%s 只供澜流界面调用", method)
	case LevelDanger:
		if id.Kind == KindMCP {
			return denied("MCP 不开放危险操作 %s：请在澜流界面中操作", method)
		}
		a, err := g.ask(ss, Consent{Client: id.String(), Agent: id.Name, Kind: LevelDanger, Method: method, Summary: summary})
		if err != nil {
			return err
		}
		if a == AnswerDeny {
			return refused("用户拒绝了 %s", method)
		}
		return nil
	case LevelSensitive:
		return g.checkSensitive(ss, id, method, summary)
	case LevelWrite:
		ss.mu.Lock()
		interactive := ss.interactive
		ss.mu.Unlock()
		if interactive {
			return nil // a person typed the command
		}
		if high {
			return g.checkHigh(ss, id, method, summary)
		}
		return g.checkWrite(ss, id, method, summary)
	}
	return denied("%s 的权限级别未知", method)
}

// captureReads are the methods that return captured traffic.
var captureReads = map[string]bool{"CaptureFlows": true, "CaptureFlow": true}

// isolate keeps reading captured traffic and acting apart within one
// connection (docs/capture.md §10): a request body is an ideal carrier
// for prompt injection, so an agent that has read one must not be able to
// act on it through the same channel.
func (g *guard) isolate(ss *session, method string, level Level, summary string) error {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	switch {
	case captureReads[method]:
		if ss.acted {
			return denied("本连接已执行过修改操作，不能再读取抓包数据：请另开一个只读的会话")
		}
		ss.capRead = true
	case level == LevelWrite || level == LevelDanger:
		if ss.capRead {
			return denied("本连接读取过抓包数据，不能再执行 %s：请另开一个会话", method)
		}
		ss.acted = true
	}
	return nil
}

func (g *guard) checkSensitive(ss *session, id Identity, method, summary string) error {
	ss.mu.Lock()
	reveal := ss.reveal
	ss.mu.Unlock()
	if id.Kind == KindCtl && !reveal {
		return denied("%s 会返回敏感内容：需要加 --reveal，并在澜流界面中确认", method)
	}
	ss.askMu.Lock()
	defer ss.askMu.Unlock()
	switch ss.sensitive {
	case 1:
		return nil
	case -1:
		return refused("用户已拒绝本次连接读取敏感内容")
	}
	a, err := g.askLocked(ss, Consent{Client: id.String(), Agent: id.Name, Kind: LevelSensitive, Method: method, Summary: summary})
	if err != nil {
		return err
	}
	if a == AnswerDeny {
		ss.sensitive = -1
		return refused("用户拒绝了读取敏感内容（%s）", method)
	}
	ss.sensitive = 1
	return nil
}

func (g *guard) checkWrite(ss *session, id Identity, method, summary string) error {
	who := grantName(id)
	// A remembered "always" counts only for Lanflow's own executable
	// (`lanflow mcp`, `lanflow ctl`): a program that speaks to the socket
	// itself under a remembered name is asked again. A remembered refusal
	// counts for everyone.
	_, _, genuine := ss.peer()
	if allow, ok := g.grant(who); ok {
		if !allow {
			return refused("用户已拒绝「%s」修改澜流的配置；可在 设置 › 高级 › Agent 接入 撤销这个决定", who)
		}
		if genuine {
			return nil
		}
	}
	ss.askMu.Lock()
	defer ss.askMu.Unlock()
	if ss.writeOK {
		return nil
	}
	// Another connection of the same agent may have been answered meanwhile.
	if allow, ok := g.grant(who); ok {
		if !allow {
			return refused("用户已拒绝「%s」修改澜流的配置", who)
		}
		if genuine {
			return nil
		}
	}
	a, err := g.askLocked(ss, Consent{Client: id.String(), Agent: id.Name, Kind: LevelWrite, Method: method, Summary: summary})
	if err != nil {
		return err
	}
	switch a {
	case AnswerOnce:
		ss.writeOK = true
		return nil
	case AnswerAlways:
		ss.writeOK = true
		if !genuine {
			return nil // not remembered: the name is only a claim
		}
		if err := g.setGrant(who, true); err != nil {
			return err
		}
		return nil
	default:
		if err := g.setGrant(who, false); err != nil {
			return err
		}
		return refused("用户拒绝了「%s」修改澜流的配置", who)
	}
}

// checkHigh asks for a high-impact write: once per connection, and
// whatever the agent was allowed "always". A remembered refusal of its
// writes still refuses.
func (g *guard) checkHigh(ss *session, id Identity, method, summary string) error {
	who := grantName(id)
	if allow, ok := g.grant(who); ok && !allow {
		return refused("用户已拒绝「%s」修改澜流的配置；可在 设置 › 高级 › Agent 接入 撤销这个决定", who)
	}
	// Scripts that run Lanflow's own `lanflow ctl` may have been allowed
	// for good (CtlHighGrant).
	_, _, genuine := ss.peer()
	script := id.Kind == KindCtl && genuine
	if script {
		if allow, ok := g.grant(CtlHighGrant); ok && allow {
			return nil
		}
	}
	ss.askMu.Lock()
	defer ss.askMu.Unlock()
	switch ss.high {
	case 1:
		return nil
	case -1:
		return refused("用户已拒绝本次连接执行影响较大的操作")
	}
	a, err := g.askLocked(ss, Consent{Client: id.String(), Agent: id.Name, Kind: LevelHigh, Method: method, Summary: summary})
	if err != nil {
		return err
	}
	if a == AnswerDeny {
		ss.high = -1
		return refused("用户拒绝了 %s", method)
	}
	ss.high = 1
	if a == AnswerAlways && script {
		return g.setGrant(CtlHighGrant, true)
	}
	return nil
}

func (g *guard) ask(ss *session, c Consent) (Answer, error) {
	ss.askMu.Lock()
	defer ss.askMu.Unlock()
	return g.askLocked(ss, c)
}

func (g *guard) askLocked(ss *session, c Consent) (Answer, error) {
	if g.prompt == nil {
		return "", refused("需要用户授权，但澜流无法弹出授权窗口（没有界面）")
	}
	c.PID, c.Path, _ = ss.peer()
	if line := PeerLine(c.PID, c.Path); line != "" {
		if c.Summary != "" {
			c.Summary += "\n"
		}
		c.Summary += line
	}
	ctx, cancel := context.WithTimeout(ss.ctx, consentTimeout)
	defer cancel()
	type result struct {
		a   Answer
		err error
	}
	ch := make(chan result, 1)
	// The prompt runs apart: one stuck behind a dialog or a busy main
	// thread must not hold the caller past the timeout.
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("lanflow: local api: error: the consent prompt panicked: %v\n%s", r, debug.Stack())
				ch <- result{err: fmt.Errorf("%v", r)}
			}
		}()
		a, err := g.prompt(ctx, c)
		ch <- result{a, err}
	}()
	select {
	case r := <-ch:
		if r.err != nil {
			if ctx.Err() != nil {
				return "", refused("等待界面确认超时")
			}
			return "", refused("无法请求用户授权: %v", r.err)
		}
		return r.a, nil
	case <-ctx.Done():
		return "", refused("等待界面确认超时")
	}
}

// grant returns the stored answer for agent.
func (g *guard) grant(agent string) (allow, ok bool) {
	for _, gr := range g.api.Settings().AgentGrants {
		if strings.EqualFold(gr.Agent, agent) {
			return gr.Allow, true
		}
	}
	return false, false
}

func (g *guard) setGrant(agent string, allow bool) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	// Only the grants are saved: an answer given while the user changes
	// other settings must not put those back.
	base := g.api.Settings()
	next := base
	out := base.AgentGrants[:0:0]
	for _, gr := range base.AgentGrants {
		if !strings.EqualFold(gr.Agent, agent) {
			out = append(out, gr)
		}
	}
	next.AgentGrants = append(out, model.AgentGrant{Agent: agent, Allow: allow, At: time.Now()})
	if err := g.api.UpdateSettings(base, next); err != nil {
		return fmt.Errorf("无法保存授权结果: %w", err)
	}
	return nil
}
