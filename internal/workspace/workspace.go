// Package workspace holds what castra remembers about each workspace: a host
// folder with a devcontainer, and the container castra made from it.
package workspace

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Label marks every container castra manages; its value is the workspace name.
const Label = "castra.workspace"

// State is the record castra keeps per workspace.
type State struct {
	Name        string `json:"name"`
	Folder      string `json:"folder"`
	Engine      string `json:"engine"`
	ContainerID string `json:"containerId"`
	RemoteUser  string `json:"remoteUser"`
	RemoteHome  string `json:"remoteHome"`
	Workdir     string `json:"workdir"`
}

// Alias is the SSH host name of the workspace.
func (s State) Alias() string { return s.Name + ".castra" }

var unsafeChars = regexp.MustCompile(`[^a-z0-9-]+`)

// NameFromFolder derives a workspace name from a folder: its base name,
// lower-cased, with anything that is not a letter, digit or dash replaced.
func NameFromFolder(folder string) (string, error) {
	return CleanName(filepath.Base(folder))
}

// CleanName makes s usable as a workspace name and SSH host name.
func CleanName(s string) (string, error) {
	n := strings.Trim(unsafeChars.ReplaceAllString(strings.ToLower(s), "-"), "-")
	if n == "" {
		return "", fmt.Errorf("cannot make a workspace name from %q; pass --name", s)
	}
	return n, nil
}

// StateDir is where castra keeps workspace records and locks.
func StateDir() (string, error) {
	if d := os.Getenv("XDG_STATE_HOME"); d != "" {
		return filepath.Join(d, "castra"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "state", "castra"), nil
}

func recordPath(name string) (string, error) {
	dir, err := StateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "workspaces", name+".json"), nil
}

// LockPath is the lock file that serializes `up` runs on one workspace.
func LockPath(name string) (string, error) {
	dir, err := StateDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return filepath.Join(dir, name+".lock"), nil
}

// ErrNotFound means castra has no record of the workspace.
var ErrNotFound = errors.New("workspace not found")

// Load reads the record of one workspace.
func Load(name string) (State, error) {
	p, err := recordPath(name)
	if err != nil {
		return State{}, err
	}
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return State{}, fmt.Errorf("%w: %s (run `castra up` in its folder first)", ErrNotFound, name)
	}
	if err != nil {
		return State{}, err
	}
	var s State
	if err := json.Unmarshal(b, &s); err != nil {
		return State{}, fmt.Errorf("read %s: %w", p, err)
	}
	return s, nil
}

// Save writes the record of one workspace.
func Save(s State) error {
	p, err := recordPath(s.Name)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// All reads every workspace record, sorted by name.
func All() ([]State, error) {
	dir, err := StateDir()
	if err != nil {
		return nil, err
	}
	files, err := filepath.Glob(filepath.Join(dir, "workspaces", "*.json"))
	if err != nil {
		return nil, err
	}
	var out []State
	for _, f := range files {
		s, err := Load(strings.TrimSuffix(filepath.Base(f), ".json"))
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}
