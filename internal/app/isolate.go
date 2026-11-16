package app

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// resolveIsolate is what an isolated run is: the flag says so, --no-isolate
// says not, and otherwise a pre-push hook says so. The hook is the case the
// isolation exists for -- a push is the last cheap chance to catch what CI
// would reject -- while every other run stays warm and personal.
func resolveIsolate(on, off, hook bool) bool {
	switch {
	case off:
		return false
	case on:
		return true
	}
	return hook
}

// isolateAware reports whether this gate runs isolated: a gate's own
// `isolate` key settles it where the file said so, and the run's answer
// stands everywhere else.
func isolateAware(opts options, spec gateSpec) bool {
	if spec.hasIsolate {
		return spec.isolate
	}
	return opts.isolate
}

// prePushProcs is how far up the process tree the hook is looked for. git and
// lefthook sit one or two levels above a gate the hook started; a deeper
// ancestor is the terminal, and reading it proves nothing.
const prePushProcs = 8

// psDeadline bounds the one ps call a system without /proc costs per level.
// Detecting the hook must never hold a run up.
const psDeadline = 2 * time.Second

// inPrePushHook reports whether gate was started by a git pre-push hook.
//
// Neither git nor lefthook exports a hook name -- measured, not assumed: a
// pre-push hook's environment holds the same GIT_* variables a pre-commit one
// does, and lefthook sets no LEFTHOOK_HOOK. What both do is leave the
// process that ran the hook above it, so the hook is read from there.
//
// A var so this repository's suite can pin it off: that suite runs inside
// this repository's own pre-push hook, and results that change depending on
// who started the test binary are a trap for whoever debugs them.
var inPrePushHook = walkForPrePushHook

func walkForPrePushHook() bool {
	pid := os.Getpid()
	for range prePushProcs {
		argv, ppid, ok := procArgv(pid)
		if !ok {
			return false
		}
		if matchPrePushHook(argv) {
			return true
		}
		if ppid <= 1 {
			return false
		}
		pid = ppid
	}
	return false
}

// matchPrePushHook reads one process for the two programs that run a pre-push
// hook: lefthook naming the hook, and git pushing.
func matchPrePushHook(argv []string) bool {
	if len(argv) == 0 {
		return false
	}
	switch filepath.Base(argv[0]) {
	case "lefthook":
		return len(argv) > 2 && argv[1] == "run" && argv[2] == "pre-push"
	case "git":
		// The subcommand is not always argv[1]: `git -c k=v push` and
		// `git -C dir push` are the same push. Only leading global
		// options are skipped, so `git log --grep push` is not one.
		return gitSubcommand(argv[1:]) == "push"
	}
	return false
}

// isPrePushHook reads a process list, nearest first, for a pre-push hook.
func isPrePushHook(procs [][]string) bool {
	return slices.ContainsFunc(procs, matchPrePushHook)
}

// gitValueOptions are the global options that take their value as the next
// argument, so the word after one is not the subcommand.
var gitValueOptions = map[string]bool{
	"-C": true, "-c": true, "--git-dir": true, "--work-tree": true,
	"--namespace": true, "--exec-path": true, "--super-prefix": true,
}

// gitSubcommand returns the first argument after git's global options.
func gitSubcommand(args []string) string {
	for i := 0; i < len(args); i++ {
		if !strings.HasPrefix(args[i], "-") {
			return args[i]
		}
		if gitValueOptions[args[i]] {
			i++
		}
	}
	return ""
}

// procArgv reads a process's argv and its parent, from /proc where it exists
// and from ps otherwise. ok is false where neither answers -- Windows has no
// ps and no /proc -- which ends the walk: without the process tree there is no
// hook to find, and --isolate covers the run.
func procArgv(pid int) (argv []string, ppid int, ok bool) {
	if raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cmdline"); err == nil {
		for field := range strings.SplitSeq(string(raw), "\x00") {
			if field != "" {
				argv = append(argv, field)
			}
		}
		stat, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
		if err != nil {
			return nil, 0, false
		}
		// The command name is parenthesised and may itself hold spaces or
		// parentheses, so the fields after it start at the last ")".
		_, rest, found := strings.Cut(string(stat), ")")
		if !found {
			return nil, 0, false
		}
		fields := strings.Fields(rest)
		if len(fields) < 2 {
			return nil, 0, false
		}
		if ppid, err = strconv.Atoi(fields[1]); err != nil {
			return nil, 0, false
		}
		return argv, ppid, true
	}

	// BSDs and macOS have no /proc. One ps call answers both questions; its
	// argv column may be truncated, which costs a recognisable "push" only.
	psCtx, cancel := context.WithTimeout(context.Background(), psDeadline)
	defer cancel()
	out, err := exec.CommandContext(psCtx, "ps", "-o", "ppid=", "-o", "args=", "-p", strconv.Itoa(pid)).Output() //nolint:gosec // fixed argv, pid is this process's ancestor
	if err != nil {
		return nil, 0, false
	}
	line := strings.TrimSpace(string(out))
	parent, rest, found := strings.Cut(line, " ")
	if !found {
		return nil, 0, false
	}
	if ppid, err = strconv.Atoi(parent); err != nil {
		return nil, 0, false
	}
	return strings.Fields(rest), ppid, true
}

// isolatedEnv is the environment an isolated gate runs with. A fresh HOME
// hides the global git identity, signing files and dotfiles a developer's
// machine has and CI never will; the caches are passed through explicitly
// because losing them would make every push a cold build.
//
// The paths are resolved from the caller's own environment before HOME is
// replaced: after the swap nothing can tell where the warm cache lives.
func isolatedEnv(env []string, home string) []string {
	lookup := func(key string) string {
		for _, e := range slices.Backward(env) {
			if k, v, _ := strings.Cut(e, "="); k == key {
				return v
			}
		}
		return ""
	}
	callerHome := lookup("HOME")
	if callerHome == "" {
		callerHome = home
	}
	pick := func(key, fallback string) string {
		if v := lookup(key); v != "" {
			return v
		}
		return fallback
	}
	dir := func(base, elem string) string { return filepath.Join(base, elem) }
	xdgCache := pick("XDG_CACHE_HOME", dir(callerHome, ".cache"))
	xdgData := pick("XDG_DATA_HOME", dir(callerHome, ".local/share"))
	gopath := pick("GOPATH", dir(callerHome, "go"))

	out := make([]string, 0, len(env)+12)
	for _, e := range env {
		// The XDG directories are unset rather than moved: a tool that finds
		// them keeps reading this machine's config and state, which is the
		// leak the fresh HOME closes.
		if k, _, _ := strings.Cut(e, "="); strings.HasPrefix(k, "XDG_") && strings.HasSuffix(k, "_HOME") {
			continue
		}
		out = append(out, e)
	}
	return append(out,
		"HOME="+home,
		"GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_CONFIG_NOSYSTEM=1",
		"GOPATH="+gopath,
		"GOMODCACHE="+pick("GOMODCACHE", dir(gopath, "pkg/mod")),
		"GOCACHE="+pick("GOCACHE", dir(xdgCache, "go-build")),
		"npm_config_cache="+pick("npm_config_cache", dir(callerHome, ".npm")),
		"PNPM_CONFIG_STORE_DIR="+pick("PNPM_CONFIG_STORE_DIR", dir(xdgData, "pnpm/store")),
		"BUN_INSTALL_CACHE_DIR="+pick("BUN_INSTALL_CACHE_DIR", dir(callerHome, ".bun/install/cache")),
		"UV_CACHE_DIR="+pick("UV_CACHE_DIR", dir(xdgCache, "uv")),
	)
}
