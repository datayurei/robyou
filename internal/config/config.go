// Package config holds the on-disk job configuration and credentials.
//
// The schema grew out of the flat enroll_config.json used by the CLI: what
// was a single list of course targets is now a list of jobs, each holding its
// own targets. Legacy files are read and upgraded transparently.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/datayurei/robyou/internal/ratelimit"
)

// SchemaVersion is the current job-configuration schema.
const SchemaVersion = 2

// Job execution modes.
const (
	// ModeSequence runs jobs one after another, in list order. The next job
	// starts only when the previous one finishes.
	ModeSequence = "sequence"
	// ModeConcurrent runs every enabled job at the same time. They still
	// share one rate limiter, so the combined request rate is unchanged.
	ModeConcurrent = "concurrent"
)

// Course types accepted by a Target.
const (
	TypeInPlan = "inplan"
	TypePublic = "public"
)

// Defaults applied when a field is missing or out of range.
const (
	DefaultIntervalSeconds     = 3.0
	DefaultRequestDelaySeconds = 0.5
	DefaultLoginCheckSeconds   = 180.0
	DefaultRoundRetrySeconds   = 30.0
)

// Config is the whole job configuration.
type Config struct {
	Version int `json:"version"`
	// RequestsPerSecond paces every outbound request. It defaults to
	// ratelimit.Recommended (1 rps); a higher value is honoured but
	// reported as above-recommended, and 0 disables pacing entirely.
	RequestsPerSecond float64 `json:"requests_per_second"`
	// LoginCheckSeconds is how often the session liveness check runs.
	// Zero disables it.
	LoginCheckSeconds float64 `json:"login_check_seconds"`
	// RoundRetrySeconds is how often to check whether enrollment has opened
	// while no course-selection round is available yet.
	RoundRetrySeconds float64 `json:"round_retry_seconds"`
	// Mode is ModeSequence or ModeConcurrent.
	Mode string `json:"mode"`
	Jobs []Job  `json:"jobs"`
}

// Job is an ordered group of course targets polled together.
type Job struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
	// IntervalSeconds is the pause between polling rounds of this job.
	IntervalSeconds float64 `json:"interval_seconds,omitempty"`
	// MaxRounds stops the job after this many rounds. Zero means unlimited.
	MaxRounds int `json:"max_rounds,omitempty"`
	// StopOnFirstSuccess ends the whole job as soon as any one of its
	// targets is enrolled, instead of waiting for every target.
	StopOnFirstSuccess bool     `json:"stop_on_first_success,omitempty"`
	Targets            []Target `json:"targets"`
}

// Target is one course search and the rules for enrolling from its results.
type Target struct {
	Name                string            `json:"name"`
	Type                string            `json:"type"`
	Keyword             string            `json:"keyword"`
	Enabled             bool              `json:"enabled"`
	PublicCategory      *int              `json:"public_category,omitempty"`
	Filters             map[string]string `json:"filters,omitempty"`
	FuzzyFilterKeywords []string          `json:"fuzzy_filter_keywords,omitempty"`
	ExactFilterKeywords []string          `json:"exact_filter_keywords,omitempty"`
	RequestDelaySeconds float64           `json:"request_delay_seconds,omitempty"`
	// ContinueAfterSuccessful keeps polling this target for more sections
	// after a successful enrollment.
	ContinueAfterSuccessful bool `json:"continue_after_successful,omitempty"`
}

// Credentials are the SSO login details.
type Credentials struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// legacyConfig is the flat pre-jobs schema.
type legacyConfig struct {
	IntervalSeconds  float64  `json:"interval_seconds"`
	LoginCheckRounds *int     `json:"login_check_rounds"`
	Courses          []Target `json:"courses"`
}

