package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/datayurei/robyou/internal/ratelimit"
)

const legacyFile = `{
  "interval_seconds": 4,
  "login_check_rounds": 10,
  "courses": [
    {"name": "高等数学", "type": "inplan", "keyword": "高等数学", "enabled": true},
    {"name": "乐理", "type": "public", "keyword": "乐理", "enabled": true, "public_category": 0}
  ]
}`

func TestLoadUpgradesLegacyConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), JobsFileName)
	if err := os.WriteFile(path, []byte(legacyFile), 0o600); err != nil {
		t.Fatal(err)
	}

	config, err := Load(path)
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}

	if len(config.Jobs) != 1 {
		t.Fatalf("legacy courses should collapse into one job, got %d jobs", len(config.Jobs))
	}
	job := config.Jobs[0]
	if len(job.Targets) != 2 {
		t.Fatalf("job has %d targets, want 2", len(job.Targets))
	}
	if job.IntervalSeconds != 4 {
		t.Fatalf("interval = %v, want 4", job.IntervalSeconds)
	}
	// 10 rounds at 4s per round is the wall time the old setting implied.
	if config.LoginCheckSeconds != 40 {
		t.Fatalf("login check = %v, want 40", config.LoginCheckSeconds)
	}
	if config.Mode != ModeSequence {
		t.Fatalf("mode = %q, want %q", config.Mode, ModeSequence)
	}
	if config.RequestsPerSecond != ratelimit.DefaultRPS {
		t.Fatalf("rate = %v, want %v", config.RequestsPerSecond, ratelimit.DefaultRPS)
	}
}

func TestLoadCreatesDefaultWhenMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", JobsFileName)

	config, err := Load(path)
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}
	if len(config.Jobs) == 0 {
		t.Fatal("default config should contain example jobs")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("default config was not written: %v", err)
	}
}

func TestNormalizeFillsDefaultsAndKeepsCustomRate(t *testing.T) {
	config := Config{
		Mode: "SEQUENCE",
		Jobs: []Job{
			{Name: "", Targets: []Target{{Keyword: "x", Type: "PUBLIC"}}},
			{ID: "job-1", Name: "dup", MaxRounds: -5, Targets: []Target{{Keyword: "y", Type: "weird"}}},
		},
		RequestsPerSecond: 3.5,
	}
	config.Normalize()

	if config.RequestsPerSecond != 3.5 {
		t.Fatalf("rate = %v, want the configured 3.5 (no clamping)", config.RequestsPerSecond)
	}
	if config.Jobs[0].ID == config.Jobs[1].ID {
		t.Fatal("duplicate job IDs should be reassigned")
	}
	if config.Jobs[0].IntervalSeconds != DefaultIntervalSeconds {
		t.Fatalf("interval = %v, want %v", config.Jobs[0].IntervalSeconds, DefaultIntervalSeconds)
	}
	if config.Jobs[1].MaxRounds != 0 {
		t.Fatalf("negative max rounds should become 0, got %d", config.Jobs[1].MaxRounds)
	}
	if config.Jobs[0].Targets[0].Type != TypePublic {
		t.Fatalf("type = %q, want %q", config.Jobs[0].Targets[0].Type, TypePublic)
	}
	if config.Jobs[1].Targets[0].Type != TypeInPlan {
		t.Fatalf("unknown type should fall back to %q, got %q", TypeInPlan, config.Jobs[1].Targets[0].Type)
	}
}

func TestNormalizeTreatsNegativeRateAsUnlimited(t *testing.T) {
	config := Config{RequestsPerSecond: -2}
	config.Normalize()

	if config.RequestsPerSecond != ratelimit.Unlimited {
		t.Fatalf("rate = %v, want %v", config.RequestsPerSecond, ratelimit.Unlimited)
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		config  Config
		wantErr bool
	}{
		{
			name:    "no jobs",
			config:  Config{},
			wantErr: true,
		},
		{
			name: "no enabled jobs",
			config: Config{Jobs: []Job{
				{Name: "a", Enabled: false, Targets: []Target{{Keyword: "x", Enabled: true, Type: TypeInPlan}}},
			}},
			wantErr: true,
		},
		{
			name: "enabled job without enabled targets",
			config: Config{Jobs: []Job{
				{Name: "a", Enabled: true, Targets: []Target{{Keyword: "x", Type: TypeInPlan}}},
			}},
			wantErr: true,
		},
		{
			name: "missing keyword",
			config: Config{Jobs: []Job{
				{Name: "a", Enabled: true, Targets: []Target{{Enabled: true, Type: TypeInPlan}}},
			}},
			wantErr: true,
		},
		{
			name: "valid",
			config: Config{Jobs: []Job{
				{Name: "a", Enabled: true, Targets: []Target{{Keyword: "x", Enabled: true, Type: TypeInPlan}}},
			}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.config.Validate(); (err != nil) != tt.wantErr {
				t.Fatalf("Validate() = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestCredentialsRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), CredentialsFileName)

	if creds, err := LoadCredentials(path); err != nil || creds.Username != "" {
		t.Fatalf("missing file should be empty and not an error, got %v / %v", creds, err)
	}

	want := Credentials{Username: "2020000000", Password: "secret"}
	if err := SaveCredentials(path, want); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("credentials file mode = %v, want 0600", perm)
	}

	got, err := LoadCredentials(path)
	if err != nil || got != want {
		t.Fatalf("LoadCredentials() = %v / %v, want %v", got, err, want)
	}

	if err := ClearCredentials(path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("credentials file should be gone")
	}
}

func TestNormalizeRoundRetryAlwaysHasAnInterval(t *testing.T) {
	tests := []struct {
		name  string
		value float64
		want  float64
	}{
		{name: "missing", value: 0, want: DefaultRoundRetrySeconds},
		{name: "negative", value: -10, want: DefaultRoundRetrySeconds},
		{name: "custom", value: 120, want: 120},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Waiting for enrollment to open cannot be disabled: without a
			// round there is nothing to run.
			config := Config{RoundRetrySeconds: tt.value}
			config.Normalize()

			if config.RoundRetrySeconds != tt.want {
				t.Fatalf("RoundRetrySeconds = %v, want %v", config.RoundRetrySeconds, tt.want)
			}
		})
	}
}

func TestLegacyConfigGetsARoundRetryInterval(t *testing.T) {
	path := filepath.Join(t.TempDir(), JobsFileName)
	if err := os.WriteFile(path, []byte(legacyFile), 0o600); err != nil {
		t.Fatal(err)
	}

	config, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if config.RoundRetrySeconds != DefaultRoundRetrySeconds {
		t.Fatalf("RoundRetrySeconds = %v, want %v", config.RoundRetrySeconds, DefaultRoundRetrySeconds)
	}
}
