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
	qt.Check(t, qt.IsFalse(cfg.HasTimeout))
	qt.Check(t, qt.HasLen(cfg.Gates, 0))
	qt.Check(t, qt.HasLen(cfg.Files, 0))
}

// The layers merge per KEY, not per file: a project that overrides one gate's
// timeout still inherits the user's default for every other gate. Merging per
// file would make the project file all-or-nothing.
func TestTheProjectFileWinsPerKeyNotPerFile(t *testing.T) {
	home := isolate(t)
	write(t, home, "gate/config.toml", "[defaults]\ntimeout = \"2m\"\n\n[gates.test]\ntimeout = \"5m\"\ntoolchain = \"go\"\n")

	root := t.TempDir()
	write(t, root, ProjectFile, "[gates.test]\ntimeout = \"10m\"\n")

	cfg, err := Load(root)
	qt.Assert(t, qt.IsNil(err))

	// The user's default survives, because the project said nothing about it.
	qt.Check(t, qt.IsTrue(cfg.HasTimeout))
	qt.Check(t, qt.Equals(cfg.Timeout, 2*time.Minute))

	// The nearer file wins the key it set...
	qt.Check(t, qt.Equals(cfg.Gates["test"].Timeout, 10*time.Minute))
	// ...and leaves the keys it did not set alone.
	qt.Check(t, qt.Equals(cfg.Gates["test"].Toolchain, "go"))

	qt.Check(t, qt.HasLen(cfg.Files, 2))
}

// A typo must not degrade to defaults. `timout = "10m"` silently ignored is
// the classic configuration failure, and reporting only the first unknown key
// would make fixing a file a game of whack-a-mole -- which is the whole reason
// this TOML library was chosen over the alternative.
func TestUnknownKeysAreRefusedAllAtOnce(t *testing.T) {
	isolate(t)
	root := t.TempDir()
	write(t, root, ProjectFile, "[defaults]\ntimout = \"10m\"\nkeepp = 3\n")

	_, err := Load(root)
	qt.Assert(t, qt.IsNotNil(err))
	for _, want := range []string{"timout", "keepp", ProjectFile} {
		qt.Check(t, qt.IsTrue(strings.Contains(err.Error(), want)),
			qt.Commentf("error = %v", err))
	}
}

// A duration that cannot be parsed names the key it came from, since "invalid
// duration" alone would send the reader looking through the whole file.
func TestABadDurationNamesItsKey(t *testing.T) {
	isolate(t)
	root := t.TempDir()
	write(t, root, ProjectFile, "[gates.test]\ntimeout = \"soon\"\n")

	_, err := Load(root)
	qt.Assert(t, qt.IsNotNil(err))
	qt.Check(t, qt.IsTrue(strings.Contains(err.Error(), "gates.test.timeout")),
		qt.Commentf("error = %v", err))
}

// Malformed TOML refuses rather than falling back to defaults. Falling back
// after failing to understand a file someone wrote is silent, and the reader
// would be left wondering why their settings did nothing.
func TestMalformedTOMLRefusesAndNamesTheFile(t *testing.T) {
	isolate(t)
	root := t.TempDir()
	write(t, root, ProjectFile, "[defaults\ntimeout = \"2m\"\n")

	_, err := Load(root)
	qt.Assert(t, qt.IsNotNil(err))
	qt.Check(t, qt.IsTrue(strings.Contains(err.Error(), ProjectFile)),
		qt.Commentf("error = %v", err))
}
