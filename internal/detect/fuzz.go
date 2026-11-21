package detect

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
)

const fuzzSource = "convention: go (Fuzz targets)"

var fuzzTarget = regexp.MustCompile(`(?m)^func (Fuzz\w+)\(\w+ \*testing\.F\)`)

// fuzzScript runs each "dir:Target" argument with a short -fuzztime, four at
// a time, each with -parallel a quarter of the CPUs (go test otherwise starts
// one worker per CPU per target): go test takes one -fuzz target and one package per run. A crasher
// stays in the package's testdata/fuzz, and go test prints that path in the
// failure. Output is held until a target finishes so failures do not
// interleave.
const fuzzScript = `[ -n "$GATE_FUZZ_STATE" ] && mkdir -p "${GATE_FUZZ_STATE%/*}"
printf '%s\n' "$@" | xargs -P 4 -I{} sh -c '
d=${1%%:*}; t=${1#*:}; rc=0
s=$(date +%s%3N)
out=$(cd "$d" && go test -run='"'"'^$'"'"' -fuzz="^$t\$" -parallel=@PAR@ -fuzztime="${GATE_FUZZTIME:-2s}" . 2>&1) || rc=1
if [ $rc = 0 ] && [ -n "$GATE_FUZZ_STATE" ]; then e=$(date +%s%3N); printf "%s\t%s\t%s\n" "$1" "$((e/1000))" "$((e-s))" >> "$GATE_FUZZ_STATE"; fi
printf "%s\n" "$out"; exit $rc' _ {}`

// fuzzGates is one gate for a module that declares Fuzz targets, none where
// it declares none. GATE_FUZZTIME overrides the 2s each target gets.
func fuzzGates(root string) []Gate {
	targets := map[string][]string{}
	tree, err := os.OpenRoot(root)
	if err != nil {
		return nil
	}
	defer func() { _ = tree.Close() }()
	fsys := tree.FS()
	_ = fs.WalkDir(fsys, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil //nolint:nilerr // an unreadable entry holds no targets
		}
		if d.IsDir() {
			name := d.Name()
			if path != "." && (name == "vendor" || name == "node_modules" || name == "testdata" || strings.HasPrefix(name, ".") || exists(filepath.Join(root, path, "go.mod"))) {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		body, err := fs.ReadFile(fsys, path)
		if err != nil {
			return nil //nolint:nilerr // an unreadable file holds no targets
		}
		for _, m := range fuzzTarget.FindAllSubmatch(body, -1) {
			dir := "./" + filepath.Dir(path)
			targets[dir] = append(targets[dir], string(m[1]))
		}
		return nil
	})
	if len(targets) == 0 {
		return nil
	}
	dirs := make([]string, 0, len(targets))
	for d := range targets {
		dirs = append(dirs, d)
	}
	sort.Strings(dirs)
	script := strings.Replace(fuzzScript, "@PAR@", strconv.Itoa(fuzzParallel()), 1)
	argv := []string{"sh", "-c", script, "sh"}
	for _, d := range dirs {
		ts := targets[d]
		sort.Strings(ts)
		for _, t := range ts {
			argv = append(argv, d+":"+t)
		}
	}
	return []Gate{{Name: "fuzz", Argv: argv, Source: fuzzSource, Summary: fmt.Sprintf("go test -fuzz (%d targets)", len(argv)-4)}}
}

// fuzzParallel is each target's -parallel: four targets run at once, so a
// quarter of the CPUs apiece keeps the fleet of workers near the CPU count.
func fuzzParallel() int {
	return max(1, runtime.NumCPU()/4)
}
