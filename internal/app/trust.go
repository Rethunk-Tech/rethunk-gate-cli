package app

import (
	"context"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/Rethunk-Tech/rethunk-gate-cli/internal/config"
)

var scpRemote = regexp.MustCompile(`^(?:[^@/\s]+@)?([^:/\s]+):([^/].*)$`)

// trusted reports whether a bare or role run may install and gate root: it
// carries its own .gate.toml, has no origin, or its origin org is trusted.
// An origin on any host other than github.com / Azure DevOps is not.
func trusted(ctx context.Context, root string, cfg config.Config) bool {
	if root == "" {
		return true
	}
	if _, err := os.Stat(filepath.Join(root, config.ProjectFile)); err == nil {
		return true
	}
	out, err := exec.CommandContext(ctx, "git", "-C", root, "config", "--get", "remote.origin.url").Output() //nolint:gosec // fixed argv, root is detected
	remote := strings.TrimSpace(string(out))
	if err != nil || remote == "" {
		return true
	}
	allowed := config.DefaultTrustedOrgs
	if cfg.HasTrustedOrgs {
		allowed = cfg.TrustedOrgs
	}
	return slices.Contains(allowed, originOrg(remote))
}

// originOrg is the lowercase org (or Azure DevOps namespace) of a remote URL,
// or "" for an unrecognised host.
func originOrg(remote string) string {
	var host, path string
	if m := scpRemote.FindStringSubmatch(remote); m != nil && !strings.Contains(remote, "://") {
		host, path = m[1], m[2]
	} else if u, err := url.Parse(remote); err == nil {
		host, path = u.Hostname(), u.Path
	}
	parts := strings.FieldsFunc(path, func(r rune) bool { return r == '/' })
	switch strings.ToLower(host) {
	case "dev.azure.com", "ssh.dev.azure.com":
		if len(parts) > 0 && parts[0] == "v3" {
			parts = parts[1:]
		}
	case "github.com":
	default:
		return ""
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.ToLower(parts[0])
}
