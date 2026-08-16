// Package config reads gate's per-project configuration.
//
// This is a deliberate reversal of a stated design. gate had no configuration
// file because a project's own Makefile targets and package.json scripts
// already are its config, and internal/detect reads them. Two gaps outgrew
// that: a timeout has no home in either manifest, so every gate in a run
// shared one value against a measured p99 of 65.0s; and a project cannot
// declare a check detection could never infer -- an e2e suite, a migration
// check, a schema diff.
//
// Config therefore ADDS and OVERRIDES, and never replaces. Detection always
// runs, so `--list` keeps answering why each gate is there, and a config file
// cannot remove a gate a project genuinely has.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/pelletier/go-toml/v2"
)

// ProjectFile is the per-project configuration, found from the DETECTED
// project root -- which is what makes `gate -C <elsewhere>` pick up that
// project's config for free.
const ProjectFile = ".gate.toml"

// Gate is one gate's configuration.
type Gate struct {
	// Run is a shell command for a gate detection could not infer. Empty
	// means this entry only overrides a detected gate.
	Run string

	// Timeout bounds this gate alone. HasTimeout distinguishes "not set"
	// from a deliberate 0, which means no limit.
	Timeout    time.Duration
	HasTimeout bool

	// Toolchain decides scheduling, not labelling: gates sharing one run in
	// sequence, and groups run concurrently. A custom gate that shares a
	// build cache with a detected one and does not say so will contend.
	Toolchain string

	// Source is the file this gate's settings came from, so --list can name
	// it. A gate that loses its source silently undoes the point of --list.
	Source string
}

// Config is the merged result of every layer that was found.
type Config struct {
	// Timeout is the default for gates with no timeout of their own.
	Timeout    time.Duration
	HasTimeout bool

	Gates map[string]Gate

	// Files lists what was read, nearest last, for --list to report.
	Files []string
}

// file is the on-disk shape. Durations are strings so a bad one can name the
// key it came from rather than failing as a type error.
type file struct {
	Defaults struct {
		Timeout string `toml:"timeout"`
	} `toml:"defaults"`
	Gates map[string]struct {
		Run       string `toml:"run"`
		Timeout   string `toml:"timeout"`
		Toolchain string `toml:"toolchain"`
	} `toml:"gates"`
}

// Load reads the user-level config, then the project's, layering the nearer
// file over the further one PER KEY rather than per file: a project that sets
// only one gate's timeout still inherits the user's defaults.
//
// A missing file is not an error. An unreadable or malformed one is: falling
// back to defaults after failing to understand a file the caller wrote is the
// classic configuration failure, and it is silent.
func Load(projectRoot string) (Config, error) {
	cfg := Config{Gates: map[string]Gate{}}

	for _, path := range []string{userPath(), filepath.Join(projectRoot, ProjectFile)} {
		if path == "" {
			continue
		}
		data, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return Config{}, fmt.Errorf("%s: %w", path, err)
		}
		if err := cfg.merge(path, data); err != nil {
			return Config{}, err
		}
		cfg.Files = append(cfg.Files, path)
	}
	return cfg, nil
}

// userPath is the user-level config location: $XDG_CONFIG_HOME/gate/config.toml,
// falling back to ~/.config/gate/config.toml.
func userPath() string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "gate", "config.toml")
}

// merge layers one file's settings over what is already there.
func (c *Config) merge(path string, data []byte) error {
	var f file
	dec := toml.NewDecoder(bytes.NewReader(data))
	// Errors rather than warns, and names every unknown key in one pass:
	// `timout = "10m"` silently ignored is the failure this guards against,
	// and reporting only the first would make fixing a file a game of
	// whack-a-mole.
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		var strict *toml.StrictMissingError
		if errors.As(err, &strict) {
			return fmt.Errorf("%s: unknown setting(s):\n%s", path, strict.String())
		}
		return fmt.Errorf("%s: %w", path, err)
	}

	if f.Defaults.Timeout != "" {
		d, err := time.ParseDuration(f.Defaults.Timeout)
		if err != nil || d < 0 {
			return fmt.Errorf("%s: defaults.timeout wants a duration like 90s or 5m, got %q", path, f.Defaults.Timeout)
		}
		c.Timeout, c.HasTimeout = d, true
	}

	for name, g := range f.Gates {
		merged := c.Gates[name]
		merged.Source = path
		if g.Run != "" {
			merged.Run = g.Run
		}
		if g.Toolchain != "" {
			merged.Toolchain = g.Toolchain
		}
		if g.Timeout != "" {
			d, err := time.ParseDuration(g.Timeout)
			if err != nil || d < 0 {
				return fmt.Errorf("%s: gates.%s.timeout wants a duration like 90s or 5m, got %q", path, name, g.Timeout)
			}
			merged.Timeout, merged.HasTimeout = d, true
		}
		c.Gates[name] = merged
	}
	return nil
}
