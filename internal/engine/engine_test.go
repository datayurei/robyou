package engine

import (
	"context"
	"testing"
	"time"

	"github.com/datayurei/robyou/enrollment"
	"github.com/datayurei/robyou/internal/config"
	"github.com/datayurei/robyou/internal/logbus"
)

func TestIsFiltered(t *testing.T) {
	course := enrollment.Course{Name: "音乐鉴赏", Teacher: "张三"}

	tests := []struct {
		name  string
		fuzzy []string
		exact []string
		want  bool
	}{
		{name: "no filters", want: false},
		{name: "fuzzy matches course name", fuzzy: []string{"音乐"}, want: true},
		{name: "fuzzy matches teacher", fuzzy: []string{"张"}, want: true},
		{name: "fuzzy miss", fuzzy: []string{"体育"}, want: false},
		{name: "exact needs the whole value", exact: []string{"音乐"}, want: false},
		{name: "exact matches course name", exact: []string{"音乐鉴赏"}, want: true},
		{name: "exact matches teacher", exact: []string{"张三"}, want: true},
		{name: "blank keywords are ignored", fuzzy: []string{"", "  "}, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isFiltered(course, tt.fuzzy, tt.exact); got != tt.want {
				t.Fatalf("isFiltered() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestAllDone(t *testing.T) {
	if !allDone(map[int]bool{0: true, 1: true}, 2) {
		t.Fatal("every target complete should report done")
	}
	if allDone(map[int]bool{0: true}, 2) {
		t.Fatal("a pending target should report not done")
	}
	if !allDone(map[int]bool{}, 0) {
		t.Fatal("a job with no targets is done")
	}
}

func TestInitialJobStatusesMarksDisabled(t *testing.T) {
	cfg := config.Config{Jobs: []config.Job{
		{
			ID:      "job-1",
			Name:    "enabled",
			Enabled: true,
			Targets: []config.Target{
				{Name: "on", Keyword: "a", Enabled: true, Type: config.TypeInPlan},
				{Name: "off", Keyword: "b", Type: config.TypePublic},
			},
		},
		{ID: "job-2", Name: "disabled"},
	}}

	statuses := initialJobStatuses(cfg)
	if statuses[0].State != StatePending {
		t.Fatalf("enabled job state = %q, want %q", statuses[0].State, StatePending)
	}
	if statuses[0].Targets[1].State != StateSkipped {
		t.Fatalf("disabled target state = %q, want %q", statuses[0].Targets[1].State, StateSkipped)
	}
	if statuses[1].State != StateSkipped {
		t.Fatalf("disabled job state = %q, want %q", statuses[1].State, StateSkipped)
	}
}

func TestStatusSnapshotIsIsolated(t *testing.T) {
	store := newStatusStore()
	store.setJobs([]JobStatus{{ID: "job-1", Name: "a", Targets: []TargetStatus{{Name: "t"}}}})

	snapshot := store.Snapshot()
	snapshot.Jobs[0].Name = "mutated"
	snapshot.Jobs[0].Targets[0].Name = "mutated"

	fresh := store.Snapshot()
	if fresh.Jobs[0].Name != "a" || fresh.Jobs[0].Targets[0].Name != "t" {
		t.Fatal("mutating a snapshot must not affect the store")
	}
}

func TestStatusStoreUpdatesByID(t *testing.T) {
	store := newStatusStore()
	store.setJobs([]JobStatus{
		{ID: "job-1", Targets: []TargetStatus{{Name: "t0"}}},
		{ID: "job-2", Targets: []TargetStatus{{Name: "t1"}}},
	})

	store.updateJob("job-2", func(status *JobStatus) { status.Round = 7 })
	store.updateTarget("job-2", 0, func(status *TargetStatus) { status.Successes = 3 })
	store.updateJob("missing", func(status *JobStatus) { status.Round = 99 })
	store.updateTarget("job-2", 5, func(status *TargetStatus) { status.Successes = 99 })

	snapshot := store.Snapshot()
	if snapshot.Jobs[1].Round != 7 {
		t.Fatalf("round = %d, want 7", snapshot.Jobs[1].Round)
	}
	if snapshot.Jobs[1].Targets[0].Successes != 3 {
		t.Fatalf("successes = %d, want 3", snapshot.Jobs[1].Targets[0].Successes)
	}
	if snapshot.Jobs[0].Round != 0 {
		t.Fatal("unrelated jobs must not be touched")
	}
}

func TestStartRequiresCredentials(t *testing.T) {
	runner := New(logbus.New(10), 1)
	cfg := config.Config{Jobs: []config.Job{
		{ID: "job-1", Name: "a", Enabled: true, Targets: []config.Target{
			{Name: "t", Keyword: "k", Enabled: true, Type: config.TypeInPlan},
		}},
	}}

	if err := runner.Start(cfg); err != ErrNotLoggedIn {
		t.Fatalf("Start() = %v, want %v", err, ErrNotLoggedIn)
	}
}

func TestStartRejectsInvalidConfig(t *testing.T) {
	runner := New(logbus.New(10), 1)

	if err := runner.Start(config.Config{}); err == nil {
		t.Fatal("Start() with no jobs should fail validation")
	}
}

func TestSetRateIsReflectedInStatus(t *testing.T) {
	runner := New(logbus.New(10), 1)

	runner.SetRate(0.25)
	status := runner.Status()
	if status.RequestsPerSecond != 0.25 {
		t.Fatalf("rate = %v, want 0.25", status.RequestsPerSecond)
	}
	if status.RequestIntervalMS != 4000 {
		t.Fatalf("interval = %dms, want 4000ms", status.RequestIntervalMS)
	}
	if status.AboveRecommendedRate {
		t.Fatal("0.25 rps is below the recommended pace")
	}

	runner.SetRate(5)
	if !runner.Status().AboveRecommendedRate {
		t.Fatal("5 rps should be flagged as above recommended")
	}
}

func TestSleepCtx(t *testing.T) {
	if !sleepCtx(context.Background(), time.Millisecond) {
		t.Fatal("a completed sleep should report true")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if sleepCtx(ctx, time.Hour) {
		t.Fatal("a cancelled sleep should report false")
	}
}

func TestLoginWatchdogStopsWithItsContext(t *testing.T) {
	runner := New(logbus.New(10), 1)
	ctx, cancel := context.WithCancel(context.Background())

	wait := runner.startLoginWatchdog(ctx, 3600)
	cancel()

	done := make(chan struct{})
	go func() {
		wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("watchdog did not exit after its context was cancelled")
	}
}
