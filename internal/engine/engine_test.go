package engine

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/datayurei/robyou/enrollment"
	"github.com/datayurei/robyou/internal/config"
	"github.com/datayurei/robyou/internal/logbus"
	"github.com/datayurei/robyou/internal/ratelimit"
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
	runner := New(logbus.New(10), 1, t.TempDir())
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
	runner := New(logbus.New(10), 1, t.TempDir())

	if err := runner.Start(config.Config{}); err == nil {
		t.Fatal("Start() with no jobs should fail validation")
	}
}

func TestSetRateIsReflectedInStatus(t *testing.T) {
	runner := New(logbus.New(10), 1, t.TempDir())

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
	runner := New(logbus.New(10), 1, t.TempDir())
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

func TestMarkRoundWaitHoldsJobsInWaitingState(t *testing.T) {
	runner := New(logbus.New(10), 1, t.TempDir())
	runner.status.setJobs([]JobStatus{
		{ID: "job-1", Name: "a", Enabled: true, State: StatePending},
		{ID: "job-2", Name: "b", Enabled: false, State: StateSkipped},
	})

	runner.markRoundWait(3, 30*time.Second, ErrRoundNotOpen)

	status := runner.Status()
	if status.Phase != PhaseWaiting {
		t.Fatalf("phase = %q, want %q", status.Phase, PhaseWaiting)
	}
	if !status.WaitingForRound || status.RoundAttempts != 3 {
		t.Fatalf("waiting=%v attempts=%d, want true/3", status.WaitingForRound, status.RoundAttempts)
	}
	if status.NextRoundCheckAt == nil || !status.NextRoundCheckAt.After(time.Now()) {
		t.Fatal("next check time should be in the future")
	}
	if status.Jobs[0].State != StateWaiting {
		t.Fatalf("pending job state = %q, want %q", status.Jobs[0].State, StateWaiting)
	}
	if status.Jobs[0].Message == "" {
		t.Fatal("a waiting job should explain what it is waiting for")
	}
	if status.Jobs[1].State != StateSkipped {
		t.Fatalf("disabled job state = %q, want it left alone", status.Jobs[1].State)
	}
}

func TestClearRoundWaitReleasesJobs(t *testing.T) {
	runner := New(logbus.New(10), 1, t.TempDir())
	runner.status.setJobs([]JobStatus{{ID: "job-1", Name: "a", Enabled: true, State: StatePending}})
	runner.markRoundWait(2, time.Second, ErrRoundNotOpen)

	runner.clearRoundWait("A1B2C3D4E5F60718293A4B5C6D7E8F90")

	status := runner.Status()
	if status.WaitingForRound || status.RoundAttempts != 0 || status.NextRoundCheckAt != nil {
		t.Fatalf("wait state not cleared: %+v", status)
	}
	if status.Xkid != "A1B2C3D4E5F60718293A4B5C6D7E8F90" {
		t.Fatalf("xkid = %q", status.Xkid)
	}
	if status.Phase != PhaseRunning {
		t.Fatalf("phase = %q, want %q", status.Phase, PhaseRunning)
	}
	if status.Jobs[0].State != StatePending || status.Jobs[0].Message != "" {
		t.Fatalf("job not released: %+v", status.Jobs[0])
	}
}

func TestEnsureRoundKeepsWaitingUntilCancelled(t *testing.T) {
	var checks int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&checks, 1)
		// The portal before enrollment opens: no round link at all.
		w.Write([]byte(`<html><body><a href="/jsxsd/xsxx/xsxxxx">学生信息</a></body></html>`))
	}))
	defer server.Close()

	runner := New(logbus.New(100), ratelimit.Unlimited, t.TempDir())
	runner.session.portalURL = server.URL + "/jsxsd/framework/xsrkxz.htmlx"
	runner.session.baseURL = server.URL + "/"
	runner.status.setJobs([]JobStatus{{ID: "job-1", Name: "a", Enabled: true, State: StatePending}})

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Millisecond)
	defer cancel()

	if runner.ensureRound(ctx, runner.logger("", ""), 10*time.Millisecond) {
		t.Fatal("ensureRound should report failure when it is cancelled while waiting")
	}
	if got := atomic.LoadInt32(&checks); got < 2 {
		t.Fatalf("portal was checked %d times, want repeated retries", got)
	}

	status := runner.Status()
	if !status.WaitingForRound || status.Phase != PhaseWaiting {
		t.Fatalf("engine should still report waiting: %+v", status)
	}
	if status.Jobs[0].State != StateWaiting {
		t.Fatalf("job state = %q, want %q", status.Jobs[0].State, StateWaiting)
	}
}

func TestEnsureRoundStopsWhenReloginFails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Always the CAS form: the session is gone, not the round.
		w.Write([]byte(loginPageHTML))
	}))
	defer server.Close()

	runner := New(logbus.New(100), ratelimit.Unlimited, t.TempDir())
	runner.session.portalURL = server.URL + "/jsxsd/framework/xsrkxz.htmlx"
	runner.session.baseURL = server.URL + "/"

	done := make(chan bool, 1)
	go func() {
		// No credentials are stored, so the re-login attempt fails and the
		// wait must give up instead of spinning forever.
		done <- runner.ensureRound(context.Background(), runner.logger("", ""), time.Millisecond)
	}()

	select {
	case ok := <-done:
		if ok {
			t.Fatal("ensureRound should fail when the session cannot be restored")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("ensureRound did not give up on an unrecoverable session")
	}
}
