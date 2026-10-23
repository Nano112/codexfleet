package fleet

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// The registry is a user-level list of fleet files that have been launched or
// adopted, so tools (the Claude Code plugin, `codexfleet fleets`) can find
// fleets whose fleet file lives outside the current directory.

// RegistryEntry is one registered fleet file.
type RegistryEntry struct {
	Path    string    `json:"path"`
	Updated time.Time `json:"updated"`
}

type registryDoc struct {
	Fleets []RegistryEntry `json:"fleets"`
}

// RegistryPath is $CODEXFLEET_REGISTRY, else $XDG_CONFIG_HOME/codexfleet/fleets.json,
// else ~/.config/codexfleet/fleets.json.
func RegistryPath() string {
	if p := os.Getenv("CODEXFLEET_REGISTRY"); p != "" {
		return p
	}
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "codexfleet", "fleets.json")
}

// Registered lists registered fleet files that still exist, newest first.
func Registered() []RegistryEntry {
	b, err := os.ReadFile(RegistryPath())
	if err != nil {
		return nil
	}
	var doc registryDoc
	if json.Unmarshal(b, &doc) != nil {
		return nil
	}
	var out []RegistryEntry
	for _, e := range doc.Fleets {
		if _, err := os.Stat(e.Path); err == nil {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Updated.After(out[j].Updated) })
	return out
}

// Register records a fleet file in the registry (best effort).
func Register(path string) error {
	reg := RegistryPath()
	unlock, err := Lock(reg + ".lock")
	if err != nil {
		return err
	}
	defer unlock()
	var doc registryDoc
	if b, err := os.ReadFile(reg); err == nil {
		json.Unmarshal(b, &doc)
	}
	kept := doc.Fleets[:0]
	for _, e := range doc.Fleets {
		if e.Path == path {
			continue
		}
		if _, err := os.Stat(e.Path); err == nil {
			kept = append(kept, e)
		}
	}
	doc.Fleets = append(kept, RegistryEntry{Path: path, Updated: time.Now().UTC().Truncate(time.Second)})
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	return WriteFileAtomic(reg, append(b, '\n'), 0o600)
}

func (f *Fleet) register() {
	if f.Cfg.Path != "" {
		Register(f.Cfg.Path)
	}
}
