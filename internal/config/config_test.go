package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-quicktest/qt"
)

// isolate points the user-level layer at an empty directory. Without it these
// tests would read the developer's own ~/.config/gate/config.toml and pass or
// fail by accident of their machine.
func isolate(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)
	return home
}

func write(t *testing.T, dir, name, body string) {
	t.Helper()
	path := filepath.Join(dir, name)
	qt.Assert(t, qt.IsNil(os.MkdirAll(filepath.Dir(path), 0o755)))
	qt.Assert(t, qt.IsNil(os.WriteFile(path, []byte(body), 0o644)))
}

// Nothing configured is the common case and must not be an error -- gate had
// no configuration file at all until this existed.
func TestNoConfigAnywhereIsNotAnError(t *testing.T) {
	isolate(t)
	cfg, err := Load(t.TempDir())
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.HasLen(cfg.Gates, 0))
	qt.Check(t, qt.HasLen(cfg.Files, 0))
}

// The layers merge per KEY, not per file: a project that overrides one gate
// still inherits the user's settings for every other gate. Merging per file
// would make the project file all-or-nothing.
func TestTheProjectFileWinsPerKeyNotPerFile(t *testing.T) {
	home := isolate(t)
	write(t, home, "gate/config.toml", "[gates.test]\nrun = \"user test\"\n\n[gates.lint]\nrun = \"user lint\"\n")

	root := t.TempDir()
	write(t, root, ProjectFile, "[gates.test]\nrun = \"project test\"\n")

	cfg, err := Load(root)
	qt.Assert(t, qt.IsNil(err))

	// The nearer file wins the gate it set...
	qt.Check(t, qt.Equals(cfg.Gates["test"].Run, "project test"))
	// ...and leaves the one it did not mention alone.
	qt.Check(t, qt.Equals(cfg.Gates["lint"].Run, "user lint"))

	qt.Check(t, qt.HasLen(cfg.Files, 2))
}

// serial is a bool, and a bool has a zero value that means the same thing as
// "absent" unless presence is tracked separately. Without HasSerial a project
// could never turn off a user-level default: writing serial = false would be
// indistinguishable from not writing it at all.
func TestSerialFalseIsDistinguishableFromUnset(t *testing.T) {
	home := isolate(t)
	write(t, home, "gate/config.toml", "[gates.test]\nserial = true\n")

	root := t.TempDir()
	write(t, root, ProjectFile, "[gates.test]\nserial = false\n")

	cfg, err := Load(root)
	qt.Assert(t, qt.IsNil(err))

	// The project turned this one back off, which is a statement, not silence.
	qt.Check(t, qt.IsTrue(cfg.Gates["test"].HasSerial))
	qt.Check(t, qt.IsFalse(cfg.Gates["test"].Serial))

	// A gate nobody mentioned is not serial, and does not claim to have said so.
	qt.Check(t, qt.IsFalse(cfg.Gates["lint"].HasSerial))
	qt.Check(t, qt.IsFalse(cfg.Gates["lint"].Serial))
}

// The per-key merge is what Load promises: a project that sets one gate's
// timeout still inherits the user's settings for every other gate.
func TestAProjectTimeoutLeavesOtherGatesOnTheirDefaults(t *testing.T) {
	home := isolate(t)
	write(t, home, "gate/config.toml", "[gates.e2e]\ntimeout = \"10m\"\n")

	root := t.TempDir()
	write(t, root, ProjectFile, "[gates.test]\ntimeout = \"5m\"\n")

	cfg, err := Load(root)
	qt.Assert(t, qt.IsNil(err))

	qt.Check(t, qt.Equals(cfg.Gates["test"].Timeout, 5*time.Minute))
	qt.Check(t, qt.Equals(cfg.Gates["e2e"].Timeout, 10*time.Minute))
	// A gate nobody mentioned takes the default, and does not claim to have
	// said so -- which is what the runner reads to leave it alone.
	qt.Check(t, qt.IsFalse(cfg.Gates["lint"].HasTimeout))
}

// Zero is a statement -- run this gate with no limit -- and an absent key is
// silence. A plain duration cannot tell them apart, and they mean opposite
// things.
func TestATimeoutOfZeroIsDistinguishableFromUnset(t *testing.T) {
	home := isolate(t)
	write(t, home, "gate/config.toml", "[gates.test]\ntimeout = \"5m\"\n")

	root := t.TempDir()
	write(t, root, ProjectFile, "[gates.test]\ntimeout = \"0\"\n")

	cfg, err := Load(root)
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.IsTrue(cfg.Gates["test"].HasTimeout))
	qt.Check(t, qt.Equals(cfg.Gates["test"].Timeout, time.Duration(0)))
}

// A duration that cannot be read is reported like an unknown key -- refused,
// named, and all at once, so a file with two bad values is fixed in one pass
// rather than one run per mistake.
func TestUnusableTimeoutsAreRefusedAllAtOnce(t *testing.T) {
	isolate(t)
	root := t.TempDir()
	write(t, root, ProjectFile, "[gates.test]\ntimeout = \"soon\"\n\n[gates.lint]\ntimeout = \"-1m\"\n")

	_, err := Load(root)
	qt.Assert(t, qt.IsNotNil(err))
	for _, want := range []string{"gates.test.timeout", "soon", "gates.lint.timeout", "-1m", ProjectFile} {
		qt.Check(t, qt.IsTrue(strings.Contains(err.Error(), want)),
			qt.Commentf("error = %v", err))
	}
}

// A typo must not degrade to defaults. `timout = "10m"` silently ignored is
// the classic configuration failure, and reporting only the first unknown key
// would make fixing a file a game of whack-a-mole -- which is the whole reason
// this TOML library was chosen over the alternative.
func TestUnknownKeysAreRefusedAllAtOnce(t *testing.T) {
	isolate(t)
	root := t.TempDir()
	write(t, root, ProjectFile, "[gates.test]\nrunn = \"go test\"\nseriall = true\n")

	_, err := Load(root)
	qt.Assert(t, qt.IsNotNil(err))
	for _, want := range []string{"runn", "seriall", ProjectFile} {
		qt.Check(t, qt.IsTrue(strings.Contains(err.Error(), want)),
			qt.Commentf("error = %v", err))
	}
}

// Malformed TOML refuses rather than falling back to defaults. Falling back
// after failing to understand a file someone wrote is silent, and the reader
// would be left wondering why their settings did nothing.
func TestMalformedTOMLRefusesAndNamesTheFile(t *testing.T) {
	isolate(t)
	root := t.TempDir()
	write(t, root, ProjectFile, "[gates.test\nrun = \"go test\"\n")

	_, err := Load(root)
	qt.Assert(t, qt.IsNotNil(err))
	qt.Check(t, qt.IsTrue(strings.Contains(err.Error(), ProjectFile)),
		qt.Commentf("error = %v", err))
}
