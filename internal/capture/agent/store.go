package agent

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"lanflow/internal/capture/redact"
	"lanflow/internal/model"
)

// store is the capture ring (docs/capture.md §7.1): at most maxFlows
// records; bodies stay in memory up to memLimit, beyond which the bodies
// of the oldest finished records are sealed (AES-256-GCM, key only in
// memory) into spill files of at most spillLimit bytes together, deleted
// on exit. The spill is written in segments: records leave the ring oldest
// first, which is the order their bodies were spilled in, so a segment
// whose records are all gone is deleted and its space used again.
// Encryption and disk I/O run outside mu so parsers and detail reads never
// stall on each other.
type store struct {
	mu       sync.Mutex
	recs     []*record // oldest first
	byID     map[int64]*record
	nextID   int64
	seq      int64 // bumped on every change
	epoch    int64 // changes on clear: clients resync
	maxFlows int
	bodyCap  int64
	memLimit int64
	memUsed  int64

	// spillFloor: a scan that could not get under the target (what is left
	// belongs to unfinished records) is not repeated on every update, only
	// once memory has grown past this or a record has finished.
	spillFloor int64

	spillDir   string
	spillLimit int64
	segSize    int64
	segs       []*spillSeg // oldest first; the last one is written to
	segSeq     int
	spillUsed  int64 // bytes in all segments
	onChange   func()

	// background spill / reclaim state (guarded by mu)
	spillCond    *sync.Cond
	spillWG      sync.WaitGroup
	spillStarted bool
	spillPending bool
	spillActive  bool
	spillClosed  bool
	spillGen     uint64
	deadSegs     []*spillSeg
}

// spillSeg is one spill file. Once detached from s.segs it is closed and
// deleted when its active lock-free I/O count (io) reaches zero.
type spillSeg struct {
	f      *os.File
	path   string
	aead   cipher.AEAD
	size   int64
	live   int // records whose bodies are in it
	io     int // concurrent ReadAt/WriteAt operations outside mu
	zombie bool
}

type record struct {
	d   model.CaptureFlowDetail
	upd int64
	// evicted: out of the ring (pushed out by newer records, or cleared).
	// The connection that made the record may still hold it; what it
	// writes from then on is neither kept nor counted.
	evicted bool
	// spilled bodies
	spilled         bool
	spilling        bool // currently being encrypted/written by spillLoop
	seg             *spillSeg
	spillAt, spillN int64
	dropped         bool // bodies lost (spill file full)
}

func newStore(maxFlows int, bodyCap, memLimit int64, spillDir string, spillLimit int64) *store {
	if maxFlows <= 0 {
		maxFlows = 5000
	}
	if bodyCap <= 0 {
		bodyCap = 1 << 20
	}
	if memLimit <= 0 {
		memLimit = 64 << 20
	}
	if spillLimit <= 0 {
		spillLimit = 512 << 20
	}
	s := &store{byID: map[int64]*record{}, maxFlows: maxFlows, bodyCap: bodyCap, memLimit: memLimit,
		spillDir: spillDir, spillLimit: spillLimit, segSize: max(spillLimit/spillSegments, 1), epoch: time.Now().UnixNano()}
	s.spillCond = sync.NewCond(&s.mu)
	return s
}

// spillSegments is how many segments a full spill is made of: the space of
// the oldest sixteenth comes back as its records leave the ring.
const spillSegments = 16

// CleanSpill removes spill files left by a crashed agent.
func CleanSpill(dir string) {
	if dir == "" {
		return
	}
	ms, _ := filepath.Glob(filepath.Join(dir, "session-*.bin"))
	for _, m := range ms {
		os.Remove(m)
	}
}

// add creates a record and returns its ID.
func (s *store) add(f model.CaptureFlow) *record {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextID++
	s.seq++
	f.ID = s.nextID
	r := &record{d: model.CaptureFlowDetail{Flow: f}, upd: s.seq}
	s.recs = append(s.recs, r)
	s.byID[f.ID] = r
	for len(s.recs) > s.maxFlows {
		old := s.recs[0]
		s.recs[0] = nil
		s.recs = s.recs[1:]
		delete(s.byID, old.d.Flow.ID)
		s.evictLocked(old)
	}
	s.changed()
	return r
}

