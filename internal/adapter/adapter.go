// Package adapter describes each AI tool tote knows: where it keeps its
// files, which ones are worth carrying, how to install it, and how to point
// it at a private folder. Adding a tool means adding one JSON file — either
// built in (builtin/) or in <tote config dir>/adapters/.
package adapter

import (
	"embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

//go:embed builtin/*.json
var builtin embed.FS

type Adapter struct {
	Name    string `json:"name"`
	Display string `json:"display"`
	Home    struct {
		Env     string `json:"env"`     // env var that relocates the tool's folder
		Default string `json:"default"` // "~/.claude" style
	} `json:"home"`
	Command string `json:"command"`
	Install struct {
		Unix    string `json:"unix"`
		Windows string `json:"windows"`
	} `json:"install"`
	InstructionFile string `json:"instruction_file"` // auto-read by the tool on start
	Carry           []struct {
		Path string `json:"path"` // relative to home; may contain * globs
		What string `json:"what"`
	} `json:"carry"`
	Chats struct {
		Glob   string `json:"glob"`
		Format string `json:"format"`
	} `json:"chats"`
	Never []string `json:"never"` // basenames never packed (globs)
	Allow []string `json:"allow"` // exceptions to Never, e.g. "*.example"

	// Guest mode.
	SecureEnv       string            `json:"secure_env"`       // env var that gives the private folder its own login slot
	MinVersion      map[string]string `json:"min_version"`      // per OS: older builds can't keep logins separate
	InstalledBinary map[string]string `json:"installed_binary"` // where the installer puts the program, relative to HOME
	Logout          []string          `json:"logout"`           // args that log out (run before wiping)
	RunEnv          map[string]string `json:"run_env"`          // extra env when tote launches the tool
	ProjectDirs     string            `json:"project_dirs"`     // folder whose children are named after project paths

	// PrivateEnv: env vars that point the tool at its private folder;
	// "{home}" is replaced by that folder. E.g. {"CODEX_HOME": "{home}"}.
	PrivateEnv map[string]string `json:"private_env"`
	// FilesSubdir: where the tool's files live inside {home} (Gemini: ".gemini").
	FilesSubdir string `json:"files_subdir"`
	// PrivateSetup: lines forced into the tool's config in guest mode, e.g.
	// keep Codex's login in a file in its own folder instead of the Keychain.
	PrivateSetup []SetupLine `json:"private_setup"`
	// Verified is false for support written from docs but not yet run for real.
	Verified bool `json:"verified"`

	// Generic: a tool tote has no description for; the AI packs it itself.
	Generic bool `json:"-"`
	// FromBox: this description arrived inside a box (written by an AI), so
	// its install command is shown to the person before it runs.
	FromBox bool `json:"-"`
}

var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,39}$`)

// ValidName reports whether s can be a tool name (a folder name in boxes).
func ValidName(s string) bool { return nameRe.MatchString(s) }

// Generic returns a placeholder for a tool tote doesn't know yet.
func Generic(name string) Adapter {
	return Adapter{Name: name, Display: name, Generic: true}
}

// Parse reads one tool description and checks the basics.
func Parse(b []byte) (Adapter, error) {
	var a Adapter
	if err := json.Unmarshal(b, &a); err != nil {
		return a, fmt.Errorf("not valid JSON: %w", err)
	}
	if !ValidName(a.Name) {
		return a, fmt.Errorf("bad tool name %q", a.Name)
	}
	if a.Display == "" {
		a.Display = a.Name
	}
	return a, nil
}

// SetupLine replaces any line starting with Key in File with Line (at the top).
type SetupLine struct {
	File string `json:"file"`
	Key  string `json:"key"`
	Line string `json:"line"`
}

// junk is rebuildable on the other side and never worth carrying.
var junk = []string{".venv", "venv", "node_modules", "__pycache__", ".git", ".DS_Store", "*.pyc"}

// HomeDir is the tool's folder on this computer, honouring its env var.
func (a Adapter) HomeDir() (string, error) {
	if a.Home.Env != "" {
		if v := os.Getenv(a.Home.Env); v != "" {
			return v, nil
		}
	}
	d := a.Home.Default
	if strings.HasPrefix(d, "~/") {
		h, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		d = filepath.Join(h, d[2:])
	}
	return d, nil
}

func matchAny(pats []string, name string) bool {
	for _, pat := range pats {
		if ok, _ := filepath.Match(pat, name); ok {
			return true
		}
	}
	return false
}

// IsJunk reports rebuildable clutter (virtualenvs, node_modules, .git…),
// skipped silently.
func IsJunk(name string) bool { return matchAny(junk, name) }

// Excluded reports whether a file name matches one of the never-pack rules.
func (a Adapter) Excluded(name string) bool {
	return matchAny(a.Never, name) && !matchAny(a.Allow, name)
}

// Load returns the named adapter: a user file in extraDir wins over built-ins.
func Load(name, extraDir string) (Adapter, error) {
	all, err := All(extraDir)
	if err != nil {
		return Adapter{}, err
	}
	for _, a := range all {
		if a.Name == name {
			return a, nil
		}
	}
	var names []string
	for _, a := range all {
		names = append(names, a.Name)
	}
	return Adapter{}, fmt.Errorf("no adapter for %q (known: %s)", name, strings.Join(names, ", "))
}

// All lists every known adapter, sorted by name.
func All(extraDir string) ([]Adapter, error) {
	byName := map[string]Adapter{}
	ents, _ := builtin.ReadDir("builtin")
	for _, e := range ents {
		b, _ := builtin.ReadFile("builtin/" + e.Name())
		var a Adapter
		if err := json.Unmarshal(b, &a); err != nil {
			return nil, fmt.Errorf("built-in adapter %s: %w", e.Name(), err)
		}
		byName[a.Name] = a
	}
	if extraDir != "" {
		files, _ := filepath.Glob(filepath.Join(extraDir, "*.json"))
		for _, f := range files {
			b, err := os.ReadFile(f)
			if err != nil {
				return nil, err
			}
			var a Adapter
			if err := json.Unmarshal(b, &a); err != nil || a.Name == "" {
				return nil, fmt.Errorf("adapter %s is not valid", f)
			}
			byName[a.Name] = a
		}
	}
	var out []Adapter
	for _, a := range byName {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}
