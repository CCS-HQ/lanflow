package agent

import (
	"errors"
	"io"
	"sync"
)

const (
	pipeInitCap   = 4 << 10
	pipeShrinkCap = 64 << 10
)

// lossyPipe carries a copy of a forwarded byte stream to a parser using a
// lazily grown ring buffer. Writes never block the forwarding: when the
// parser falls behind by more than max bytes the pipe breaks and the
// parser stops (only the record suffers, docs/capture.md §5.2).
type lossyPipe struct {
	mu     sync.Mutex
	cond   *sync.Cond
	buf    []byte
	head   int
	size   int
	max    int
	closed bool
	broken bool
}

var errPipeBroken = errors.New("capture parser fell behind")

func newLossyPipe(max int) *lossyPipe {
	p := &lossyPipe{max: max}
	p.cond = sync.NewCond(&p.mu)
	return p
}

func (p *lossyPipe) Write(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.broken || p.closed || len(b) == 0 {
		return len(b), nil
	}
	need := p.size + len(b)
	if need > p.max {
		p.broken = true
		p.buf, p.head, p.size = nil, 0, 0
		p.cond.Broadcast()
		return len(b), nil
	}
	if need > len(p.buf) {
		ncap := len(p.buf) * 2
		if ncap < pipeInitCap {
			ncap = pipeInitCap
		}
		if ncap < need {
			ncap = need
		}
		if ncap > p.max {
			ncap = p.max
		}
		nb := make([]byte, ncap)
		if p.size > 0 {
			n1 := min(p.size, len(p.buf)-p.head)
			copy(nb, p.buf[p.head:p.head+n1])
			if n1 < p.size {
				copy(nb[n1:], p.buf[:p.size-n1])
			}
		}
		p.buf, p.head = nb, 0
	}
	tail := (p.head + p.size) % len(p.buf)
	n1 := copy(p.buf[tail:], b)
	if n1 < len(b) {
		copy(p.buf, b[n1:])
	}
	p.size += len(b)
	p.cond.Broadcast()
	return len(b), nil
}

func (p *lossyPipe) Read(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for p.size == 0 && !p.closed && !p.broken {
		p.cond.Wait()
	}
	if p.broken {
		return 0, errPipeBroken
	}
	if p.size == 0 {
		p.buf, p.head = nil, 0
		return 0, io.EOF
	}
	want := min(len(b), p.size)
	n1 := min(want, len(p.buf)-p.head)
	copy(b[:n1], p.buf[p.head:p.head+n1])
	if n1 < want {
		copy(b[n1:want], p.buf[:want-n1])
	}
	p.head = (p.head + want) % len(p.buf)
	p.size -= want
	if p.size == 0 {
		p.head = 0
		if len(p.buf) > pipeShrinkCap || p.closed {
			p.buf = nil
		}
	}
	return want, nil
}

// Close ends the stream: the reader gets the rest, then EOF.
func (p *lossyPipe) Close() error {
	p.mu.Lock()
	p.closed = true
	if p.size == 0 {
		p.buf, p.head = nil, 0
	}
	p.cond.Broadcast()
	p.mu.Unlock()
	return nil
}

// Abort stops the reader at once.
func (p *lossyPipe) Abort() {
	p.mu.Lock()
	p.broken = true
	p.buf, p.head, p.size = nil, 0, 0
	p.cond.Broadcast()
	p.mu.Unlock()
}

// teeReader forwards reads of r and copies them into w (a lossyPipe).
type teeReader struct {
	r io.Reader
	w io.Writer
}

func (t teeReader) Read(b []byte) (int, error) {
	n, err := t.r.Read(b)
	if n > 0 {
		t.w.Write(b[:n])
	}
	return n, err
}
