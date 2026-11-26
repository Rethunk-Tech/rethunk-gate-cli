package result

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Prune removes the records of roots that no longer exist, with their
// durations and fuzz siblings, once the record is older than maxAge. The age
// leaves a scratch checkout that is deleted and recreated, or a drive that is
// briefly unmounted, its history. A record naming no root cannot be tied to a
// directory and is treated as gone. It returns how many records it removed.
func Prune(maxAge time.Duration) (removed int) {
	state, err := StateDir()
	if err != nil {
		return 0
	}
	dir := filepath.Join(state, "gate", "results")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	for _, e := range entries {
		name := e.Name()
		stem, ok := strings.CutSuffix(name, ".json")
		if !ok || strings.HasSuffix(stem, ".durations") {
			continue
		}
		info, err := e.Info()
		if err != nil || time.Since(info.ModTime()) < maxAge {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, name)) //nolint:gosec // a name read from the results dir
		if err != nil {
			continue
		}
		var rec struct {
			Root string `json:"root"`
		}
		if json.Unmarshal(data, &rec) != nil {
			continue
		}
		if rec.Root != "" {
			if _, err := os.Stat(rec.Root); err == nil || !os.IsNotExist(err) {
				continue
			}
		}
		if os.Remove(filepath.Join(dir, name)) != nil {
			continue
		}
		removed++
		_ = os.Remove(filepath.Join(dir, stem+".durations.json"))
		_ = os.Remove(filepath.Join(dir, stem+".fuzz.tsv"))
	}
	return removed
}
