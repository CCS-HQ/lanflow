package rpc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"lanflow/internal/app"
	"lanflow/internal/model"
)

// Options configure a Server.
type Options struct {
	API     app.API
	Version string // Lanflow's version, sent in the handshake
	// AuditDir is where audit.log goes (Lanflow's log directory); "" keeps
	// the audit in memory only.
	AuditDir string
	// Prompt asks the user to allow an agent's write or sensitive read
	// (the window's dialog). Nil: such calls are refused with a hint.
	Prompt PromptFunc
	// Host is the serving process's side of the window ops (windowops.go):
	// the service process of the split app, or the single process. Nil:
	// the window ops are refused.
	Host Host
}

// Server serves app.API on the local interface.
type Server struct {
	o     Options
	api   reflect.Value
	guard *guard
	audit *auditLog
	undo  *undoStack

	mu    sync.Mutex
	ls    []net.Listener
	conns map[*session]struct{}
	done  bool
	ui    *session // the window process that attached (AttachUI), nil when none

	// the limits, as they were when the server was made
	handshakeTimeout time.Duration
	maxConns         int
}

// NewServer returns a server for o.API; call Serve with a listener.
func NewServer(o Options) *Server {
	s := &Server{o: o, api: reflect.ValueOf(o.API), conns: map[*session]struct{}{}, handshakeTimeout: handshakeTimeout, maxConns: maxConns}
	s.audit = openAudit(o.AuditDir)
	s.undo = &undoStack{}
	s.guard = &guard{api: o.API, prompt: o.Prompt}
	return s
}

// Start listens on the endpoint of dataDir and serves in the background.
func Start(dataDir string, o Options) (*Server, error) {
	l, err := Listen(dataDir)
	if err != nil {
		return nil, err
	}
	s := NewServer(o)
	go func() { _ = s.Serve(l) }()
	return s, nil
}

// Limits of the server (variables for tests; a server keeps the values it
// was made with). A client that stops reading
// (a suspended window, a stuck MCP host) is dropped rather than left to
// hold goroutines and memory in a service that runs for days.
var (
	handshakeTimeout = 10 * time.Second // from accept to the hello message
	writeTimeout     = 30 * time.Second // for one message to a client
	maxConns         = 64               // connections at a time
)

const (
	maxHello       = 64 << 10  // the hello message
	maxInFlight    = 128       // requests of one connection being served
	maxQueued      = 1024      // messages waiting for a client to read
	maxQueuedBytes = 256 << 20 // and their size
	flushTimeout   = 2 * time.Second
)

// Serve accepts connections on l until Close.
func (s *Server) Serve(l net.Listener) error {
	s.mu.Lock()
	if s.done {
		s.mu.Unlock()
		_ = l.Close()
		return net.ErrClosed
	}
	s.ls = append(s.ls, l)
	s.mu.Unlock()
	for {
		c, err := l.Accept()
		if err != nil {
			s.mu.Lock()
			done := s.done
			s.mu.Unlock()
			if done || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		ss := s.register(c)
		if ss == nil {
			log.Printf("lanflow: local api: refused a connection: %d are open already", s.maxConns)
			_ = c.Close()
			continue
		}
		go s.serveConn(ss)
	}
}

// register makes the session of c; nil when the server is full.
func (s *Server) register(c net.Conn) *session {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.conns) >= s.maxConns {
		return nil
	}
	ss := newSession(c)
	s.conns[ss] = struct{}{}
	return ss
}

func newSession(c net.Conn) *session {
	ctx, cancel := context.WithCancel(context.Background())
	return &session{c: c, ctx: ctx, cancel: cancel, wake: make(chan struct{}, 1), wdone: make(chan struct{}), writeTimeout: writeTimeout}
}

