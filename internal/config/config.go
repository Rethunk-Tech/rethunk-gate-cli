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

	// Env carries per-gate environment variables, layered per variable
	// rather than per table: a project that sets one variable still
	// inherits the user's values for the rest. Values are literal -- no
	// $VAR expansion -- so what runs is what the file says. HasEnv
	// distinguishes "not set" from a table that happens to be empty.
	Env    map[string]string
	HasEnv bool

	// Dir runs this gate in a directory other than its default (the
	// project root for detected and configured gates). Relative paths
	// resolve against the project root. HasDir distinguishes "not set"
	// from an empty value, which is refused rather than guessed at.
	Dir    string
	HasDir bool

	// AllowFailure keeps a failing gate from failing the run: the gate's
	// own verdict is still reported -- the FAIL line, the NDJSON status
	// word, the log trailer -- but the aggregate exit status ignores it,
	// and a serial group runs on past it. HasAllowFailure distinguishes
	// "not set" from a deliberate false, so a project can opt back out
	// of a user-level default the way serial does.
	AllowFailure    bool
	HasAllowFailure bool

	// E2E settles whether this gate is a browser e2e suite, which a default
	// run leaves out, where detection's reading of its name and command is
	// wrong. HasE2E distinguishes "not set", which leaves detection's answer
	// standing, from a deliberate false.
	E2E    bool
	HasE2E bool

	// Source is the file this gate's settings came from, so --list can name
	// it. A gate that loses its source silently undoes the point of --list.
	Source string
}

// Config is the merged result of every layer that was found.
type Config struct {
	Gates map[string]Gate

	// Files lists what was read, nearest last, for --list to report.
	Files []string

	// Budget is the wall time a passing run without e2e gates may take
	// before gate warns; zero disables the warning. HasBudget distinguishes
	// "not set", which takes the default, from a deliberate 0.
	Budget    time.Duration
	HasBudget bool
}

// file is the on-disk shape. Both optional settings are pointers so an absent
// key is distinguishable from a deliberate false or 0, which is what lets a
// project turn off a user-level default. Timeout stays a string here so a
// malformed duration is reported like an unknown key rather than decoded into
// something that silently means "no limit".
//
// `dir` and `allow-failure` are the only spellings: a retired alias such as
// `workdir` or `continue-on-error` is refused as an unknown key by
// DisallowUnknownFields, the same as any typo, rather than silently accepted.
type file struct {
	Budget *string `toml:"budget"`
	Gates  map[string]struct {
		Run          string            `toml:"run"`
		Serial       *bool             `toml:"serial"`
		Timeout      *string           `toml:"timeout"`
		Env          map[string]string `toml:"env"`
		Dir          *string           `toml:"dir"`
		AllowFailure *bool             `toml:"allow-failure"`
		E2E          *bool             `toml:"e2e"`
	} `toml:"gates"`
}

// reservedEnv names variables a gate must not set. The runner appends a
// gate's env after its own, so a setting here would displace gate's own
// mechanism rather than the command's environment -- and displacing
// GATE_ACTIVE_ROOTS re-opens the detection loop that guard exists to close.
const reservedEnv = "GATE_ACTIVE_ROOTS"

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
		data, err := os.ReadFile(path) //nolint:gosec // paths are the fixed user or project config locations
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
		if strict, ok := errors.AsType[*toml.StrictMissingError](err); ok {
			return fmt.Errorf("%s: unknown setting(s):\n%s", path, strict.String())
		}
		return fmt.Errorf("%s: %w", path, err)
	}

	// Collected rather than returned at the first one, so a file with two bad
	// durations is fixed in one pass -- the same reason unknown keys are
	// reported all at once.
	var unusable []string

	// Same parse as a gate's timeout, and the same refusal for a value that
	// is not a duration.
	if f.Budget != nil {
		d, err := ParseTimeout(*f.Budget)
		if err != nil {
			unusable = append(unusable, fmt.Sprintf("budget %v", err))
		} else {
			c.Budget, c.HasBudget = d, true
		}
	}

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
		// Env layers per variable, not per table: the nearer file overrides
		// the variables it names and inherits the rest, the same promise
		// Load makes per key. Sorted below with the rest, because map
		// iteration would otherwise report the same file differently each
		// time.
		if g.Env != nil {
			if merged.Env == nil {
				merged.Env = map[string]string{}
			}
			for k, v := range g.Env {
				if k == "" || strings.ContainsAny(k, "=\x00") || k == reservedEnv {
					unusable = append(unusable, fmt.Sprintf("gates.%s.env has an unusable variable name %q", name, k))
					continue
				}
				merged.Env[k] = v
			}
			merged.HasEnv = true
		}
		// `dir` is the one spelling, and an empty directory is refused
		// rather than resolved against nothing, which would run the gate
		// wherever the caller happened to stand.
		if g.Dir != nil {
			if *g.Dir == "" {
				unusable = append(unusable, fmt.Sprintf("gates.%s.dir is empty", name))
			} else {
				merged.Dir, merged.HasDir = *g.Dir, true
			}
		}
		if g.AllowFailure != nil {
			merged.AllowFailure, merged.HasAllowFailure = *g.AllowFailure, true
		}
		if g.E2E != nil {
			merged.E2E, merged.HasE2E = *g.E2E, true
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
