package logbus

import (
	"testing"
	"time"
)

func TestHistoryIsBounded(t *testing.T) {
	bus := New(3)
	for i := 0; i < 5; i++ {
		bus.Publish(LevelInfo, "", "", "entry")
	}

	history := bus.History(0)
	if len(history) != 3 {
		t.Fatalf("history length = %d, want 3", len(history))
	}
	if history[0].Seq != 3 || history[2].Seq != 5 {
		t.Fatalf("history should keep the newest entries, got %d..%d", history[0].Seq, history[2].Seq)
	}
}

func TestHistoryAfterSeq(t *testing.T) {
	bus := New(10)
	for i := 0; i < 4; i++ {
		bus.Publish(LevelInfo, "", "", "entry")
	}

	if got := len(bus.History(2)); got != 2 {
		t.Fatalf("History(2) returned %d entries, want 2", got)
	}
	if got := len(bus.History(10)); got != 0 {
		t.Fatalf("History past the end returned %d entries, want 0", got)
	}
}

func TestSubscribeReceivesEntries(t *testing.T) {
	bus := New(10)
	entries, cancel := bus.Subscribe(4)
	defer cancel()

	bus.Publish(LevelSuccess, "job", "target", "选课成功")

	select {
	case entry := <-entries:
		if entry.Level != LevelSuccess || entry.Job != "job" || entry.Message != "选课成功" {
			t.Fatalf("unexpected entry %+v", entry)
		}
	case <-time.After(time.Second):
		t.Fatal("subscriber did not receive the entry")
	}
}

func TestSlowSubscriberDropsInsteadOfBlocking(t *testing.T) {
	bus := New(50)
	_, cancel := bus.Subscribe(1)
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 20; i++ {
			bus.Publish(LevelInfo, "", "", "entry")
		}
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("publishing blocked on a full subscriber channel")
	}

	if got := len(bus.History(0)); got != 20 {
		t.Fatalf("history has %d entries, want 20 — dropped entries must stay in history", got)
	}
}

func TestCancelIsIdempotent(t *testing.T) {
	bus := New(10)
	_, cancel := bus.Subscribe(1)

	cancel()
	cancel()

	bus.Publish(LevelInfo, "", "", "after cancel")
}

func TestClearKeepsSequenceMonotonic(t *testing.T) {
	bus := New(10)
	bus.Publish(LevelInfo, "", "", "one")
	bus.Clear()

	entry := bus.Publish(LevelInfo, "", "", "two")
	if entry.Seq != 2 {
		t.Fatalf("seq after clear = %d, want 2", entry.Seq)
	}
	if got := len(bus.History(0)); got != 1 {
		t.Fatalf("history after clear = %d entries, want 1", got)
	}
}

func TestLoggerScoping(t *testing.T) {
	bus := New(10)
	logger := NewLogger(bus, "job", "").With("target")
	logger.Warnf("剩余 %d 个名额", 3)

	history := bus.History(0)
	if len(history) != 1 {
		t.Fatalf("history length = %d, want 1", len(history))
	}
	entry := history[0]
	if entry.Job != "job" || entry.Target != "target" || entry.Level != LevelWarn {
		t.Fatalf("unexpected scoping %+v", entry)
	}
	if entry.Message != "剩余 3 个名额" {
		t.Fatalf("message = %q", entry.Message)
	}
}