// Close stops listening and ends every connection.
func (s *Server) Close() error {
	s.mu.Lock()
	s.done = true
	ls := s.ls
	s.ls = nil
	conns := make([]*session, 0, len(s.conns))
	for c := range s.conns {
		conns = append(conns, c)
	}
	s.mu.Unlock()
	for _, l := range ls {
		_ = l.Close()
	}
	// What is queued (the window's quit command) goes out first.
	for _, c := range conns {
		c.finish()
	}
	deadline := time.After(flushTimeout)
	for _, c := range conns {
		select {
		case <-c.wdone:
		case <-deadline:
		}
		_ = c.c.Close()
	}
	s.audit.close()
	return nil
}

// session is one client connection.
type session struct {
	c      net.Conn
	ctx    context.Context
	cancel context.CancelFunc

	// Outgoing messages wait in queue for the writing goroutine
	// (writeLoop), so that nothing blocks on a slow client.
	qmu     sync.Mutex
	queue   []*outMsg
	qbytes  int
	topics  map[string]*outMsg // the queued "changed" of a topic
	closing bool               // close the connection once the queue is empty
	dead    bool               // the writer is gone
	wake    chan struct{}
	wdone   chan struct{} // closed when the writer ends
	// writeTimeout is how long one message may take to leave.
	writeTimeout time.Duration

	mu          sync.Mutex
	hello       bool
	id          Identity
	reveal      bool
	interactive bool            // ctl at a terminal, from Lanflow's own executable
	genuine     bool            // the peer is Lanflow's own executable (peer.go)
	pid         int             // the peer process, 0 when unknown
	exe         string          // and its executable, "" when unknown
	subs        map[string]bool // nil = not subscribed, empty = all
	off         func()

	// consent remembered for this connection (guard.go)
	askMu     sync.Mutex
	writeOK   bool
	sensitive int // 0 = not asked, 1 = allowed, -1 = refused
	high      int // high-impact writes, likewise

	// capture isolation (docs/capture.md §10): a connection that read
	// captured traffic cannot change anything, and one that changed
	// something cannot read captured traffic (guard.go)
	capRead bool
	acted   bool
}

// outMsg is one queued message.
type outMsg struct {
	b     []byte
	topic string        // of a "changed" notification, else ""
	done  chan struct{} // closed once written, when someone waits
}

func (s *Server) serveConn(ss *session) {
	c := ss.c
	go ss.writeLoop()
	defer func() {
		// An answer still queued (a refused handshake) goes out first.
		ss.finish()
		select {
		case <-ss.wdone:
		case <-time.After(flushTimeout):
		}
		ss.cancel()
		ss.mu.Lock()
		off := ss.off
		ss.off = nil
		ss.mu.Unlock()
		if off != nil {
			off()
		}
		s.mu.Lock()
		delete(s.conns, ss)
		s.mu.Unlock()
		s.detachUI(ss)
		_ = c.Close()
	}()
	// The handshake: one short line, soon. A connection that sends
	// nothing does not stay.
	br := bufio.NewReaderSize(c, maxHello)
	_ = c.SetReadDeadline(time.Now().Add(s.handshakeTimeout))
	line, err := br.ReadSlice('\n')
	if err != nil {
		return
	}
	var m message
	if err := json.Unmarshal(line, &m); err != nil || m.Method == "" {
		ss.send(message{ID: json.RawMessage("null"), Error: &Error{Code: CodeHandshake, Message: "第一条消息必须是 hello 握手"}})
		return
	}
	res, err := s.hello(ss, m.Params)
	s.reply(ss, m, res, err)
	if err != nil {
		return
	}
	_ = c.SetReadDeadline(time.Time{})

	sem := make(chan struct{}, maxInFlight)
	sc := bufio.NewScanner(br)
	sc.Buffer(make([]byte, 64<<10), maxLine)
	for sc.Scan() {
		var m message
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			ss.send(message{ID: json.RawMessage("null"), Error: &Error{Code: CodeParse, Message: "无法解析请求"}})
			continue
		}
		if m.Method == "" {
			continue // a response or garbage: clients send none
		}
		// A client with maxInFlight requests under way waits here.
		select {
		case sem <- struct{}{}:
		case <-ss.ctx.Done():
			return
		}
		go func(m message) {
			defer func() { <-sem }()
			res, err := s.safeDispatch(ss, m.Method, m.Params)
			s.reply(ss, m, res, err)
		}(m)
	}
}