func (s *store) changed() {
	if s.onChange != nil {
		s.onChange() // must not block
	}
}

// evictLocked takes a record that left the ring out of the accounts: its
// bodies are released and the spill space it held is given back.
func (s *store) evictLocked(r *record) {
	r.evicted = true
	s.memUsed -= int64(len(r.d.ReqBody) + len(r.d.RespBody))
	r.d.ReqBody, r.d.RespBody, r.d.WSFrames = nil, nil, nil
	s.unspillLocked(r)
}

// update runs fn on the record under the lock. A record that has left the
// ring is not touched: nothing can read it any more.
func (s *store) update(r *record, fn func(d *model.CaptureFlowDetail)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.evicted {
		return
	}
	before, wasDone := int64(len(r.d.ReqBody)+len(r.d.RespBody)), r.d.Flow.Done
	fn(&r.d)
	after := int64(len(r.d.ReqBody) + len(r.d.RespBody))
	s.memUsed += after - before
	s.seq++
	r.upd = s.seq
	if r.d.Flow.Done && !wasDone {
		s.spillFloor = 0 // its bodies can be spilled now
	}
	if s.memUsed > s.memLimit && s.memUsed >= s.spillFloor {
		if s.spillDir == "" {
			s.dropLocked()
		} else if s.hasSpillableLocked() {
			s.spillFloor = 0
			s.signalSpillLocked()
		} else {
			s.spillFloor = s.memUsed + s.memLimit/16
		}
	}
}

func (s *store) hasSpillableLocked() bool {
	for _, r := range s.recs {
		if r.d.Flow.Done && !r.spilled && !r.spilling && !r.dropped && len(r.d.ReqBody)+len(r.d.RespBody) > 0 {
			return true
		}
	}
	return false
}

func (s *store) dropLocked() {
	target := s.memLimit * 3 / 4
	s.spillFloor = 0
	for _, r := range s.recs {
		if s.memUsed <= target {
			return
		}
		if !r.d.Flow.Done || r.spilled || r.spilling || r.dropped || len(r.d.ReqBody)+len(r.d.RespBody) == 0 {
			continue
		}
		s.dropRecordLocked(r)
	}
	if s.memUsed > target {
		s.spillFloor = s.memUsed + s.memLimit/16
	}
}

func (s *store) dropRecordLocked(r *record) {
	n := int64(len(r.d.ReqBody) + len(r.d.RespBody))
	r.dropped = true
	r.d.ReqBody, r.d.RespBody = nil, nil
	s.memUsed -= n
}

func (s *store) ensureSpillWorkerLocked() {
	if s.spillStarted || s.spillClosed || s.spillDir == "" {
		return
	}
	s.spillStarted = true
	s.spillWG.Add(1)
	go s.spillLoop()
}

func (s *store) signalSpillLocked() {
	if s.spillClosed || s.spillDir == "" {
		return
	}
	s.ensureSpillWorkerLocked()
	s.spillPending = true
	s.spillCond.Broadcast()
}

// appendBody adds p to a body, keeping at most bodyCap bytes and counting
// the full size.
func (s *store) appendBody(r *record, resp bool, p []byte) {
	s.update(r, func(d *model.CaptureFlowDetail) {
		body, size := &d.ReqBody, &d.ReqBodySize
		if resp {
			body, size = &d.RespBody, &d.RespBodySize
		}
		*size += int64(len(p))
		if room := s.bodyCap - int64(len(*body)); room > 0 {
			if int64(len(p)) > room {
				p = p[:room]
			}
			*body = append(*body, p...)
		}
		if *size > s.bodyCap && !d.Flow.Has(model.FlowTruncated) {
			d.Flow.Flags = append(d.Flow.Flags, model.FlowTruncated)
		}
		if resp {
			d.Flow.RespSize = *size
		} else {
			d.Flow.ReqSize = *size
		}
	})
}

