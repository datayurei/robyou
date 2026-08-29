package ratelimit

import (
	"context"
	"testing"
	"time"
)

func TestIntervalForIsConfigurable(t *testing.T) {
	tests := []struct {
		name string
		rps  float64
		want time.Duration
	}{
		{name: "default pace", rps: 1, want: time.Second},
		{name: "slower than default", rps: 0.5, want: 2 * time.Second},
		{name: "faster than default", rps: 4, want: 250 * time.Millisecond},
		{name: "zero disables pacing", rps: 0, want: 0},
		{name: "negative disables pacing", rps: -3, want: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IntervalFor(tt.rps); got != tt.want {
				t.Fatalf("IntervalFor(%v) = %v, want %v", tt.rps, got, tt.want)
			}
		})
	}
}

func TestAboveRecommended(t *testing.T) {
	if AboveRecommended(1) {
		t.Fatal("1 rps is the recommended pace and must not be flagged")
	}
	if !AboveRecommended(1.5) {
		t.Fatal("1.5 rps should be flagged as above recommended")
	}
	if !AboveRecommended(0) {
		t.Fatal("unlimited should be flagged as above recommended")
	}
}

func TestReserveSpacesRequests(t *testing.T) {
	limiter := New(2) // one request every 500ms
	start := time.Now()

	if delay := limiter.reserve(start); delay != 0 {
		t.Fatalf("first slot delay = %v, want 0", delay)
	}
	if delay := limiter.reserve(start); delay != 500*time.Millisecond {
		t.Fatalf("second slot delay = %v, want 500ms", delay)
	}
	if delay := limiter.reserve(start); delay != time.Second {
		t.Fatalf("third slot delay = %v, want 1s", delay)
	}
}

func TestSetRPSAppliesToNextSlot(t *testing.T) {
	limiter := New(1)
	now := time.Now()
	limiter.reserve(now)

	limiter.SetRPS(10)
	if delay := limiter.reserve(now.Add(time.Second)); delay != 0 {
		t.Fatalf("delay after speeding up = %v, want 0", delay)
	}
	if got := limiter.Interval(); got != 100*time.Millisecond {
		t.Fatalf("Interval() = %v, want 100ms", got)
	}
}

func TestUnlimitedDoesNotWait(t *testing.T) {
	limiter := New(Unlimited)

	start := time.Now()
	for i := 0; i < 5; i++ {
		if err := limiter.Wait(context.Background()); err != nil {
			t.Fatalf("Wait() = %v, want nil", err)
		}
	}
	if elapsed := time.Since(start); elapsed > 50*time.Millisecond {
		t.Fatalf("unlimited limiter waited %v", elapsed)
	}
	if got := limiter.RPS(); got != Unlimited {
		t.Fatalf("RPS() = %v, want %v", got, Unlimited)
	}
}

func TestWaitReturnsOnCancel(t *testing.T) {
	limiter := New(0.1) // one request every 10s
	if err := limiter.Wait(context.Background()); err != nil {
		t.Fatalf("first Wait() = %v, want nil", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := limiter.Wait(ctx); err == nil {
		t.Fatal("Wait() on a cancelled context should return an error")
	}
}