// safeDispatch is dispatch with a panic of the method turned into the
// call's error: one bad call must not take the service down (and leave
// the system proxy set).
func (s *Server) safeDispatch(ss *session, method string, params json.RawMessage) (res any, err error) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("lanflow: local api: error: %s panicked: %v\n%s", method, r, debug.Stack())
			res, err = nil, &Error{Code: CodeApp, Message: fmt.Sprintf("澜流内部错误（%s）: %v", method, r)}
		}
	}()
	return s.dispatch(ss, method, params)
}

func (s *Server) reply(ss *session, m message, res any, err error) {
	if len(m.ID) == 0 {
		return // a notification
	}
	out := message{ID: m.ID}
	if err != nil {
		out.Error = toError(err)
	} else {
		b, merr := json.Marshal(res)
		if merr != nil {
			out.Error = &Error{Code: CodeApp, Message: "无法编码结果: " + merr.Error()}
		} else {
			out.Result = b
		}
	}
	ss.send(out)
}

func (ss *session) send(m message) { ss.enqueue(m, "", false) }

// enqueue queues m for the writer and returns at once. A "changed" of a
// topic replaces the one still waiting, at the end of the queue: answers
// computed before the newer change must not arrive after its only
// notification. With wait, the message's outMsg is returned to wait on.
func (ss *session) enqueue(m message, topic string, wait bool) *outMsg {
	m.JSONRPC = "2.0"
	b, err := json.Marshal(m)
	if err != nil {
		return nil
	}
	om := &outMsg{b: append(b, '\n'), topic: topic}
	if wait {
		om.done = make(chan struct{})
	}
	ss.qmu.Lock()
	if ss.dead {
		ss.qmu.Unlock()
		return nil
	}
	if topic != "" {
		if old := ss.topics[topic]; old != nil {
			for i, q := range ss.queue {
				if q == old {
					ss.queue = append(ss.queue[:i], ss.queue[i+1:]...)
					ss.qbytes -= len(old.b)
					break
				}
			}
		}
		if ss.topics == nil {
			ss.topics = map[string]*outMsg{}
		}
		ss.topics[topic] = om
	}
	ss.queue = append(ss.queue, om)
	ss.qbytes += len(om.b)
	over := len(ss.queue) > maxQueued || ss.qbytes > maxQueuedBytes
	ss.qmu.Unlock()
	if over {
		log.Printf("lanflow: local api: %s does not read its messages: connection closed", ss.label())
		_ = ss.c.Close()
		return nil
	}
	select {
	case ss.wake <- struct{}{}:
	default:
	}
	return om
}

// finish lets the writer close the connection once the queue is empty.
func (ss *session) finish() {
	ss.qmu.Lock()
	ss.closing = true
	ss.qmu.Unlock()
	select {
	case ss.wake <- struct{}{}:
	default:
	}
}

// writeLoop is the one goroutine that writes to the connection. A write
// that does not finish within writeTimeout ends the connection: the
// client is not reading.
func (ss *session) writeLoop() {
	defer close(ss.wdone)
	defer func() {
		ss.qmu.Lock()
		ss.dead = true
		ss.queue, ss.topics, ss.qbytes = nil, nil, 0
		ss.qmu.Unlock()
	}()
	for {
		ss.qmu.Lock()
		for len(ss.queue) == 0 {
			closing := ss.closing
			ss.qmu.Unlock()
			if closing {
				_ = ss.c.Close()
				return
			}
			select {
			case <-ss.wake:
			case <-ss.ctx.Done():
				return
			}
			ss.qmu.Lock()
		}
		om := ss.queue[0]
		ss.queue[0] = nil
		ss.queue = ss.queue[1:]
		ss.qbytes -= len(om.b)
		if om.topic != "" && ss.topics[om.topic] == om {
			delete(ss.topics, om.topic)
		}
		ss.qmu.Unlock()
		_ = ss.c.SetWriteDeadline(time.Now().Add(ss.writeTimeout))
		_, err := ss.c.Write(om.b)
		if om.done != nil {
			close(om.done)
		}
		if err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				log.Printf("lanflow: local api: %s did not read for %s: connection closed", ss.label(), ss.writeTimeout)
			}
			_ = ss.c.Close()
			return
		}
	}
}