func (s *store) spillLoop() {
	defer s.spillWG.Done()
	var workerAEAD cipher.AEAD
	var workerGen uint64
	for {
		s.mu.Lock()
		for !s.spillClosed && !s.spillPending && len(s.deadSegs) == 0 {
			s.spillCond.Wait()
		}
		if s.spillClosed {
			s.mu.Unlock()
			return
		}
		s.spillPending = false
		s.spillActive = true
		s.mu.Unlock()

		s.runSpillPass(&workerAEAD, &workerGen)

		s.mu.Lock()
		s.spillActive = false
		s.spillCond.Broadcast()
		s.mu.Unlock()
	}
}

func (s *store) runSpillPass(workerAEAD *cipher.AEAD, workerGen *uint64) {
	target := s.memLimit * 3 / 4
	for {
		s.mu.Lock()
		if len(s.deadSegs) > 0 {
			dead := s.deadSegs
			s.deadSegs = nil
			s.mu.Unlock()
			closeAndRemoveSegs(dead)
			continue
		}
		if s.spillClosed {
			s.mu.Unlock()
			return
		}
		var cand *record
		if s.memUsed > target {
			for _, r := range s.recs {
				if !r.d.Flow.Done || r.spilled || r.spilling || r.dropped || len(r.d.ReqBody)+len(r.d.RespBody) == 0 {
					continue
				}
				cand = r
				break
			}
			if cand == nil {
				s.spillFloor = s.memUsed + s.memLimit/16
			} else {
				s.spillFloor = 0
			}
		}
		if cand == nil {
			s.mu.Unlock()
			return
		}
		// Clip capacity to length so any concurrent append allocates a new backing array.
		req := cand.d.ReqBody[:len(cand.d.ReqBody):len(cand.d.ReqBody)]
		resp := cand.d.RespBody[:len(cand.d.RespBody):len(cand.d.RespBody)]
		cand.d.ReqBody, cand.d.RespBody = req, resp
		cand.spilling = true
		gen := s.spillGen
		s.mu.Unlock()

		if *workerAEAD == nil || *workerGen != gen {
			key := make([]byte, 32)
			if _, err := rand.Read(key); err != nil {
				s.abortSpillRecord(cand, gen)
				continue
			}
			blk, _ := aes.NewCipher(key)
			*workerAEAD, _ = cipher.NewGCM(blk)
			*workerGen = gen
		}

		plain := make([]byte, 4, 4+len(req)+len(resp))
		binary.BigEndian.PutUint32(plain, uint32(len(req)))
		plain = append(append(plain, req...), resp...)
		nonce := make([]byte, (*workerAEAD).NonceSize())
		if _, err := rand.Read(nonce); err != nil {
			s.abortSpillRecord(cand, gen)
			continue
		}
		sealed := (*workerAEAD).Seal(nonce, nonce, plain, nil)
		n := int64(len(sealed))

		s.mu.Lock()
		if s.spillClosed || s.spillGen != gen || cand.evicted {
			cand.spilling = false
			s.mu.Unlock()
			continue
		}
		if len(cand.d.ReqBody) != len(req) || len(cand.d.RespBody) != len(resp) {
			cand.spilling = false
			s.mu.Unlock()
			continue
		}
		if s.spillUsed+n > s.spillLimit {
			s.dropRecordLocked(cand)
			cand.spilling = false
			s.mu.Unlock()
			continue
		}

		var seg *spillSeg
		if len(s.segs) > 0 {
			seg = s.segs[len(s.segs)-1]
		}
		if seg != nil && (seg.size == 0 || seg.size+n <= s.segSize) {
			seg.live++
			seg.io++
			writeAt := seg.size
			seg.size += n
			s.spillUsed += n
			s.mu.Unlock()

			_, werr := seg.f.WriteAt(sealed, writeAt)

			s.mu.Lock()
			seg.io--
			var zombieSeg *spillSeg
			if seg.io == 0 && seg.zombie {
				seg.zombie = false
				zombieSeg = seg
			}
			if s.spillClosed || s.spillGen != gen {
				cand.spilling = false
				s.mu.Unlock()
				closeAndRemoveSeg(zombieSeg)
				continue
			}
			bodyChanged := len(cand.d.ReqBody) != len(req) || len(cand.d.RespBody) != len(resp)
			if werr != nil || cand.evicted || bodyChanged {
				seg.live--
				if werr != nil && !seg.zombie && slices.Contains(s.segs, seg) && seg.size == writeAt+n {
					seg.size -= n
					s.spillUsed -= n
				}
				var deadSeg *spillSeg
				if seg.live == 0 {
					if idx := slices.Index(s.segs, seg); idx >= 0 {
						s.spillUsed -= seg.size
						seg.size = 0
						s.segs = slices.Delete(s.segs, idx, idx+1)
						if seg.io == 0 {
							deadSeg = seg
						} else {
							seg.zombie = true
						}
					}
				}
				if werr != nil && !cand.evicted {
					s.dropRecordLocked(cand)
				}
				cand.spilling = false
				s.mu.Unlock()
				closeAndRemoveSeg(zombieSeg)
				closeAndRemoveSeg(deadSeg)
				continue
			}
			cand.spilled, cand.spilling, cand.seg, cand.spillAt, cand.spillN = true, false, seg, writeAt, n
			s.memUsed -= int64(len(cand.d.ReqBody) + len(cand.d.RespBody))
			cand.d.ReqBody, cand.d.RespBody = nil, nil
			s.mu.Unlock()
			closeAndRemoveSeg(zombieSeg)
			continue
		}

		// Allocate a new segment file outside mu.
		s.segSeq++
		seq := s.segSeq
		s.mu.Unlock()

		var newFile *os.File
		var newPath string
		var ferr error
		if ferr = os.MkdirAll(s.spillDir, 0o700); ferr == nil {
			newPath = filepath.Join(s.spillDir, fmt.Sprintf("session-%d-%d.bin", time.Now().UnixNano(), seq))
			newFile, ferr = os.OpenFile(newPath, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
			if ferr == nil {
				if _, ferr = newFile.WriteAt(sealed, 0); ferr != nil {
					newFile.Close()
					os.Remove(newPath)
					newFile = nil
				}
			}
		}

		s.mu.Lock()
		if ferr != nil {
			if !cand.evicted && !s.spillClosed && s.spillGen == gen {
				s.dropRecordLocked(cand)
			}
			cand.spilling = false
			s.mu.Unlock()
			continue
		}
		bodyChanged := len(cand.d.ReqBody) != len(req) || len(cand.d.RespBody) != len(resp)
		if s.spillClosed || s.spillGen != gen || cand.evicted || s.spillUsed+n > s.spillLimit || bodyChanged {
			if !cand.evicted && !s.spillClosed && s.spillGen == gen && s.spillUsed+n > s.spillLimit && !bodyChanged {
				s.dropRecordLocked(cand)
			}
			cand.spilling = false
			s.mu.Unlock()
			newFile.Close()
			os.Remove(newPath)
			continue
		}
		newSeg := &spillSeg{f: newFile, path: newPath, aead: *workerAEAD, size: n, live: 1}
		s.segs = append(s.segs, newSeg)
		s.spillUsed += n
		cand.spilled, cand.spilling, cand.seg, cand.spillAt, cand.spillN = true, false, newSeg, 0, n
		s.memUsed -= int64(len(cand.d.ReqBody) + len(cand.d.RespBody))
		cand.d.ReqBody, cand.d.RespBody = nil, nil
		s.mu.Unlock()
	}
}

func (s *store) abortSpillRecord(r *record, gen uint64) {
	s.mu.Lock()
	if !r.evicted && !s.spillClosed && s.spillGen == gen {
		s.dropRecordLocked(r)
	}
	r.spilling = false
	s.mu.Unlock()
}

// unspillLocked gives back the spill space of a record. A segment none
// of whose records is left is detached and scheduled for background
// deletion once any in-flight lock-free I/O completes.
func (s *store) unspillLocked(r *record) {
	if !r.spilled {
		return
	}
	seg := r.seg
	r.spilled, r.seg = false, nil
	if seg.live--; seg.live > 0 {
		return
	}
	s.spillUsed -= seg.size
	seg.size = 0
	if i := slices.Index(s.segs, seg); i >= 0 {
		s.segs = slices.Delete(s.segs, i, i+1)
	}
	if seg.io > 0 {
		seg.zombie = true
		return
	}
	s.deadSegs = append(s.deadSegs, seg)
	s.signalSpillLocked()
}

func closeAndRemoveSeg(seg *spillSeg) {
	if seg == nil {
		return
	}
	seg.f.Close()
	os.Remove(seg.path)
}

func closeAndRemoveSegs(segs []*spillSeg) {
	for _, seg := range segs {
		closeAndRemoveSeg(seg)
	}
}

// detail returns a copy of the record with its bodies. Spilled bodies are
// read from disk and decrypted outside mu.
func (s *store) detail(id int64) (model.CaptureFlowDetail, bool) {
	s.mu.Lock()
	r, ok := s.byID[id]
	if !ok {
		s.mu.Unlock()
		return model.CaptureFlowDetail{}, false
	}
	d := r.d
	d.Flow.Flags = append([]string(nil), r.d.Flow.Flags...)
	d.ReqHeader = append([]model.KV(nil), r.d.ReqHeader...)
	d.RespHeader = append([]model.KV(nil), r.d.RespHeader...)
	d.WSFrames = append([]model.CaptureWSFrame(nil), r.d.WSFrames...)
	d.ReqBody = append([]byte(nil), r.d.ReqBody...)
	d.RespBody = append([]byte(nil), r.d.RespBody...)
	var seg *spillSeg
	var at, n int64
	var aead cipher.AEAD
	if r.spilled && r.seg != nil {
		seg, at, n, aead = r.seg, r.spillAt, r.spillN, r.seg.aead
		seg.io++
	}
	s.mu.Unlock()

	if seg != nil {
		buf := make([]byte, n)
		if _, err := seg.f.ReadAt(buf, at); err == nil && aead != nil {
			ns := aead.NonceSize()
			if len(buf) >= ns {
				if plain, err := aead.Open(nil, buf[:ns], buf[ns:], nil); err == nil && len(plain) >= 4 {
					rn := int(binary.BigEndian.Uint32(plain))
					if 4+rn <= len(plain) {
						d.ReqBody = plain[4 : 4+rn]
						d.RespBody = plain[4+rn:]
					}
				}
			}
		}
		s.mu.Lock()
		seg.io--
		var zombieSeg *spillSeg
		if seg.io == 0 && seg.zombie {
			seg.zombie = false
			zombieSeg = seg
		}
		s.mu.Unlock()
		closeAndRemoveSeg(zombieSeg)
	}
	return d, true
}

// since returns the summaries changed after seq, the current seq, the
// oldest ID kept and the epoch.
func (s *store) since(seq, epoch int64) (flows []model.CaptureFlow, cur, minID, ep int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if epoch != s.epoch {
		seq = 0
	}
	for _, r := range s.recs {
		if r.upd > seq {
			f := r.d.Flow
			f.Flags = append([]string(nil), f.Flags...)
			flows = append(flows, f)
		}
	}
	if len(s.recs) > 0 {
		minID = s.recs[0].d.Flow.ID
	} else {
		minID = s.nextID + 1
	}
	return flows, s.seq, minID, s.epoch
}

func (s *store) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.recs)
}

