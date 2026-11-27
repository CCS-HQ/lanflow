package app

import (
	"sync"
	"time"

	"lanflow/internal/model"
)

// Geo databases are configured in runtime.yaml (geo-auto-update /
// geo-update-interval), which Mihomo only schedules with an in-memory ticker.
// If the machine was turned off or sleeping across the configured window, that
// ticker resets to zero on restart and overdue databases stay stale.
// geoAutoUpdateLoop checks last-update timestamps on startup, wake, core
// start/takeover, settings changes, and periodically so overdue updates catch
// up automatically.

var (
	geoAutoUpdateDelay = 30 * time.Second
	geoAutoUpdateTick  = 10 * time.Minute
	geoRetryMin        = 10 * time.Minute
)

const geoDefaultIntervalHours = 24

type geoUpdateState struct {
	runMu       sync.Mutex
	mu          sync.Mutex
	failures    int
	lastAttempt time.Time
	checkCh     chan struct{}
}

func (s *Service) signalGeoAutoUpdate() {
	if s.geoUpd.checkCh == nil {
		return
	}
	select {
	case s.geoUpd.checkCh <- struct{}{}:
	default:
	}
}

// lastGeoUpdate returns the most recent known geo database timestamp from
// either file modification times on disk or Settings.Geo.UpdatedAt.
func lastGeoUpdate(info model.GeoDataInfo, recorded time.Time) time.Time {
	if recorded.After(info.UpdatedAt) {
		return recorded
	}
	return info.UpdatedAt
}

func geoInterval(hours int) time.Duration {
	if hours <= 0 {
		hours = geoDefaultIntervalHours
	}
	return time.Duration(hours) * time.Hour
}

func geoDue(set model.GeoSettings, last, now time.Time) (bool, time.Duration) {
	if !set.AutoUpdate {
		return false, 0
	}
	interval := geoInterval(set.IntervalHours)
	return last.IsZero() || !now.Before(last.Add(interval)), interval
}

func geoRetryBackoff(failures int, interval time.Duration) time.Duration {
	if failures <= 0 {
		return 0
	}
	shift := failures - 1
	if shift > 6 {
		shift = 6
	}
	backoff := geoRetryMin << shift
	if interval > 0 && backoff > interval {
		return interval
	}
	return backoff
}

func (s *Service) geoAutoUpdateLoop() {
	// Wait a short settle window after startup before the first catch-up check
	// so the core and network interface settle first.
	timer := time.NewTimer(geoAutoUpdateDelay)
	select {
	case <-s.rootCtx.Done():
		timer.Stop()
		return
	case <-timer.C:
	}
	// Drain any startup signals queued during the initial settle delay.
	if s.geoUpd.checkCh != nil {
		select {
		case <-s.geoUpd.checkCh:
		default:
		}
	}
	s.maybeAutoUpdateGeo()

	t := time.NewTicker(geoAutoUpdateTick)
	defer t.Stop()
	for {
		select {
		case <-s.rootCtx.Done():
			return
		case <-t.C:
			s.maybeAutoUpdateGeo()
		case <-s.geoUpd.checkCh:
			s.maybeAutoUpdateGeo()
		}
	}
}

func (s *Service) clearGeoRetry() {
	s.geoUpd.mu.Lock()
	s.geoUpd.failures = 0
	s.geoUpd.lastAttempt = time.Time{}
	s.geoUpd.mu.Unlock()
}

// maybeAutoUpdateGeo runs an overdue Geo update when AutoUpdate is enabled,
// the core is running, and retry backoff has elapsed. It reports true when an
// update succeeded.
func (s *Service) maybeAutoUpdateGeo() bool {
	if s.rootCtx.Err() != nil || !s.running() {
		return false
	}
	set := s.Settings().Geo
	if !set.AutoUpdate {
		s.clearGeoRetry()
		return false
	}
	last := lastGeoUpdate(s.GeoDataInfo(), set.UpdatedAt)
	now := s.d.now()
	due, interval := geoDue(set, last, now)
	if !due {
		s.clearGeoRetry()
		return false
	}
	s.geoUpd.mu.Lock()
	failures, lastAttempt := s.geoUpd.failures, s.geoUpd.lastAttempt
	s.geoUpd.mu.Unlock()
	if failures > 0 && now.Sub(lastAttempt) < geoRetryBackoff(failures, interval) {
		return false
	}

	updated, err := s.updateGeoData(true)
	if err != nil {
		s.geoUpd.mu.Lock()
		s.geoUpd.failures++
		s.geoUpd.lastAttempt = s.d.now()
		n := s.geoUpd.failures
		s.geoUpd.mu.Unlock()
		if s.rootCtx.Err() == nil {
			if n == 1 {
				s.alog.Warnf("geo databases auto-update failed: %v", err)
			} else {
				s.alog.Debugf("geo databases auto-update retry #%d failed: %v", n, err)
			}
		}
		return false
	}
	if updated {
		s.alog.Infof("geo databases auto-updated")
	}
	return updated
}
