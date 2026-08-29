package engine

import (
	"sync"
	"time"
)

// Phase is the engine's overall lifecycle state.
type Phase string

const (
	PhaseIdle      Phase = "idle"
	PhaseLoggingIn Phase = "logging_in"
	PhaseReady     Phase = "ready"
	// PhaseWaiting means the login is good but enrollment has not opened
	// yet, so the engine is polling for the round to appear.
	PhaseWaiting  Phase = "waiting"
	PhaseRunning  Phase = "running"
	PhaseStopping Phase = "stopping"
	PhaseFinished Phase = "finished"
	PhaseError    Phase = "error"
)

// RunState is the state of one job or one target within a run.
type RunState string

const (
	StatePending RunState = "pending"
	// StateWaiting is a job held before its first round because enrollment
	// has not opened yet.
	StateWaiting   RunState = "waiting"
	StateRunning   RunState = "running"
	StateSucceeded RunState = "succeeded"
	StateCompleted RunState = "completed"
	StateStopped   RunState = "stopped"
	StateFailed    RunState = "failed"
	StateSkipped   RunState = "skipped"
)

// Status is a snapshot of everything the GUI renders.
type Status struct {
	Phase                Phase   `json:"phase"`
	LoggedIn             bool    `json:"logged_in"`
	Username             string  `json:"username"`
	Xkid                 string  `json:"xkid"`
	Mode                 string  `json:"mode"`
	RequestsPerSecond    float64 `json:"requests_per_second"`
	RequestIntervalMS    int64   `json:"request_interval_ms"`
	AboveRecommendedRate bool    `json:"above_recommended_rate"`
	Message              string  `json:"message"`
	// WaitingForRound is true while the engine is polling for enrollment to
	// open; RoundAttempts counts those checks and NextRoundCheckAt is when
	// the next one is due.
	WaitingForRound  bool             `json:"waiting_for_round"`
	RoundAttempts    int              `json:"round_attempts"`
	NextRoundCheckAt *time.Time       `json:"next_round_check_at,omitempty"`
	StartedAt        *time.Time       `json:"started_at,omitempty"`
	FinishedAt       *time.Time       `json:"finished_at,omitempty"`
	Jobs             []JobStatus      `json:"jobs"`
	Enrolled         []EnrolledCourse `json:"enrolled"`
}

// JobStatus is the progress of one job.
type JobStatus struct {
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	Enabled   bool           `json:"enabled"`
	State     RunState       `json:"state"`
	Round     int            `json:"round"`
	MaxRounds int            `json:"max_rounds"`
	Message   string         `json:"message"`
	Targets   []TargetStatus `json:"targets"`
}

// TargetStatus is the progress of one course target.
type TargetStatus struct {
	Name      string    `json:"name"`
	Type      string    `json:"type"`
	Keyword   string    `json:"keyword"`
	Enabled   bool      `json:"enabled"`
	State     RunState  `json:"state"`
	Searches  int       `json:"searches"`
	Attempts  int       `json:"attempts"`
	Found     int       `json:"found"`
	Successes int       `json:"successes"`
	Message   string    `json:"message"`
	UpdatedAt time.Time `json:"updated_at"`
}

// EnrolledCourse records one successful enrollment.
type EnrolledCourse struct {
	Job      string    `json:"job"`
	Target   string    `json:"target"`
	Name     string    `json:"name"`
	Teacher  string    `json:"teacher"`
	Time     string    `json:"time"`
	Location string    `json:"location"`
	At       time.Time `json:"at"`
}

// statusStore holds the mutable status behind a mutex. Jobs may run
// concurrently, so every field is written through it.
type statusStore struct {
	mu       sync.RWMutex
	status   Status
	jobIndex map[string]int
}

func newStatusStore() *statusStore {
	return &statusStore{
		status: Status{
			Phase:    PhaseIdle,
			Jobs:     []JobStatus{},
			Enrolled: []EnrolledCourse{},
		},
		jobIndex: map[string]int{},
	}
}

// Snapshot returns a deep copy that is safe to hand to the GUI layer.
func (s *statusStore) Snapshot() Status {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := s.status
	out.Jobs = make([]JobStatus, len(s.status.Jobs))
	for i, job := range s.status.Jobs {
		copied := job
		copied.Targets = make([]TargetStatus, len(job.Targets))
		copy(copied.Targets, job.Targets)
		out.Jobs[i] = copied
	}
	out.Enrolled = make([]EnrolledCourse, len(s.status.Enrolled))
	copy(out.Enrolled, s.status.Enrolled)

	if s.status.NextRoundCheckAt != nil {
		next := *s.status.NextRoundCheckAt
		out.NextRoundCheckAt = &next
	}
	if s.status.StartedAt != nil {
		started := *s.status.StartedAt
		out.StartedAt = &started
	}
	if s.status.FinishedAt != nil {
		finished := *s.status.FinishedAt
		out.FinishedAt = &finished
	}

	return out
}

func (s *statusStore) update(mutate func(*Status)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	mutate(&s.status)
}

func (s *statusStore) setPhase(phase Phase, message string) {
	s.update(func(status *Status) {
		status.Phase = phase
		status.Message = message
	})
}

// setJobs resets the per-job view for a fresh run.
func (s *statusStore) setJobs(jobs []JobStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.status.Jobs = jobs
	s.jobIndex = make(map[string]int, len(jobs))
	for i, job := range jobs {
		s.jobIndex[job.ID] = i
	}
}

func (s *statusStore) updateJob(jobID string, mutate func(*JobStatus)) {
	s.mu.Lock()
	defer s.mu.Unlock()

	index, ok := s.jobIndex[jobID]
	if !ok || index >= len(s.status.Jobs) {
		return
	}
	mutate(&s.status.Jobs[index])
}

// updateAllJobs applies mutate to every job, used for state that belongs to
// the run as a whole, such as waiting for enrollment to open.
func (s *statusStore) updateAllJobs(mutate func(*JobStatus)) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i := range s.status.Jobs {
		mutate(&s.status.Jobs[i])
	}
}

func (s *statusStore) updateTarget(jobID string, targetIndex int, mutate func(*TargetStatus)) {
	s.mu.Lock()
	defer s.mu.Unlock()

	index, ok := s.jobIndex[jobID]
	if !ok || index >= len(s.status.Jobs) {
		return
	}
	targets := s.status.Jobs[index].Targets
	if targetIndex < 0 || targetIndex >= len(targets) {
		return
	}
	mutate(&targets[targetIndex])
	targets[targetIndex].UpdatedAt = time.Now()
}

func (s *statusStore) addEnrolled(course EnrolledCourse) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status.Enrolled = append(s.status.Enrolled, course)
}