const bodySearchScanCap = 2 << 20

// searchBodies scans request/response headers, decoded non-binary bodies (up
// to 2 MiB each), and WebSocket text frames across all records in the ring
// (including AES-256-GCM spilled segments).
func (s *store) searchBodies(ctx context.Context, query string) []int64 {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return nil
	}
	s.mu.Lock()
	ids := make([]int64, len(s.recs))
	for i, r := range s.recs {
		ids[i] = r.d.Flow.ID
	}
	s.mu.Unlock()

	qBytes := []byte(q)
	var hits []int64
	for _, id := range ids {
		if ctx != nil && ctx.Err() != nil {
			return hits
		}
		d, ok := s.detail(id)
		if !ok {
			continue
		}
		if detailMatchesQuery(d, q, qBytes) {
			hits = append(hits, id)
		}
	}
	return hits
}

func detailMatchesQuery(d model.CaptureFlowDetail, q string, qBytes []byte) bool {
	for _, kv := range d.ReqHeader {
		if strings.Contains(strings.ToLower(kv.Key), q) || strings.Contains(strings.ToLower(kv.Value), q) {
			return true
		}
	}
	for _, kv := range d.RespHeader {
		if strings.Contains(strings.ToLower(kv.Key), q) || strings.Contains(strings.ToLower(kv.Value), q) {
			return true
		}
	}
	if bodyContains(d.ReqBody, d.ReqHeader, "", qBytes) {
		return true
	}
	if bodyContains(d.RespBody, d.RespHeader, d.Flow.ContentType, qBytes) {
		return true
	}
	for _, f := range d.WSFrames {
		if f.Text != "" && strings.Contains(strings.ToLower(f.Text), q) {
			return true
		}
	}
	return false
}

