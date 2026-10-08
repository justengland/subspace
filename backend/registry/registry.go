package registry

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Repo is a registered working tree (name → absolute path).
type Repo struct {
	Name         string `json:"name"`
	AbsolutePath string `json:"absolutePath"`
}

func filePath(home string) string {
	return filepath.Join(home, "repos.json")
}

// List returns registered Repos. Missing or empty registry → empty slice, no error.
func List(home string) ([]Repo, error) {
	b, err := os.ReadFile(filePath(home))
	if err != nil {
		if os.IsNotExist(err) {
			return []Repo{}, nil
		}
		return nil, err
	}
	if len(strings.TrimSpace(string(b))) == 0 {
		return []Repo{}, nil
	}
	var m map[string]string
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	out := make([]Repo, 0, len(m))
	for name, p := range m {
		out = append(out, Repo{Name: name, AbsolutePath: p})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Has reports whether name is registered in home/repos.json.
func Has(home, name string) (bool, error) {
	repos, err := List(home)
	if err != nil {
		return false, err
	}
	for _, r := range repos {
		if r.Name == name {
			return true, nil
		}
	}
	return false, nil
}

// Add registers name → absolutePath in home/repos.json, creating the file if needed.
func Add(home, name, path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	m := map[string]string{}
	b, err := os.ReadFile(filePath(home))
	if err == nil && len(strings.TrimSpace(string(b))) > 0 {
		if err := json.Unmarshal(b, &m); err != nil {
			return err
		}
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(home, 0o755); err != nil {
		return err
	}
	m[name] = abs
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filePath(home), append(data, '\n'), 0o644)
}