// toError turns a method's error into the wire form.
func toError(err error) *Error {
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	out := &Error{Code: CodeApp, Message: err.Error()}
	if errors.Is(err, app.ErrHelperRequired) {
		out.Data = &ErrorData{Kind: "helper-required"}
	}
	return out
}

func (s *Server) hello(ss *session, params json.RawMessage) (any, error) {
	var h Hello
	if err := json.Unmarshal(params, &h); err != nil {
		return nil, &Error{Code: CodeHandshake, Message: "第一条消息必须是 hello 握手"}
	}
	if h.Protocol != ProtocolVersion {
		return nil, &Error{Code: CodeHandshake, Message: fmt.Sprintf("本机接口协议版本不一致（澜流 %s 为 %d，客户端为 %d）：请重启澜流，或使用同一版本的 lanflow 命令", s.o.Version, ProtocolVersion, h.Protocol),
			Data: &ErrorData{Kind: "protocol", PID: pid()}}
	}
	id, err := ParseIdentity(h.Client)
	if err != nil {
		return nil, &Error{Code: CodeHandshake, Message: err.Error()}
	}
	peerPID, exe, perr := peerProc(ss.c)
	genuine := perr == nil && sameExe(exe, selfExe())
	if id.Kind == KindUI && !genuine {
		// Only Lanflow's own window is the window (peer.go).
		log.Printf("lanflow: local api: refused a connection as %q from process %d (%s): not Lanflow's executable %s (%v)", KindUI, peerPID, exe, selfExe(), perr)
		// Kind "protocol" with the pid, as for another protocol version:
		// the window of another copy of Lanflow (run from a disk image
		// beside the installed one) ends this service and starts its own,
		// as it does after an update (split.Connect). Any other program
		// learns a pid it could list anyway.
		return nil, &Error{Code: CodeHandshake, Message: "只有这份澜流自己的界面可以使用 ui 身份：其他程序请用 lanflow ctl 或 lanflow mcp",
			Data: &ErrorData{Kind: "protocol", PID: pid()}}
	}
	ss.mu.Lock()
	ss.hello, ss.id, ss.reveal = true, id, h.Reveal && id.Kind == KindCtl
	ss.interactive = h.Interactive && id.Kind == KindCtl && genuine
	ss.genuine, ss.pid, ss.exe = genuine, peerPID, exe
	ss.mu.Unlock()
	if h.Version != "" && s.o.Version != "" && h.Version != s.o.Version {
		log.Printf("lanflow: local api: %s is version %s, Lanflow is %s", id, h.Version, s.o.Version)
	}
	return Welcome{Protocol: ProtocolVersion, Version: s.o.Version, Client: id.String(), PID: os.Getpid()}, nil
}

// dispatch runs one request after the handshake.
func (s *Server) dispatch(ss *session, method string, params json.RawMessage) (any, error) {
	switch method {
	case "hello":
		return nil, &Error{Code: CodeInvalidRequest, Message: "握手已完成"}
	case "subscribe":
		return nil, s.subscribe(ss, params)
	}
	if op, ok := agentOps[method]; ok {
		return s.callAgentOp(ss, method, op, params)
	}
	m, ok := apiMethod(method)
	if !ok || method == "OnChange" || method == "UpdateUIState" {
		return nil, &Error{Code: CodeNoMethod, Message: "没有这个方法: " + method}
	}
	return s.callAPI(ss, m, params)
}

