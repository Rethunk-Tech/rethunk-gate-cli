package detect

import (
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// playwrightTest matches the Playwright runner however it is reached: bare,
// through bunx or npx, or by a resolved node_modules/.bin path.
var playwrightTest = regexp.MustCompile(`(?:^|[\s/])playwright\s+test\b`)

// IsE2E reports whether a gate is a browser end-to-end suite, which a default
// run leaves out: the fleet's local budget is 10s warm, and one Playwright
// suite alone measures minutes.
//
// A gate is e2e when its name has an `e2e` segment (`e2e`, `test:e2e`,
// `test:e2e:a11y`), or when its command runs `playwright test`, or runs a
// package script or turbo task that is e2e by either test. Scripts are read
// from dir's package.json, where the gate runs.
func IsE2E(dir, name string, argv []string) bool {
	if e2eName(name) {
		return true
	}
	pkg, _ := readPackageJSON(filepath.Join(dir, "package.json"))
	return runsE2E(strings.Join(argv, " "), pkg.Scripts, nil)
}

func e2eName(name string) bool {
	return slices.Contains(strings.Split(name, ":"), "e2e")
}

// runsE2E reads a command for Playwright, or for a `run <name>...` that
// names an e2e script or task. seen stops a script that runs itself.
func runsE2E(command string, scripts map[string]string, seen []string) bool {
	if playwrightTest.MatchString(command) {
		return true
	}
	fields := strings.Fields(command)
	for i, f := range fields {
		if f != "run" {
			continue
		}
		for _, name := range fields[i+1:] {
			if name == "&&" || name == "||" || name == ";" || name == "|" {
				break
			}
			name = strings.TrimPrefix(strings.Trim(name, `'"`), "//#")
			if strings.HasPrefix(name, "-") {
				continue
			}
			if e2eName(name) {
				return true
			}
			body, ok := scripts[name]
			if ok && !slices.Contains(seen, name) && runsE2E(body, scripts, append(seen, name)) {
				return true
			}
		}
	}
	return false
}
