package app

import (
	"context"
	"slices"
	"strings"
	"time"

	"lanflow/internal/controller"
	"lanflow/internal/model"
	"lanflow/internal/profile"
)

const (
	trafficHistory  = 60
	logRing         = 1000
	closedRing      = 500
	connInterval    = time.Second
	refreshInterval = 10 * time.Second
	// In the background (the window hidden or minimized, SetBackground)
	// nothing shows the connections, so the core is asked for them less
	// often: the statistics count deltas and lose nothing, the tray's
	// speed comes from the traffic stream; the proxies and rules are
	// re-read every refreshTicksHidden ticks of the refresh loop.
	connIntervalHidden = 3 * time.Second
	refreshTicksHidden = 6
)

// startStreams opens the controller WebSocket streams and the periodic
// refreshers for the running core. Each runs until stopStreams. Streams
// still open from an earlier start are stopped (and waited for) first.
//
// Every goroutine joins streamWG under mu while streamCancel is set, and
// stopStreams clears streamCancel under mu before it waits: no Add can
// race the Wait. The connections and log streams can be replaced on their
// own (startConnStream, restartLogStream); their contexts derive from the
// streams' context, and a replacement starts only after the stream it
// replaces has returned, so two never feed the same state at once.
func (s *Service) startStreams() {
	s.stopStreams()
	ctx, cancel := context.WithCancel(s.rootCtx)
	logCtx, logCancel := context.WithCancel(ctx)
	logsDone := make(chan struct{})
	s.mu.Lock()
	s.streamCtx, s.streamCancel = ctx, cancel
	s.logsCancel, s.logsDone = logCancel, logsDone
	level := s.state.settings.LogLevel
	s.streamWG.Add(5)
	s.mu.Unlock()

	run := func(fn func(context.Context)) {
		go func() {
			defer s.streamWG.Done()
			fn(ctx)
		}()
	}
	run(func(ctx context.Context) { _ = s.d.ctrl.StreamTraffic(ctx, s.onTraffic) })
	run(func(ctx context.Context) { _ = s.d.ctrl.StreamMemory(ctx, s.onMemory) })
	run(s.refreshLoop)
	run(s.runTailscaleLogs) // tailscale_builtin.go
	go func() {
		defer s.streamWG.Done()
		defer close(logsDone)
		s.runLogStream(logCtx, level)
	}()
	s.startConnStream()
}

// connInterval is how often the core reports the connections now.
func (s *Service) connInterval() time.Duration {
	if s.background.Load() {
		return connIntervalHidden
	}
	return connInterval
}

// startConnStream opens the connections stream at the current interval,
// replacing the previous one: the new stream starts once the old one has
// returned. Nothing happens while the streams are stopped.
func (s *Service) startConnStream() {
	s.mu.Lock()
	if s.streamCancel == nil {
		s.mu.Unlock()
		return
	}
	if s.connsCancel != nil {
		s.connsCancel()
	}
	prev := s.connsDone
	ctx, cancel := context.WithCancel(s.streamCtx)
	done := make(chan struct{})
	s.connsCancel, s.connsDone = cancel, done
	s.streamWG.Add(1)
	s.mu.Unlock()
	interval := s.connInterval()
	go func() {
		defer s.streamWG.Done()
		defer close(done)
		if prev != nil {
			<-prev
		}
		if ctx.Err() != nil {
			return
		}
		_ = s.d.ctrl.StreamConnections(ctx, interval, s.onConnections)
	}()
}

// SetBackground tells the service whether the window is out of sight
// (hidden to the tray, minimized): in the background the connections
// stream slows to connIntervalHidden and the proxies and rules refresh
// less often. The UI calls it from the window's show, hide, minimize
// and restore events; the fake backend has no such method.
func (s *Service) SetBackground(background bool) {
	if s.background.Swap(background) == background {
		return
	}
	s.startConnStream()
	if !background {
		// The routes were not rebuilt per frame while out of sight
		// (onConnections): bring them up to date for the window.
		s.goBackground(s.rebuildRoutes)
	}
}

// restartLogStream reopens the log stream at a new level, once the old
// stream has returned.
func (s *Service) restartLogStream(level string) {
	s.mu.Lock()
	if s.streamCancel == nil || s.logsCancel == nil {
		s.mu.Unlock()
		return
	}
	s.logsCancel()
	prev := s.logsDone
	logCtx, logCancel := context.WithCancel(s.streamCtx)
	done := make(chan struct{})
	s.logsCancel, s.logsDone = logCancel, done
	s.streamWG.Add(1)
	s.mu.Unlock()
	go func() {
		defer s.streamWG.Done()
		defer close(done)
		if prev != nil {
			<-prev
		}
		if logCtx.Err() != nil {
			return
		}
		s.runLogStream(logCtx, level)
	}()
}

