// Command gate runs a project gate, keeps its complete output in a log, and
// reports only the verdict.
package main

import (
	"context"
	"os"

	"github.com/Rethunk-Tech/rethunk-gate-cli/internal/app"
)

// version is overwritten at build time via -ldflags.
var version = "dev"

func main() {
	os.Exit(int(app.Run(context.Background(), version, os.Args[1:], os.Stdout, os.Stderr)))
}