// decodeArgs decodes a JSON array into the parameters of fn.
func decodeArgs(fn reflect.Type, params json.RawMessage) ([]reflect.Value, error) {
	var raw []json.RawMessage
	if len(params) > 0 && string(params) != "null" {
		if err := json.Unmarshal(params, &raw); err != nil {
			return nil, &Error{Code: CodeInvalidParams, Message: "参数应为数组"}
		}
	}
	if len(raw) != fn.NumIn() {
		return nil, &Error{Code: CodeInvalidParams, Message: fmt.Sprintf("需要 %d 个参数，收到 %d 个", fn.NumIn(), len(raw))}
	}
	args := make([]reflect.Value, len(raw))
	for i, r := range raw {
		v := reflect.New(fn.In(i))
		if err := json.Unmarshal(r, v.Interface()); err != nil {
			return nil, &Error{Code: CodeInvalidParams, Message: fmt.Sprintf("参数 %d 无效: %v", i+1, err)}
		}
		args[i] = v.Elem()
	}
	return args, nil
}

var errorType = reflect.TypeOf((*error)(nil)).Elem()

// invoke calls the API method name with args and splits the results.
func (s *Server) invoke(name string, args []reflect.Value) ([]any, error) {
	fn := s.api.MethodByName(name)
	if !fn.IsValid() {
		return nil, &Error{Code: CodeNoMethod, Message: "没有这个方法: " + name}
	}
	out := fn.Call(args)
	var err error
	if n := len(out); n > 0 && out[n-1].Type() == errorType {
		if e := out[n-1].Interface(); e != nil {
			err = e.(error)
		}
		out = out[:n-1]
	}
	res := make([]any, len(out))
	for i, v := range out {
		res[i] = v.Interface()
	}
	return res, err
}

// agentRefused lists the API methods agents reach another way: the full
// settings structs they would write back hold redacted values.
var agentRefused = map[string]string{
	"SaveSettings": "agent 不能整体保存设置（读到的设置已脱敏）：请用 PatchSettings（lanflow ctl config set）只改需要的字段",
	// UpdateSettings takes two whole settings structs: an agent would build
	// them from the redacted ones it reads, and its PatchSettings is where
	// locked fields and high-impact changes are checked.
	"UpdateSettings": "agent 不能用整份设置提交修改（读到的设置已脱敏）：请用 PatchSettings（lanflow ctl config set）只改需要的字段",
	"ApplyDNS":       "agent 不能整体替换 DNS 设置：请用 PatchSettings 修改 DNS 下的字段",
	"ApplySniffer":   "agent 不能整体替换嗅探设置：请用 PatchSettings 修改 Sniffer 下的字段",
}

func (s *Server) callAPI(ss *session, m Method, params json.RawMessage) (any, error) {
	fn := s.api.MethodByName(m.Name)
	if !fn.IsValid() {
		return nil, &Error{Code: CodeNoMethod, Message: "没有这个方法: " + m.Name}
	}
	args, err := decodeArgs(fn.Type(), params)
	if err != nil {
		return nil, err
	}
	id := ss.identity()
	if !id.Agent() {
		return s.invoke(m.Name, args)
	}
	if why, ok := agentRefused[m.Name]; ok {
		err := &Error{Code: CodeDenied, Message: why}
		s.audit.add(ss, m.Name, args, err)
		return nil, err
	}
	if id.Kind == KindMCP {
		if err := s.mcpRefuses(m.Name, args); err != nil {
			s.audit.add(ss, m.Name, args, err)
			return nil, err
		}
	}
	if err := s.guard.check(ss, m.Name, m.Level, isHighImpact(m.Name, args), summarize(m.Name, args)); err != nil {
		if m.Level != LevelRead {
			s.audit.add(ss, m.Name, args, err)
		}
		return nil, err
	}
	if m.Name == "EditSubscription" {
		args[0] = reflect.ValueOf(keepSubscriptionURL(s.o.API, args[0].Interface().(model.Subscription)))
	}
	if m.Level == LevelRead || m.Level == LevelSensitive {
		res, err := s.invoke(m.Name, args)
		if err != nil {
			return nil, err
		}
		return redactResults(m.Name, res), nil
	}
	prep := s.prepareUndo(m.Name, args)
	res, err := s.invoke(m.Name, args)
	s.audit.add(ss, m.Name, args, err)
	if err != nil {
		return nil, err
	}
	if prep != nil {
		if restore, desc := prep(res); restore != nil {
			s.undo.push(undoEntry{client: id.String(), method: m.Name, desc: desc, high: isHighImpact(m.Name, args), restore: restore})
		}
	}
	return redactResults(m.Name, res), nil
}

