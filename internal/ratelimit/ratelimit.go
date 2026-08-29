// Package ratelimit paces outbound HTTP requests.
//
// Every request this program makes to the teaching-management system goes
// through a Limiter. The rate is configurable, and the default is one request
// per second: the server has no documented rate limit, and the polling loop is
// the only thing deciding how hard it is hit, so anything faster is opt-in and
// flagged as such. There is no floor on the interval — a configured rate is
// used exactly as given, and zero disables pacing entirely.
package ratelimit

import (
	"context"
	"sync"
	"time"
)

const (
	// Recommended is the rate this project is meant to run at. Configuring
	// more than this is allowed but reported as above-recommended.
	Recommended = 1.0

	// DefaultRPS is used when nothing is configured.
	DefaultRPS = Recommended

	// Unlimited disables pacing when used as a rate.
	Unlimited = 0.0
)

// Limiter serialises request starts so no two of them begin closer together
// than the configured interval. It is safe for concurrent use, so a single
// Limiter shared by every job keeps the *combined* rate at the configured
// value rather than the per-job rate.
type Limiter struct {
	mu       sync.Mutex
	interval time.Duration
	next     time.Time
}

// New returns a Limiter running at rps requests per second. A rate of zero or
// less means no pacing at all.
func New(rps float64) *Limiter {
	return &Limiter{interval: IntervalFor(rps)}
}

// SetRPS changes the rate. It takes effect from the next reserved slot.
func (l *Limiter) SetRPS(rps float64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.interval = IntervalFor(rps)
}

// RPS reports the effective rate, or Unlimited when pacing is off.
func (l *Limiter) RPS() float64 {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.interval <= 0 {
		return Unlimited
	}
	return float64(time.Second) / float64(l.interval)
}

// Interval reports the current gap between request starts. Zero means no gap.
func (l *Limiter) Interval() time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.interval
}

// Wait blocks until the caller is allowed to start a request, or until ctx is
// done. A cancelled wait still consumes its slot, which errs on the side of
// sending less traffic rather than more.
func (l *Limiter) Wait(ctx context.Context) error {
	if l == nil {
		return ctx.Err()
	}

	delay := l.reserve(time.Now())
	if delay <= 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			return nil
		}
	}

	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// reserve claims the next free slot and reports how long to wait for it.
func (l *Limiter) reserve(now time.Time) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.interval <= 0 {
		return 0
	}
	if l.next.Before(now) {
		l.next = now
	}
	delay := l.next.Sub(now)
	l.next = l.next.Add(l.interval)

	return delay
}

// IntervalFor converts a rate into the gap between requests. Zero or less
// yields a zero interval, meaning unlimited.
func IntervalFor(rps float64) time.Duration {
	if rps <= 0 {
		return 0
	}
	return time.Duration(float64(time.Second) / rps)
}

// AboveRecommended reports whether rps sends requests faster than this
// project's default pace, including the unlimited setting.
func AboveRecommended(rps float64) bool {
	return rps <= 0 || rps > Recommended
}
