package app

import (
	"context"
	"time"
)

// ctrlCtx returns the context of one bounded call to the controller, the
// core binary or the network: derived from parent (the caller's own loop or
// request context) or, when parent is nil, from rootCtx, so Shutdown
// cancels whatever is still waiting, and limited to d.
//
// Calls that must outlive Shutdown's cancel (stopping the core, restoring
// the system proxy) keep using context.Background().
func (s *Service) ctrlCtx(parent context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	if parent == nil {
		return s.svcCtx(d)
	}
	return context.WithTimeout(parent, d)
}

// svcCtx is ctrlCtx without a caller's context: derived from rootCtx.
func (s *Service) svcCtx(d time.Duration) (context.Context, context.CancelFunc) {
	parent := s.rootCtx
	if parent == nil { // a bare Service in a test
		parent = context.Background()
	}
	return context.WithTimeout(parent, d)
}