func (ss *session) identity() Identity {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	return ss.id
}

// label names the connection in the log.
func (ss *session) label() string {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	if !ss.hello {
		return "a client before its handshake"
	}
	return fmt.Sprintf("%s (pid %d)", ss.id, ss.pid)
}

// peer is the process at the other end (0 and "" when unknown) and
// whether it is Lanflow's own executable.
func (ss *session) peer() (pid int, exe string, genuine bool) {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	return ss.pid, ss.exe, ss.genuine
}

// mcpPathRefused are the methods with a file path that MCP does not get
// at all: the agent would choose which local file Lanflow reads.
var mcpPathRefused = map[string]string{
	"ImportSubscriptionFile": "MCP 不开放从本地文件导入订阅：请在澜流界面中导入，或使用 lanflow ctl call ImportSubscriptionFile",
}

// mcpRefuses refuses what MCP may not call although its level allows it:
// the methods that read a local file of the caller's choice.
// ApplyConfigFile stays, for the sync file itself ("") and for files
// beside it (a preview of an edited copy).
func (s *Server) mcpRefuses(name string, args []reflect.Value) error {
	if why, ok := mcpPathRefused[name]; ok {
		return &Error{Code: CodeDenied, Message: why}
	}
	if name != "ApplyConfigFile" || len(args) == 0 || args[0].Kind() != reflect.String || args[0].String() == "" {
		return nil
	}
	if !besideSyncFile(args[0].String(), s.o.API.ConfigSyncStatus().Path) {
		return denied("MCP 只能加载同步目录里的配置文件：请把文件放在 lanflow.yaml 所在的目录，或使用 lanflow ctl config sync apply")
	}
	return nil
}

// besideSyncFile reports whether path is a YAML file in the directory of
// the sync file, symbolic links resolved.
func besideSyncFile(path, syncFile string) bool {
	if syncFile == "" || !filepath.IsAbs(path) {
		return false
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".yaml", ".yml":
	default:
		return false
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return false
	}
	dir, err := filepath.EvalSymlinks(filepath.Dir(syncFile))
	if err != nil {
		return false
	}
	if runtime.GOOS == "windows" {
		return strings.EqualFold(filepath.Dir(real), dir)
	}
	return filepath.Dir(real) == dir
}

// subscribe registers the connection for "changed" notifications.
func (s *Server) subscribe(ss *session, params json.RawMessage) error {
	var p []json.RawMessage
	var topics []string
	if len(params) > 0 && string(params) != "null" {
		if err := json.Unmarshal(params, &p); err != nil || len(p) > 1 {
			return &Error{Code: CodeInvalidParams, Message: "参数应为 [主题列表]"}
		}
		if len(p) == 1 {
			if err := json.Unmarshal(p[0], &topics); err != nil {
				return &Error{Code: CodeInvalidParams, Message: "主题列表应为字符串数组"}
			}
		}
	}
	set := map[string]bool{}
	for _, t := range topics {
		set[t] = true
	}
	ss.mu.Lock()
	first := ss.subs == nil
	ss.subs = set
	ss.mu.Unlock()
	if !first {
		return nil
	}
	off := s.o.API.OnChange(func(t app.Topic) {
		ss.mu.Lock()
		want := ss.subs != nil && (len(ss.subs) == 0 || ss.subs[string(t)])
		ss.mu.Unlock()
		if !want {
			return
		}
		b, _ := json.Marshal(Changed{Topic: string(t)})
		// Queued, one per topic: a slow client must not stall the
		// service's other listeners, nor pile up notifications.
		ss.enqueue(message{Method: "changed", Params: b}, string(t), false)
	})
	ss.mu.Lock()
	if ss.ctx.Err() != nil {
		ss.mu.Unlock()
		off()
		return nil
	}
	ss.off = off
	ss.mu.Unlock()
	return nil
}

func pid() int { return os.Getpid() }
