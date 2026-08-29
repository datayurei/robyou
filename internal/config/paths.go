package config

import (
	"os"
	"path/filepath"
)

// File names, kept the same as the original CLI so existing setups keep working.
const (
	JobsFileName        = "enroll_config.json"
	CredentialsFileName = "secret.json"
	appDirName          = "robyou"
)

// Paths locates the configuration files.
type Paths struct {
	Dir         string `json:"dir"`
	Jobs        string `json:"jobs"`
	Credentials string `json:"credentials"`
	// Portable is true when the files live next to the working directory
	// rather than in the per-user configuration directory.
	Portable bool `json:"portable"`
}

// Resolve picks where configuration lives. A config file in the working
// directory wins, so a checkout with an existing enroll_config.json behaves as
// it always did; otherwise the per-user config directory is used, which is
// what a double-clicked GUI build needs.
func Resolve() Paths {
	if wd, err := os.Getwd(); err == nil {
		local := filepath.Join(wd, JobsFileName)
		if _, err := os.Stat(local); err == nil {
			return Paths{
				Dir:         wd,
				Jobs:        local,
				Credentials: filepath.Join(wd, CredentialsFileName),
				Portable:    true,
			}
		}
	}

	base, err := os.UserConfigDir()
	if err != nil || base == "" {
		base, err = os.UserHomeDir()
		if err != nil || base == "" {
			base = "."
		}
	}
	dir := filepath.Join(base, appDirName)

	return Paths{
		Dir:         dir,
		Jobs:        filepath.Join(dir, JobsFileName),
		Credentials: filepath.Join(dir, CredentialsFileName),
	}
}
