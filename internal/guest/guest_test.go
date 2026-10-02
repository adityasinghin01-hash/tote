//go:build unix

package guest

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/adityasinghin01-hash/tote/internal/adapter"
	"github.com/adityasinghin01-hash/tote/internal/box"
)

func put(t *testing.T, p, s string, mode os.FileMode) {
	t.Helper()
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(s), mode); err != nil {
		t.Fatal(err)
	}
}

// snap hashes every file and lists every folder under root, skipping skip.
func snap(t *testing.T, root, skip string) map[string]string {
	t.Helper()
	out := map[string]string{}
	filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if skip != "" && (p == skip || strings.HasPrefix(p, skip+string(filepath.Separator))) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		if d.IsDir() {
			out[rel] = "dir"
			return nil
		}
		b, _ := os.ReadFile(p)
		h := sha256.Sum256(b)
		info, _ := d.Info()
		out[rel] = hex.EncodeToString(h[:]) + info.Mode().String() + info.ModTime().String()
		return nil
	})
	return out
}

func sameSnap(t *testing.T, what string, a, b map[string]string) {
	t.Helper()
	for k, v := range a {
		if b[k] != v {
			t.Errorf("%s: %s changed or vanished", what, k)
		}
	}
	for k := range b {
		if _, ok := a[k]; !ok {
			t.Errorf("%s: new file appeared: %s", what, k)
		}
	}
}

type world struct {
	home, log string
	box       string
	adapters  map[string]adapter.Adapter
	all       []adapter.Adapter
}

// newWorld: a fake computer whose owner already uses Claude, plus a box from
// a sender whose home was /Users/sender.
func newWorld(t *testing.T, ownerHasClaude bool) world {
	w := world{home: t.TempDir(), log: filepath.Join(t.TempDir(), "calls.log")}
	t.Setenv("HOME", w.home)
	t.Setenv("TOTE_GUEST_ROOT", "")
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("CLAUDE_SECURESTORAGE_CONFIG_DIR", "")
	t.Setenv("TOTE_TEST_LOG", w.log)

	// the owner's stuff
	put(t, filepath.Join(w.home, ".claude/CLAUDE.md"), "OWNER RULES", 0o644)
	put(t, filepath.Join(w.home, ".claude/settings.json"), `{"model":"owner"}`, 0o644)
	put(t, filepath.Join(w.home, ".claude/projects/-owner/memory/MEMORY.md"), "owner memory", 0o644)
	put(t, filepath.Join(w.home, "Documents/private.txt"), "owner's private file", 0o600)
	put(t, filepath.Join(w.home, ".zshrc"), "export OWNER=1", 0o644)

	bin := filepath.Join(w.home, "bin")
	if ownerHasClaude {
		put(t, filepath.Join(bin, "claude"), `#!/bin/sh
case "$1" in
  --version) mkdir -p "$HOME/.config/litter"; echo "2.1.300 (Claude Code)";;
  auth) echo "logout cfg=$CLAUDE_CONFIG_DIR slot=$CLAUDE_SECURESTORAGE_CONFIG_DIR" >> "$TOTE_TEST_LOG";;
  *) echo "run cfg=$CLAUDE_CONFIG_DIR slot=$CLAUDE_SECURESTORAGE_CONFIG_DIR upd=$DISABLE_AUTOUPDATER" >> "$TOTE_TEST_LOG"
     cat "$CLAUDE_CONFIG_DIR/CLAUDE.md" >> "$TOTE_TEST_LOG";;
esac
`, 0o755)
	}
	t.Setenv("PATH", bin+":/usr/bin:/bin")

	// the box that arrived
	w.box = filepath.Join(t.TempDir(), "box")
	put(t, filepath.Join(w.box, "START-HERE.md"), "Before doing anything, read DONE.md.", 0o600)
	put(t, filepath.Join(w.box, "DONE.md"), "Nothing irreversible.", 0o600)
	put(t, filepath.Join(w.box, "tools/claude/CLAUDE.md"), "SENDER RULES", 0o600)
	put(t, filepath.Join(w.box, "tools/claude/projects/-Users-sender/memory/MEMORY.md"), "sender memory", 0o600)
	put(t, filepath.Join(w.box, "tools/claude/projects/-Users-sender-dev-x/memory/a.md"), "project x", 0o600)
	put(t, filepath.Join(w.box, "tools/claude/projects/-Users-other/memory/b.md"), "not under sender home", 0o600)
	put(t, filepath.Join(w.box, "tools/claude/skills/demo/SKILL.md"), "skill", 0o600)

	all, err := adapter.All("")
	if err != nil {
		t.Fatal(err)
	}
	w.all = all
	w.adapters = map[string]adapter.Adapter{}
	for _, a := range all {
		w.adapters[a.Name] = a
	}
	return w
}

