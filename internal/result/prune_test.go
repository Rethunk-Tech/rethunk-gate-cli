package result

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-quicktest/qt"
)

func TestPruneRemovesOldRecordsOfGoneRoots(t *testing.T) {
	state := t.TempDir()
	t.Setenv(StateDirEnv, state)
	dir := filepath.Join(state, "gate", "results")
	qt.Assert(t, qt.IsNil(os.MkdirAll(dir, 0o700)))
	live := t.TempDir()
	write := func(name, root string, age time.Duration) {
		path := filepath.Join(dir, name)
		qt.Assert(t, qt.IsNil(os.WriteFile(path, []byte(`{"root":"`+root+`"}`), 0o600)))
		old := time.Now().Add(-age)
		qt.Assert(t, qt.IsNil(os.Chtimes(path, old, old)))
	}
	write("gone.json", "/nonexistent/x", 2*time.Hour)
	write("gone.durations.json", "", 2*time.Hour)
	write("young.json", "/nonexistent/y", 0)
	write("live.json", live, 2*time.Hour)

	qt.Check(t, qt.Equals(Prune(time.Hour), 1))
	for name, want := range map[string]bool{"gone.json": false, "gone.durations.json": false, "young.json": true, "live.json": true} {
		_, err := os.Stat(filepath.Join(dir, name))
		qt.Check(t, qt.Equals(err == nil, want), qt.Commentf("%s", name))
	}
}