// Default returns a configuration with one example job.
func Default() Config {
	category := 1
	return Config{
		Version:           SchemaVersion,
		RequestsPerSecond: ratelimit.DefaultRPS,
		LoginCheckSeconds: DefaultLoginCheckSeconds,
		RoundRetrySeconds: DefaultRoundRetrySeconds,
		Mode:              ModeSequence,
		Jobs: []Job{
			{
				ID:              "job-1",
				Name:            "主选课任务",
				Enabled:         true,
				IntervalSeconds: DefaultIntervalSeconds,
				Targets: []Target{
					{
						Name:                    "示例计划内课程",
						Type:                    TypeInPlan,
						Keyword:                 "课程关键词",
						Enabled:                 true,
						FuzzyFilterKeywords:     []string{"不想要的教师或课程片段"},
						ExactFilterKeywords:     []string{"精确排除的教师或课程名"},
						RequestDelaySeconds:     DefaultRequestDelaySeconds,
						ContinueAfterSuccessful: false,
					},
				},
			},
			{
				ID:              "job-2",
				Name:            "公选课备选任务",
				Enabled:         false,
				IntervalSeconds: DefaultIntervalSeconds,
				Targets: []Target{
					{
						Name:                "示例公选课",
						Type:                TypePublic,
						Keyword:             "公选课关键词",
						Enabled:             true,
						PublicCategory:      &category,
						RequestDelaySeconds: DefaultRequestDelaySeconds,
					},
				},
			},
		},
	}
}

// Normalize fills in defaults, clamps out-of-range values and assigns any
// missing job IDs. It never fails: an unusable field is replaced, not
// rejected, so the GUI can always render what is on disk.
func (c *Config) Normalize() {
	c.Version = SchemaVersion

	if c.RequestsPerSecond < 0 {
		c.RequestsPerSecond = ratelimit.Unlimited
	}

	if c.LoginCheckSeconds < 0 {
		c.LoginCheckSeconds = 0
	}

	// Unlike the login check, waiting for enrollment to open cannot be
	// turned off: without a round there is nothing to run, so a missing or
	// nonsensical value falls back to the default interval.
	if c.RoundRetrySeconds <= 0 {
		c.RoundRetrySeconds = DefaultRoundRetrySeconds
	}

	switch strings.ToLower(strings.TrimSpace(c.Mode)) {
	case ModeConcurrent:
		c.Mode = ModeConcurrent
	default:
		c.Mode = ModeSequence
	}

	seen := make(map[string]bool, len(c.Jobs))
	for i := range c.Jobs {
		job := &c.Jobs[i]
		if job.ID == "" || seen[job.ID] {
			job.ID = fmt.Sprintf("job-%d", i+1)
		}
		seen[job.ID] = true
		if job.Name == "" {
			job.Name = job.ID
		}
		if job.IntervalSeconds <= 0 {
			job.IntervalSeconds = DefaultIntervalSeconds
		}
		if job.MaxRounds < 0 {
			job.MaxRounds = 0
		}
		for j := range job.Targets {
			target := &job.Targets[j]
			target.Type = normalizeType(target.Type)
			if target.Name == "" {
				target.Name = target.Keyword
			}
			if target.RequestDelaySeconds < 0 {
				target.RequestDelaySeconds = 0
			}
			if target.PublicCategory != nil && *target.PublicCategory < 0 {
				target.PublicCategory = nil
			}
		}
	}
}

// Validate reports problems that would make a run pointless or wrong. It is
// called before starting, not when loading.
func (c *Config) Validate() error {
	if len(c.Jobs) == 0 {
		return errors.New("配置中没有任务 (no jobs configured)")
	}

	enabledJobs := 0
	for i, job := range c.Jobs {
		if !job.Enabled {
			continue
		}
		enabledJobs++

		enabledTargets := 0
		for j, target := range job.Targets {
			if !target.Enabled {
				continue
			}
			enabledTargets++
			if strings.TrimSpace(target.Keyword) == "" {
				return fmt.Errorf("任务 %q 的第 %d 个课程未填写搜索关键词", job.Name, j+1)
			}
			if target.Type != TypeInPlan && target.Type != TypePublic {
				return fmt.Errorf("任务 %q 的第 %d 个课程类型必须是 inplan 或 public", job.Name, j+1)
			}
		}
		if enabledTargets == 0 {
			return fmt.Errorf("任务 %q (第 %d 个) 没有启用的课程", job.Name, i+1)
		}
	}
	if enabledJobs == 0 {
		return errors.New("没有启用的任务 (no enabled jobs)")
	}

	return nil
}