func manifest() box.Manifest {
	return box.Manifest{Source: box.Source{Home: "/Users/sender"}, Tools: []string{"claude"}}
}

func yesToAll(string, bool) bool { return true }

func TestGuestLeavesOwnerUntouched(t *testing.T) {
	w := newWorld(t, true)
	guestArea := filepath.Join(w.home, ".tote-guest")
	before := snap(t, w.home, guestArea)

	sc := Run(w.all)
	ct, _ := sc.Find("claude")
	if ct.Version != "2.1.300" || !ct.OwnerHomeUsed || ct.OwnerInstrFile == "" || ct.TooOld {
		t.Fatalf("scan wrong: %+v", ct)
	}
	choices, _ := Decide(sc, []string{"claude"}, yesToAll, func(string) {})
	if len(choices) != 1 || choices[0].Install || choices[0].Binary == "" {
		t.Fatalf("should reuse the installed program: %+v", choices)
	}
	st, err := Setup(w.box, manifest(), choices, w.adapters, time.Hour, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	h := st.Tools[0].Home
	if !strings.HasPrefix(h, guestArea) {
		t.Fatalf("private home %s is outside the guest area", h)
	}

	// files placed, projects renamed to this computer's home
	enc := EncodeProject(w.home)
	for _, p := range []string{
		"projects/" + enc + "/memory/MEMORY.md",
		"projects/" + enc + "-dev-x/memory/a.md",
		"projects/-Users-other/memory/b.md",
		"skills/demo/SKILL.md",
	} {
		if _, err := os.Stat(filepath.Join(h, p)); err != nil {
			t.Errorf("missing %s", p)
		}
	}
	instr, _ := os.ReadFile(filepath.Join(h, "CLAUDE.md"))
	if !strings.HasPrefix(string(instr), "SENDER RULES") || !strings.Contains(string(instr), filepath.Join(st.Box, "START-HERE.md")) {
		t.Errorf("instruction file wrong: %q", instr)
	}

	if b, err := os.ReadFile(filepath.Join(st.Box, "AGENTS.md")); err != nil || !strings.Contains(string(b), "START-HERE.md") {
		t.Errorf("universal AGENTS.md missing from the box folder: %v", err)
	}

	// launch with private env
	cmd := exec.Command(st.Tools[0].Binary)
	cmd.Env = Env(w.adapters["claude"], st.Tools[0])
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	log, _ := os.ReadFile(w.log)
	want := "run cfg=" + h + " slot=" + h + " upd=1"
	if !strings.Contains(string(log), want) || !strings.Contains(string(log), "START-HERE.md") {
		t.Fatalf("tool didn't get private env: %s", log)
	}

	sameSnap(t, "while guest is active", before, snap(t, w.home, guestArea))

	// leave: logout in the private slot, then everything gone
	if _, err := Leave(st, w.adapters); err != nil {
		t.Fatal(err)
	}
	log, _ = os.ReadFile(w.log)
	if !strings.Contains(string(log), "logout cfg="+h+" slot="+h) {
		t.Errorf("logout not run in the private slot: %s", log)
	}
	if _, err := os.Stat(guestArea); !os.IsNotExist(err) {
		t.Error("guest area still exists after leave")
	}
	sameSnap(t, "after leave", before, snap(t, w.home, ""))
}

func TestPrivateInstallStaysInside(t *testing.T) {
	w := newWorld(t, false) // claude not installed
	a := w.adapters["claude"]
	a.Install.Unix = `mkdir -p "$HOME/.local/bin" && printf '#!/bin/sh\necho 9.9.9\n' > "$HOME/.local/bin/claude" && chmod +x "$HOME/.local/bin/claude"`
	w.adapters["claude"] = a
	for i := range w.all {
		if w.all[i].Name == "claude" {
			w.all[i] = a
		}
	}
	guestArea := filepath.Join(w.home, ".tote-guest")
	before := snap(t, w.home, guestArea)

	sc := Run(w.all)
	choices, _ := Decide(sc, []string{"claude"}, yesToAll, func(string) {})
	if len(choices) != 1 || !choices[0].Install {
		t.Fatalf("should install privately: %+v", choices)
	}
	st, err := Setup(w.box, manifest(), choices, w.adapters, time.Hour, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(st.Tools[0].Binary, st.Root) {
		t.Fatalf("installed program %s is outside the guest folder", st.Tools[0].Binary)
	}
	sameSnap(t, "after private install", before, snap(t, w.home, guestArea))
	Leave(st, w.adapters)
	sameSnap(t, "after leave", before, snap(t, w.home, ""))
}

func TestDeclineMeansSkip(t *testing.T) {
	w := newWorld(t, true)
	sc := Run(w.all)
	var asked []string
	choices, _ := Decide(sc, []string{"claude", "unknown-ai"}, func(q string, _ bool) bool { asked = append(asked, q); return false }, func(string) {})
	if len(choices) != 0 {
		t.Fatalf("declined everything but got %+v", choices)
	}
	if len(asked) != 2 { // use existing? → no; install private? → no; unknown tool: not asked
		t.Fatalf("questions asked: %v", asked)
	}
}

func TestTooOldOnMacIsNotReused(t *testing.T) {
	if older("2.1.143", "2.1.144") != true || older("2.1.144", "2.1.144") || older("2.2.0", "2.1.144") || !older("1.9.9", "2.0") {
		t.Fatal("version compare wrong")
	}
	sc := Scan{Tools: []ToolScan{{Adapter: adapter.Adapter{Name: "claude", Display: "Claude Code"}, Path: "/x/claude", Version: "2.0.1", TooOld: true}}}
	sc.Tools[0].Adapter.Install.Unix = "true"
	choices, _ := Decide(sc, []string{"claude"}, yesToAll, func(string) {})
	if len(choices) != 1 || !choices[0].Install {
		t.Fatalf("an old Claude on a Mac must not be reused: %+v", choices)
	}
}

func TestExpiredFolderIsReaped(t *testing.T) {
	w := newWorld(t, true)
	sc := Run(w.all)
	choices, _ := Decide(sc, []string{"claude"}, yesToAll, func(string) {})
	st, err := Setup(w.box, manifest(), choices, w.adapters, time.Millisecond, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	Reap(st.Root, w.adapters, time.Millisecond)
	if _, err := os.Stat(st.Root); !os.IsNotExist(err) {
		t.Fatal("reaper did not wipe an expired folder")
	}

	w2 := newWorld(t, true)
	sc = Run(w2.all)
	choices, _ = Decide(sc, []string{"claude"}, yesToAll, func(string) {})
	st, _ = Setup(w2.box, manifest(), choices, w2.adapters, time.Millisecond, func(string) {})
	time.Sleep(5 * time.Millisecond)
	if n := ReapExpired(w2.adapters); n != 1 {
		t.Fatalf("ReapExpired wiped %d", n)
	}
}

func TestEncodeProject(t *testing.T) {
	for in, want := range map[string]string{
		"/Users/sam":             "-Users-sam",
		"/Users/sam/dev/tote":    "-Users-sam-dev-tote",
		"/home/lab user/.claude": "-home-lab-user--claude",
		`C:\Users\lab`:           "C--Users-lab",
	} {
		if got := EncodeProject(in); got != want && !(strings.Contains(in, `\`) && got == EncodeProject(strings.ReplaceAll(in, `\`, "/"))) {
			t.Errorf("%s → %s, want %s", in, got, want)
		}
	}
}