func (s *Service) runLogStream(ctx context.Context, level string) {
	if level == "silent" {
		<-ctx.Done()
		return
	}
	if level == "" {
		level = "info"
	}
	_ = s.d.ctrl.StreamLogs(ctx, level, s.onLog)
}

// stopStreams cancels the streams and waits for them to return.
func (s *Service) stopStreams() {
	s.mu.Lock()
	cancel := s.streamCancel
	logCancel := s.logsCancel
	connCancel := s.connsCancel
	s.streamCancel, s.logsCancel, s.connsCancel = nil, nil, nil
	s.streamCtx, s.logsDone, s.connsDone = nil, nil, nil
	s.mu.Unlock()
	if logCancel != nil {
		logCancel()
	}
	if connCancel != nil {
		connCancel()
	}
	if cancel != nil {
		cancel()
	}
	s.streamWG.Wait()
}

// refreshLoop refreshes the proxies every 10 s while running, every minute
// in the background, within the streams' context. The rules are fetched
// after config changes and when read (Rules), not here.
func (s *Service) refreshLoop(ctx context.Context) {
	t := time.NewTicker(refreshInterval)
	defer t.Stop()
	ticks := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			ticks++
			if s.background.Load() && ticks%refreshTicksHidden != 0 {
				continue
			}
			s.refreshProxiesCtx(ctx)
		}
	}
}

// ---- frame handlers ----

func (s *Service) onTraffic(t controller.Traffic) {
	s.mu.Lock()
	up, down := max(0, t.Up-s.state.innerUp), max(0, t.Down-s.state.innerDown) // chains count once (onConnections)
	s.state.up = appendRing(s.state.up, up, trafficHistory)
	s.state.down = appendRing(s.state.down, down, trafficHistory)
	s.state.st.Up, s.state.st.Down = up, down
	s.mu.Unlock()
	s.em.emit(TopicTraffic, TopicStatus)
}

func appendRing(h []int64, v int64, n int) []int64 {
	h = append(h, v)
	if len(h) > n {
		h = h[len(h)-n:]
	}
	return h
}

func (s *Service) onMemory(m controller.Memory) {
	s.mu.Lock()
	changed := s.state.st.Memory != m.InUse
	s.state.st.Memory = m.InUse
	s.mu.Unlock()
	if changed {
		s.em.emit(TopicStatus)
	}
}

// logLevel returns the lower-case level of a log line without allocating
// for the levels the core sends.
func logLevel(t string) string {
	switch t {
	case "info", "warning", "error", "debug", "silent":
		return t
	case "INFO", "Info":
		return "info"
	case "WARNING", "Warning":
		return "warning"
	case "ERROR", "Error":
		return "error"
	case "DEBUG", "Debug":
		return "debug"
	}
	return strings.ToLower(t)
}

func (s *Service) onLog(l controller.Log) {
	e := model.LogEntry{Time: s.d.now(), Level: logLevel(l.Type), Payload: l.Payload}
	s.mu.Lock()
	s.state.logs.Push(e)
	s.mu.Unlock()
	s.em.emit(TopicLogs)
}