// EnabledJobs returns the jobs that will actually run, in order.
func (c *Config) EnabledJobs() []Job {
	out := make([]Job, 0, len(c.Jobs))
	for _, job := range c.Jobs {
		if job.Enabled {
			out = append(out, job)
		}
	}
	return out
}

// EnabledTargets returns the targets of a job that will actually run.
func (j *Job) EnabledTargets() []Target {
	out := make([]Target, 0, len(j.Targets))
	for _, target := range j.Targets {
		if target.Enabled {
			out = append(out, target)
		}
	}
	return out
}

// Label is the display name of a target, falling back to its keyword.
func (t *Target) Label() string {
	if strings.TrimSpace(t.Name) != "" {
		return t.Name
	}
	return t.Keyword
}

// Load reads a job configuration, upgrading the legacy flat schema if needed.
// A missing file yields Default() written to path.
func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			config := Default()
			if writeErr := Save(path, config); writeErr != nil {
				return config, writeErr
			}
			return config, nil
		}
		return Config{}, fmt.Errorf("read %s: %w", path, err)
	}

	var config Config
	if err := json.Unmarshal(data, &config); err != nil {
		return Config{}, fmt.Errorf("parse %s: %w", path, err)
	}

	if len(config.Jobs) == 0 {
		var legacy legacyConfig
		if err := json.Unmarshal(data, &legacy); err == nil && len(legacy.Courses) > 0 {
			config = fromLegacy(legacy)
		}
	}

	config.Normalize()
	return config, nil
}

// Save writes the configuration, creating parent directories as needed.
func Save(path string, config Config) error {
	config.Normalize()
	return writeJSON(path, config, 0o600)
}

// fromLegacy wraps a flat course list into a single job.
func fromLegacy(legacy legacyConfig) Config {
	interval := legacy.IntervalSeconds
	if interval <= 0 {
		interval = DefaultIntervalSeconds
	}

	loginCheck := DefaultLoginCheckSeconds
	if legacy.LoginCheckRounds != nil {
		// The old setting counted polling rounds; express it as the wall
		// time those rounds would have taken.
		loginCheck = float64(*legacy.LoginCheckRounds) * interval
		if *legacy.LoginCheckRounds <= 0 {
			loginCheck = 0
		}
	}

	return Config{
		Version:           SchemaVersion,
		RequestsPerSecond: ratelimit.DefaultRPS,
		LoginCheckSeconds: loginCheck,
		RoundRetrySeconds: DefaultRoundRetrySeconds,
		Mode:              ModeSequence,
		Jobs: []Job{{
			ID:              "job-1",
			Name:            "导入的任务 (imported)",
			Enabled:         true,
			IntervalSeconds: interval,
			Targets:         legacy.Courses,
		}},
	}
}

// LoadCredentials reads secret.json. A missing file yields empty credentials
// and no error, so the GUI can prompt for them.
func LoadCredentials(path string) (Credentials, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Credentials{}, nil
		}
		return Credentials{}, fmt.Errorf("read %s: %w", path, err)
	}

	var creds Credentials
	if err := json.Unmarshal(data, &creds); err != nil {
		return Credentials{}, fmt.Errorf("parse %s: %w", path, err)
	}
	creds.Username = strings.TrimSpace(creds.Username)

	return creds, nil
}

// SaveCredentials writes secret.json with owner-only permissions.
func SaveCredentials(path string, creds Credentials) error {
	return writeJSON(path, creds, 0o600)
}

// ClearCredentials removes a stored secret file.
func ClearCredentials(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove %s: %w", path, err)
	}
	return nil
}

func normalizeType(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case TypePublic:
		return TypePublic
	default:
		return TypeInPlan
	}
}

func writeJSON(path string, value any, perm os.FileMode) error {
	content, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("encode %s: %w", path, err)
	}
	content = append(content, '\n')

	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("create %s: %w", dir, err)
		}
	}
	if err := os.WriteFile(path, content, perm); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}

	return nil
}
