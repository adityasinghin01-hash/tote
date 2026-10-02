// Package guest runs an AI on someone else's computer without touching
// anything of theirs: one private folder holds the box, the AI's settings,
// its login slot and any private install, and `tote leave` deletes it all.
package guest

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/adityasinghin01-hash/tote/internal/adapter"
)

// ToolScan is what the terminal (not an AI) found out about one tool.
type ToolScan struct {
	Adapter        adapter.Adapter
	Path           string // program on PATH, "" if not installed
	Version        string
	TooOld         bool // can't keep its login apart from the owner's
	OwnerHomeUsed  bool // the owner already uses this tool here
	OwnerInstrFile string
}

type Scan struct {
	OS, Arch  string
	Home      string
	FreeBytes uint64 // free space where the guest folder will live; 0 = unknown
	Tools     []ToolScan
	HasGit    bool
}

var versionRe = regexp.MustCompile(`\d+\.\d+\.\d+`)

// Run looks at the machine. It only reads: PATH lookups, `--version`, and
// whether the tools' usual folders exist.
func Run(adapters []adapter.Adapter) Scan {
	home, _ := os.UserHomeDir()
	s := Scan{OS: runtime.GOOS, Arch: runtime.GOARCH, Home: home, FreeBytes: freeBytes(home)}
	_, err := exec.LookPath("git")
	s.HasGit = err == nil
	for _, a := range adapters {
		ts := ToolScan{Adapter: a}
		if a.Command != "" {
			if p, err := exec.LookPath(a.Command); err == nil {
				ts.Path = p
				ts.Version = version(p)
				if minV := a.MinVersion[runtime.GOOS]; minV != "" && ts.Version != "" && older(ts.Version, minV) {
					ts.TooOld = true
				}
			}
		}
		if a.Home.Default != "" {
			def := a.Home.Default
			if strings.HasPrefix(def, "~/") {
				def = filepath.Join(home, def[2:])
			}
			if st, err := os.Stat(def); err == nil && st.IsDir() {
				ts.OwnerHomeUsed = true
				if a.InstructionFile != "" {
					f := filepath.Join(def, a.InstructionFile)
					if _, err := os.Stat(f); err == nil {
						ts.OwnerInstrFile = f
					}
				}
			}
		}
		s.Tools = append(s.Tools, ts)
	}
	return s
}

// version asks a program its version inside a throwaway home: some tools
// (OpenCode) create their settings folders on any run, even --version, and
// the scan must not change anything of the owner's.
func version(bin string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	sandbox, err := os.MkdirTemp("", "tote-probe-")
	if err != nil {
		return ""
	}
	defer os.RemoveAll(sandbox)
	cmd := exec.CommandContext(ctx, bin, "--version")
	cmd.Dir = sandbox
	cmd.Env = append(os.Environ(), "DISABLE_AUTOUPDATER=1",
		"HOME="+sandbox, "USERPROFILE="+sandbox, "APPDATA="+sandbox, "LOCALAPPDATA="+sandbox,
		"XDG_CONFIG_HOME="+sandbox, "XDG_DATA_HOME="+sandbox, "XDG_STATE_HOME="+sandbox, "XDG_CACHE_HOME="+sandbox)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return versionRe.FindString(string(out))
}

// older reports a < b for dotted versions.
func older(a, b string) bool {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(pa) || i < len(pb); i++ {
		var x, y int
		if i < len(pa) {
			x, _ = strconv.Atoi(pa[i])
		}
		if i < len(pb) {
			y, _ = strconv.Atoi(pb[i])
		}
		if x != y {
			return x < y
		}
	}
	return false
}

// Find returns the scan for the named tool.
func (s Scan) Find(name string) (ToolScan, bool) {
	for _, t := range s.Tools {
		if t.Adapter.Name == name {
			return t, true
		}
	}
	return ToolScan{}, false
}