func bodyContains(raw []byte, hs []model.KV, fallbackCT string, qBytes []byte) bool {
	if len(raw) == 0 {
		return false
	}
	b, ok := redact.Decode(raw, headerValJoin(hs, "Content-Encoding"))
	if !ok || len(b) == 0 {
		return false
	}
	ct := headerVal(hs, "Content-Type")
	if ct == "" {
		ct = fallbackCT
	}
	if kind := redact.Kind(ct, b); kind == "image" || kind == "binary" {
		return false
	}
	if len(b) > bodySearchScanCap {
		b = b[:bodySearchScanCap]
	}
	return bytes.Contains(bytes.ToLower(b), qBytes)
}

func headerVal(hs []model.KV, name string) string {
	for _, kv := range hs {
		if strings.EqualFold(kv.Key, name) {
			return kv.Value
		}
	}
	return ""
}

func headerValJoin(hs []model.KV, name string) string {
	var vals []string
	for _, kv := range hs {
		if strings.EqualFold(kv.Key, name) {
			if v := strings.TrimSpace(kv.Value); v != "" {
				vals = append(vals, v)
			}
		}
	}
	return strings.Join(vals, ", ")
}

// clear forgets everything and deletes the spill files.
func (s *store) clear() {
	s.mu.Lock()
	for _, r := range s.recs {
		r.evicted = true // connections still running hold these
		r.d.ReqBody, r.d.RespBody, r.d.WSFrames = nil, nil, nil
	}
	toClose := s.detachSpillLocked()
	s.recs, s.byID, s.memUsed, s.spillFloor = nil, map[int64]*record{}, 0, 0
	s.epoch++
	s.seq++
	s.changed()
	s.mu.Unlock()
	closeAndRemoveSegs(toClose)
	s.waitSpillIdle()
}

