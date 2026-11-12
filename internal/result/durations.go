package result

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// maxSamples bounds the passing durations kept per gate; the newest win.
const maxSamples = 20

func durationsPath(root string) (string, error) {
	path, err := Path(Canonical(root))
	if err != nil {
		return "", err
	}
	return strings.TrimSuffix(path, ".json") + ".durations.json", nil
}

// LoadDurations returns each gate's recent passing durations in milliseconds,
// oldest first. Missing or unreadable history is empty: it only tunes a limit.
func LoadDurations(root string) map[string][]int64 {
	path, err := durationsPath(root)
	if err != nil {
		return nil
	}
	data, err := readFile(path)
	if err != nil {
		return nil
	}
	var got map[string][]int64
	if json.Unmarshal(data, &got) != nil {
		return nil
	}
	return got
}

// AppendDurations adds one passing duration per gate to root's history.
func AppendDurations(root string, add map[string]int64) error {
	if len(add) == 0 {
		return nil
	}
	path, err := durationsPath(root)
	if err != nil {
		return err
	}
	all := LoadDurations(root)
	if all == nil {
		all = map[string][]int64{}
	}
	for name, ms := range add {
		all[name] = append(all[name], ms)
		if n := len(all[name]); n > maxSamples {
			all[name] = all[name][n-maxSamples:]
		}
	}
	data, err := json.Marshal(all)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".durations-*")
	if err != nil {
		return err
	}
	_, werr := tmp.Write(data)
	if err := errors.Join(werr, tmp.Close()); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	return nil
}
