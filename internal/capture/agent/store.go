package agent

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"lanflow/internal/model"
)

// store is the capture ring (docs/capture.md §7.1): at most maxFlows
// records; bodies stay in memory up to memLimit, beyond which the bodies
// of the oldest finished records are sealed (AES-256-GCM, key only in
// memory) into spill files of at most spillLimit bytes together, deleted
// on exit. The spill is written in segments: records leave the ring oldest
// first, which is the order their bodies were spilled in, so a segment
// whose records are all gone is deleted and its space used again.
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
	aead       cipher.AEAD
	onChange   func()
}

// spillSeg is one spill file.
type spillSeg struct {
	f    *os.File
	path string
	size int64
	live int // records whose bodies are in it
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
	return &store{byID: map[int64]*record{}, maxFlows: maxFlows, bodyCap: bodyCap, memLimit: memLimit,
		spillDir: spillDir, spillLimit: spillLimit, segSize: max(spillLimit/spillSegments, 1), epoch: time.Now().UnixNano()}
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
		s.spillLocked()
	}
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

// spillLocked moves bodies of the oldest finished records to the spill
// file until memory use is under three quarters of the limit.
func (s *store) spillLocked() {
	target := s.memLimit * 3 / 4
	s.spillFloor = 0
	for _, r := range s.recs {
		if s.memUsed <= target {
			return
		}
		if !r.d.Flow.Done || r.spilled || r.dropped || len(r.d.ReqBody)+len(r.d.RespBody) == 0 {
			continue
		}
		n := int64(len(r.d.ReqBody) + len(r.d.RespBody))
		if err := s.sealLocked(r); err != nil {
			r.dropped = true // no room / no file: the bodies are lost
		}
		r.d.ReqBody, r.d.RespBody = nil, nil
		s.memUsed -= n
	}
	if s.memUsed > target {
		s.spillFloor = s.memUsed + s.memLimit/16
	}
}

func (s *store) sealLocked(r *record) error {
	if s.spillDir == "" {
		return errors.New("no spill directory")
	}
	if s.aead == nil {
		key := make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return err
		}
		blk, _ := aes.NewCipher(key)
		s.aead, _ = cipher.NewGCM(blk)
	}
	plain := make([]byte, 4, 4+len(r.d.ReqBody)+len(r.d.RespBody))
	binary.BigEndian.PutUint32(plain, uint32(len(r.d.ReqBody)))
	plain = append(append(plain, r.d.ReqBody...), r.d.RespBody...)
	nonce := make([]byte, s.aead.NonceSize())
	rand.Read(nonce)
	sealed := s.aead.Seal(nonce, nonce, plain, nil)
	n := int64(len(sealed))
	if s.spillUsed+n > s.spillLimit {
		return errors.New("spill file full")
	}
	var seg *spillSeg
	if len(s.segs) > 0 {
		seg = s.segs[len(s.segs)-1]
	}
	if seg == nil || (seg.size > 0 && seg.size+n > s.segSize) {
		if err := os.MkdirAll(s.spillDir, 0o700); err != nil {
			return err
		}
		s.segSeq++
		p := filepath.Join(s.spillDir, fmt.Sprintf("session-%d-%d.bin", time.Now().UnixNano(), s.segSeq))
		f, err := os.OpenFile(p, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return err
		}
		seg = &spillSeg{f: f, path: p}
		s.segs = append(s.segs, seg)
	}
	if _, err := seg.f.WriteAt(sealed, seg.size); err != nil {
		return err
	}
	r.spilled, r.seg, r.spillAt, r.spillN = true, seg, seg.size, n
	seg.size += n
	seg.live++
	s.spillUsed += n
	return nil
}

// unspillLocked gives back the spill space of a record. A segment none
// of whose records is left is removed (the one being written to is
// emptied instead).
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
	if i := slices.Index(s.segs, seg); i >= 0 && i < len(s.segs)-1 {
		seg.f.Close()
		os.Remove(seg.path)
		s.segs = slices.Delete(s.segs, i, i+1)
		return
	}
	seg.f.Truncate(0)
}

// detail returns a copy of the record with its bodies.
func (s *store) detail(id int64) (model.CaptureFlowDetail, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.byID[id]
	if !ok {
		return model.CaptureFlowDetail{}, false
	}
	d := r.d
	d.Flow.Flags = append([]string(nil), r.d.Flow.Flags...)
	d.ReqHeader = append([]model.KV(nil), r.d.ReqHeader...)
	d.RespHeader = append([]model.KV(nil), r.d.RespHeader...)
	d.WSFrames = append([]model.CaptureWSFrame(nil), r.d.WSFrames...)
	d.ReqBody = append([]byte(nil), r.d.ReqBody...)
	d.RespBody = append([]byte(nil), r.d.RespBody...)
	if r.spilled {
		buf := make([]byte, r.spillN)
		if _, err := r.seg.f.ReadAt(buf, r.spillAt); err == nil {
			ns := s.aead.NonceSize()
			if plain, err := s.aead.Open(nil, buf[:ns], buf[ns:], nil); err == nil && len(plain) >= 4 {
				n := int(binary.BigEndian.Uint32(plain))
				if 4+n <= len(plain) {
					d.ReqBody = plain[4 : 4+n]
					d.RespBody = plain[4+n:]
				}
			}
		}
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

// clear forgets everything and deletes the spill file.
func (s *store) clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.recs {
		r.evicted = true // connections still running hold these
		r.d.ReqBody, r.d.RespBody, r.d.WSFrames = nil, nil, nil
	}
	s.closeSpillLocked()
	s.recs, s.byID, s.memUsed, s.spillFloor = nil, map[int64]*record{}, 0, 0
	s.epoch++
	s.seq++
	s.changed()
}

func (s *store) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closeSpillLocked()
}

func (s *store) closeSpillLocked() {
	for _, seg := range s.segs {
		seg.f.Close()
		os.Remove(seg.path)
	}
	s.segs, s.spillUsed, s.aead = nil, 0, nil
	for _, r := range s.recs {
		if r.spilled {
			r.spilled, r.seg, r.dropped = false, nil, true
		}
	}
}