func (s *store) close() {
	s.mu.Lock()
	s.spillClosed = true
	toClose := s.detachSpillLocked()
	if s.spillStarted {
		s.spillCond.Broadcast()
	}
	s.mu.Unlock()
	closeAndRemoveSegs(toClose)
	s.spillWG.Wait()
	s.mu.Lock()
	rem := s.detachSpillLocked()
	s.mu.Unlock()
	closeAndRemoveSegs(rem)
}

func (s *store) detachSpillLocked() []*spillSeg {
	s.spillGen++
	var toClose []*spillSeg
	for _, seg := range s.segs {
		if seg.io > 0 {
			seg.zombie = true
		} else {
			toClose = append(toClose, seg)
		}
	}
	toClose = append(toClose, s.deadSegs...)
	s.segs, s.deadSegs, s.spillUsed = nil, nil, 0
	for _, r := range s.recs {
		if r.spilled {
			r.spilled, r.seg, r.dropped = false, nil, true
		}
	}
	return toClose
}

func (s *store) waitSpillIdle() {
	s.mu.Lock()
	for s.spillActive {
		s.spillCond.Wait()
	}
	s.mu.Unlock()
}

// waitSpill blocks until pending background spill and segment reclamation
// work has finished (tests).
func (s *store) waitSpill() {
	s.mu.Lock()
	for (s.spillPending || s.spillActive || len(s.deadSegs) > 0) && !s.spillClosed {
		s.spillCond.Wait()
	}
	s.mu.Unlock()
}

// stats returns current memory and spill usage under mu (tests).
func (s *store) stats() (memUsed, spillUsed, spillFloor int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.memUsed, s.spillUsed, s.spillFloor
}