func (s *Service) onConnections(c controller.Connections) {
	now := s.d.now()
	s.mu.Lock()
	hadConns := len(s.state.conns) > 0
	active, closed := s.state.tracker.update(c, now)
	s.setConnsLocked(active)
	for i := len(closed) - 1; i >= 0; i-- { // closed is newest first
		s.state.closed.Push(closed[i])
	}
	// A dialer-proxy hop (INNER, docs/chain-proxy.md §1) or a capture
	// return leg (lanflow-mitm-up, docs/capture.md §9) is a connection of
	// its own whose bytes the core counts once more in its totals and in
	// /traffic: they are taken out, so each flow counts once.
	var inUp, inDown, inTotUp, inTotDown int64
	activeCount := 0
	for _, cn := range active {
		if cn.IsInner() || cn.CaptureReturn {
			inUp, inDown = inUp+cn.UpSpeed, inDown+cn.DownSpeed
			inTotUp, inTotDown = inTotUp+cn.Upload, inTotDown+cn.Download
		}
		if !cn.CaptureReturn {
			activeCount++
		}
	}
	for _, cn := range closed {
		if cn.IsInner() || cn.CaptureReturn {
			s.state.innerClosedUp += cn.Upload
			s.state.innerClosedDown += cn.Download
		}
	}
	s.state.innerUp, s.state.innerDown = inUp, inDown
	s.state.st.Connections = activeCount
	s.state.st.TotalUp = max(0, c.UploadTotal-inTotUp-s.state.innerClosedUp)
	s.state.st.TotalDown = max(0, c.DownloadTotal-inTotDown-s.state.innerClosedDown)
	if c.Memory > 0 {
		s.state.st.Memory = c.Memory
	}
	s.mu.Unlock()
	s.em.emit(TopicConns, TopicStatus)
	if s.stats.record(active, now) {
		s.em.emit(TopicStats)
	}
	if (hadConns || len(active) > 0) && !s.background.Load() {
		// Idle (no connections before or now): the routes only change with
		// the proxies, the mode or an exit, whose own paths rebuild them.
		// Out of sight nothing shows the routes (their speeds change with
		// nearly every frame): SetBackground rebuilds them on the way back.
		s.rebuildRoutes()
	}
}

// ClearLogs empties the log ring.
func (s *Service) ClearLogs() {
	s.mu.Lock()
	s.state.logs.Reset()
	s.mu.Unlock()
	s.em.emit(TopicLogs)
}

// ---- connection tracking ----

type connSample struct {
	up, down int64
	at       time.Time
	conn     model.Connection
}

// connTracker turns successive connection snapshots into speeds and a list
// of connections that disappeared since the previous snapshot.
type connTracker struct {
	prev          map[string]connSample
	captureChains map[string][]string // downstream conn ID → paired return chains
}

// update maps the snapshot to model connections (chains outermost first,
// speeds from the byte deltas) and returns the connections that were in
// the previous snapshot but not in this one, newest closed first.
func (t *connTracker) update(c controller.Connections, now time.Time) (active, closed []model.Connection) {
	next := make(map[string]connSample, len(c.Connections))
	active = make([]model.Connection, 0, len(c.Connections))
	for _, rc := range c.Connections {
		m := toConnection(rc)
		if p, ok := t.prev[rc.ID]; ok {
			dt := now.Sub(p.at).Seconds()
			if dt > 0 {
				m.UpSpeed = int64(float64(rc.Upload-p.up) / dt)
				m.DownSpeed = int64(float64(rc.Download-p.down) / dt)
				if m.UpSpeed < 0 {
					m.UpSpeed = 0
				}
				if m.DownSpeed < 0 {
					m.DownSpeed = 0
				}
			}
		}
		next[rc.ID] = connSample{up: rc.Upload, down: rc.Download, at: now, conn: m}
		active = append(active, m)
	}
	for id, p := range t.prev {
		if _, ok := next[id]; ok {
			continue
		}
		cc := p.conn
		cc.UpSpeed, cc.DownSpeed = 0, 0
		cc.ClosedAt = now
		closed = append(closed, cc)
	}
	t.prev = next
	t.pairCaptureReturn(active, closed)
	return active, closed
}

// pairCaptureReturn matches capture return legs (lanflow-mitm-up) 1-to-1
// FIFO with their client-to-agent legs and attaches the real outbound
// chains to the client legs (docs/capture.md §9).
func (t *connTracker) pairCaptureReturn(active, closed []model.Connection) {
	if t.captureChains == nil {
		t.captureChains = map[string][]string{}
	}
	var downs, rets []int
	for i, cn := range active {
		if !cn.CaptureLeg {
			continue
		}
		if cn.CaptureReturn {
			rets = append(rets, i)
		} else {
			downs = append(downs, i)
		}
	}
	byStart := func(a, b int) int {
		if d := active[a].Start.Compare(active[b].Start); d != 0 {
			return d
		}
		return strings.Compare(active[a].ID, active[b].ID)
	}
	slices.SortFunc(downs, byStart)
	slices.SortFunc(rets, byStart)

	used := make([]bool, len(rets))
	for _, di := range downs {
		d := &active[di]
		matched := false
		for rpos, ri := range rets {
			if used[rpos] {
				continue
			}
			r := active[ri]
			if !captureLegMatch(*d, r) {
				continue
			}
			used[rpos] = true
			matched = true
			d.CaptureReturnChains = slices.Clone(r.Chains)
			if d.DstIP == "" && r.DstIP != "" {
				d.DstIP = r.DstIP
			}
			if d.RemoteDestination == "" && r.RemoteDestination != "" {
				d.RemoteDestination = r.RemoteDestination
			}
			t.captureChains[d.ID] = slices.Clone(r.Chains)
			break
		}
		if !matched {
			if prev, ok := t.captureChains[d.ID]; ok {
				d.CaptureReturnChains = slices.Clone(prev)
			}
		}
	}
	for i := range closed {
		cc := &closed[i]
		if cc.CaptureLeg && !cc.CaptureReturn {
			if prev, ok := t.captureChains[cc.ID]; ok {
				cc.CaptureReturnChains = slices.Clone(prev)
			}
		}
		delete(t.captureChains, cc.ID)
	}
	for id := range t.captureChains {
		if _, ok := t.prev[id]; !ok {
			delete(t.captureChains, id)
		}
	}
}

