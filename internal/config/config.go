// Package config reads gate's per-project configuration.
//
// A project's manifests are its config for WHAT to run, and internal/detect
// reads them. What they cannot express is a check detection could never infer
// -- an e2e suite, a migration check -- or an order one gate needs against
// another.
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
	"slices"
	"strings"
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

	// Serial sequences this gate against the other serial ones instead of
	// running it concurrently. HasSerial distinguishes "not set" from a
	// deliberate false, so a project can opt back out of a user-level
	// default. It is the only way a gate is sequenced: gate runs everything
	// concurrently unless something says otherwise.
	Serial    bool
	HasSerial bool

	// Timeout bounds this gate alone; zero means no limit. HasTimeout
	// distinguishes "not set" from a deliberate 0, which mean opposite
	// things: inherit the default, versus run with no limit at all.
	Timeout    time.Duration
	HasTimeout bool

	// Source is the file this gate's settings came from, so --list can name
	// it. A gate that loses its source silently undoes the point of --list.
	Source string
}

// Config is the merged result of every layer that was found.
type Config struct {
	Gates map[string]Gate

	// Files lists what was read, nearest last, for --list to report.
	Files []string
}

// file is the on-disk shape. Both optional settings are pointers so an absent
// key is distinguishable from a deliberate false or 0, which is what lets a
// project turn off a user-level default. Timeout stays a string here so a
// malformed duration is reported like an unknown key rather than decoded into
// something that silently means "no limit".
type file struct {
	Gates map[string]struct {
		Run     string  `toml:"run"`
		Serial  *bool   `toml:"serial"`
		Timeout *string `toml:"timeout"`
	} `toml:"gates"`
}

// ParseTimeout reads a timeout wherever one is written -- the --timeout flag
// and a gate's own `timeout` key both come through here, so `5m`, `90s` and a
// disabling `0` mean the same thing in either place.
func ParseTimeout(value string) (time.Duration, error) {
	d, err := time.ParseDuration(value)
	if err != nil || d < 0 {
		return 0, fmt.Errorf("wants a duration like 90s or 5m, got %q", value)
	}
	return d, nil
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

	// Collected rather than returned at the first one, so a file with two bad
	// durations is fixed in one pass -- the same reason unknown keys are
	// reported all at once.
	var unusable []string

	for name, g := range f.Gates {
		merged := c.Gates[name]
		merged.Source = path
		if g.Run != "" {
			merged.Run = g.Run
		}
		if g.Serial != nil {
			merged.Serial, merged.HasSerial = *g.Serial, true
		}
		if g.Timeout != nil {
			d, err := ParseTimeout(*g.Timeout)
			if err != nil {
				unusable = append(unusable, fmt.Sprintf("gates.%s.timeout %v", name, err))
			} else {
				merged.Timeout, merged.HasTimeout = d, true
			}
		}
		c.Gates[name] = merged
	}

	if len(unusable) > 0 {
		// Sorted because map iteration is not: the same file must report the
		// same message every time.
		slices.Sort(unusable)
		return fmt.Errorf("%s: unusable setting(s):\n%s", path, strings.Join(unusable, "\n"))
	}
	return nil
}
