package app

import (
	"errors"
	"testing"
)

func TestToResultLogIncompleteStatusNotOK(t *testing.T) {
	rec := toResult(gateResult{
		spec:     gateSpec{argv: []string{"sh", "-c", "echo hi; exit 0"}, display: "echo hi"},
		code:     Success,
		started:  true,
		ran:      true,
		fatalErr: errors.New("log /dev/full may be incomplete: write /dev/full: no space left on device"),
	})
	if rec.Status == "ok" {
		t.Fatalf("status=%q code=%v; want non-ok status when the log could not be finished", rec.Status, rec.Code)
	}
	if rec.Code == nil || *rec.Code == 0 {
		t.Fatalf("code=%v; want non-zero", rec.Code)
	}
}
