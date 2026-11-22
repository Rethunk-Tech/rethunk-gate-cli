package result

import (
	"os"
	"testing"
)

// A suite run under `gate --isolate` inherits GATE_STATE_DIR, which would win
// over the XDG and HOME settings the state-directory tests control.
func TestMain(m *testing.M) {
	_ = os.Unsetenv(StateDirEnv)
	os.Exit(m.Run())
}
