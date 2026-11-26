//go:build unix

package app

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/go-quicktest/qt"
)

// A .env that is a FIFO (some secret managers use one) blocks any open that
// waits for a writer; the fingerprint is computed before any gate starts, so
// a block there hangs every run.
func TestFingerprintSkipsAFIFODotenv(t *testing.T) {
	root := t.TempDir()
	fifo := filepath.Join(root, ".env")
	qt.Assert(t, qt.IsNil(syscall.Mkfifo(fifo, 0o600)))
	t.Cleanup(func() {
		if f, err := os.OpenFile(fifo, os.O_RDWR, 0); err == nil { //nolint:gosec // test fixture path
			_ = f.Close() // releases a reader a regression left blocked
		}
	})
	done := make(chan struct{})
	go func() {
		defer close(done)
		gatesFingerprint(t.Context(), options{root: root})
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the fingerprint blocked on a FIFO .env")
	}
}