func captureLegMatch(down, ret model.Connection) bool {
	if !down.Start.IsZero() && !ret.Start.IsZero() {
		if ret.Start.Before(down.Start.Add(-2*time.Second)) || ret.Start.After(down.Start.Add(5*time.Second)) {
			return false
		}
	}
	if strings.EqualFold(down.Host, ret.Host) {
		return true
	}
	dh, dp := splitHostPortStr(down.Host)
	rh, rp := splitHostPortStr(ret.Host)
	if dp != "" && dp != rp {
		return false
	}
	if ret.DstIP != "" && (down.DstIP == ret.DstIP || strings.EqualFold(dh, ret.DstIP)) {
		return true
	}
	return dh != "" && strings.EqualFold(dh, rh)
}

func splitHostPortStr(hp string) (string, string) {
	if strings.HasPrefix(hp, "[") {
		if end := strings.Index(hp, "]"); end >= 0 {
			port := ""
			if rest := hp[end+1:]; strings.HasPrefix(rest, ":") {
				port = rest[1:]
			}
			return hp[1:end], port
		}
	}
	if i := strings.LastIndex(hp, ":"); i > 0 && strings.Count(hp, ":") == 1 {
		return hp[:i], hp[i+1:]
	}
	return hp, ""
}

// toConnection maps a controller connection to the model (chains reversed).
func toConnection(rc controller.Connection) model.Connection {
	md := rc.Metadata
	host := md.Host
	if host == "" {
		host = md.SniffHost
	}
	if host == "" {
		host = md.DestinationIP
	}
	if md.DestinationPort != "" {
		host = joinHostPort(host, md.DestinationPort)
	}
	chains := make([]string, len(rc.Chains))
	ret := md.InboundName == profile.CaptureUpListener
	leg := ret
	for i, c := range rc.Chains {
		chains[len(rc.Chains)-1-i] = c
		leg = leg || strings.HasPrefix(c, profile.CaptureOutboundPrefix)
	}
	return model.Connection{
		ID:                rc.ID,
		Host:              host,
		DstIP:             md.DestinationIP,
		Network:           strings.ToLower(md.Network),
		Type:              md.Type,
		Process:           md.Process,
		ProcessPath:       md.ProcessPath,
		Rule:              rc.Rule,
		RulePayload:       rc.RulePayload,
		Chains:            chains,
		SourceIP:          md.SourceIP,
		SourcePort:        md.SourcePort,
		RemoteDestination: md.RemoteDestination,
		Upload:            rc.Upload,
		Download:          rc.Download,
		Start:             rc.Start,
		CaptureLeg:        leg,
		CaptureReturn:     ret,
	}
}

func joinHostPort(host, port string) string {
	if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
		return "[" + host + "]:" + port
	}
	return host + ":" + port
}

// CloseConnection closes one connection in the core.
func (s *Service) CloseConnection(id string) error {
	if !s.running() {
		return errCoreNotRunning
	}
	ctx, cancel := s.svcCtx(ctrlTimeout)
	defer cancel()
	if err := s.d.ctrl.CloseConnection(ctx, id); err != nil {
		return err
	}
	return nil
}

// CloseAllConnections closes every connection in the core.
func (s *Service) CloseAllConnections() error {
	if !s.running() {
		return errCoreNotRunning
	}
	ctx, cancel := s.svcCtx(ctrlTimeout)
	defer cancel()
	return s.d.ctrl.CloseAllConnections(ctx)
}
