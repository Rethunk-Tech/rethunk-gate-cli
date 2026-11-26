package app

import (
	"strings"
	"testing"
	"time"

	"github.com/go-quicktest/qt"
)

func TestTimeoutRemedyQuotesNonBareKeys(t *testing.T) {
	t.Parallel()
	var b strings.Builder
	writeTimeoutRemedy(&b, "test:unit", time.Second, false)
	qt.Check(t, qt.StringContains(b.String(), `[gates."test:unit"] timeout = "2s"`))
	b.Reset()
	writeTimeoutRemedy(&b, "test", time.Second, false)
	qt.Check(t, qt.StringContains(b.String(), `[gates.test] timeout = "2s"`))
}

func TestTimeoutRemedyDoublesAWrappedCommandsLimit(t *testing.T) {
	t.Parallel()
	var b strings.Builder
	writeTimeoutRemedy(&b, "", 10*time.Minute, true)
	qt.Check(t, qt.StringContains(b.String(), "--timeout 20m,"))
}
