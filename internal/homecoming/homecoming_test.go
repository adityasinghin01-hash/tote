package homecoming

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/adityasinghin01-hash/tote/internal/adapter"
	"github.com/adityasinghin01-hash/tote/internal/box"
	"github.com/adityasinghin01-hash/tote/internal/guest"
	"github.com/adityasinghin01-hash/tote/internal/sent"
)

func put(t *testing.T, p, s string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("missing %s", p)
	}
	return string(b)
}

// setHome points the home folder at dir on every OS (Windows reads
// USERPROFILE, not HOME).
func setHome(t *testing.T, dir string) {
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
}

func hash(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// The full trip: home sends memory away, both sides edit, work comes home.
func TestRoundTripKeepsEveryEdit(t *testing.T) {
	adapters := map[string]adapter.Adapter{}
	all, _ := adapter.All("")
	for _, a := range all {
		adapters[a.Name] = a
	}
	home, lab := t.TempDir(), t.TempDir()
	t.Setenv("TOTE_CONFIG_DIR", filepath.Join(home, "cfg"))
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	encHome, encLab := guest.EncodeProject(home), guest.EncodeProject(lab)
	homeMem := filepath.Join(home, ".claude", "projects", encHome, "memory")

	// What home sent (a box as it arrives) + home's record of it.
	boxDir := filepath.Join(t.TempDir(), "box")
	files := map[string]string{
		"tools/claude/projects/" + encHome + "/memory/MEMORY.md": "- [a](a.md)\n",
		"tools/claude/projects/" + encHome + "/memory/a.md":      "a original\n",
		"tools/claude/projects/" + encHome + "/memory/b.md":      "b original\n",
	}
	m := box.Manifest{ID: "box123", Tools: []string{"claude"}, Source: box.Source{Home: home, HostLabel: "mac"}}
	for p, s := range files {
		put(t, filepath.Join(boxDir, filepath.FromSlash(p)), s)
		put(t, filepath.Join(home, ".claude", strings.TrimPrefix(filepath.FromSlash(p), filepath.FromSlash("tools/claude/"))), s)
		m.Files = append(m.Files, box.File{Path: p, SHA256: hash(s)})
	}
	if err := sent.Save(m); err != nil {
		t.Fatal(err)
	}

	// Guest side.
	setHome(t, lab)
	t.Setenv("TOTE_GUEST_ROOT", "")
	st, err := guest.Setup(boxDir, m, []guest.Choice{{Tool: "claude", Binary: "/bin/true"}}, adapters, time.Hour, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	priv := filepath.Join(st.Tools[0].Home, "projects", encLab)
	put(t, filepath.Join(priv, "memory", "a.md"), "a original\nLAB EDIT\n")
	put(t, filepath.Join(priv, "memory", "c.md"), "c from lab\n")
	put(t, filepath.Join(priv, "memory", "MEMORY.md"), "- [a](a.md)\n- lab line\n")
	put(t, filepath.Join(priv, "lab-1.jsonl"),
		`{"type":"user","sessionId":"lab-1","cwd":"/lab","timestamp":"2026-10-02T18:00:00Z","message":{"role":"user","content":"fix printer"}}`+"\n"+
			`{"type":"assistant","sessionId":"lab-1","cwd":"/lab","timestamp":"2026-10-02T18:01:00Z","message":{"role":"assistant","content":[{"type":"text","text":"Fixed."}]}}`+"\n")
	out := filepath.Join(t.TempDir(), "out")
	rep, err := SendHome(st, adapters, out, "lab")
	if err != nil {
		t.Fatal(err)
	}
	if rep.Changed["claude"] != 2 || rep.Added["claude"] != 1 || rep.Chats != 1 {
		t.Fatalf("send-home collected %+v (CLAUDE.md pointer must not count)", rep)
	}
	if _, err := os.Stat(filepath.Join(out, "tools", "claude", "CLAUDE.md")); err == nil {
		t.Fatal("tote's 'you were just moved' note was sent home")
	}
	if _, err := os.Stat(filepath.Join(out, "tools", "claude", "projects", encHome, "memory", "c.md")); err != nil {
		t.Fatal("project folder not renamed back to home's name")
	}

	// Meanwhile at home.
	setHome(t, home)
	put(t, filepath.Join(homeMem, "MEMORY.md"), "- [a](a.md)\n- mac line\n")
	put(t, filepath.Join(homeMem, "b.md"), "b original\nMAC EDIT\n")

	p, err := PlanMerge(out, adapters, filepath.Join(home, "tote-inbox"))
	if err != nil {
		t.Fatal(err)
	}
	if !p.Known || p.Count("add") != 1 || p.Count("update") != 1 || p.Count("conflict") != 1 || p.Count("chat") != 1 {
		t.Fatalf("plan: known=%v add=%d update=%d conflict=%d chat=%d", p.Known, p.Count("add"), p.Count("update"), p.Count("conflict"), p.Count("chat"))
	}
	if err := Apply(p); err != nil {
		t.Fatal(err)
	}
	if read(t, filepath.Join(homeMem, "a.md")) != "a original\nLAB EDIT\n" {
		t.Error("lab-only edit not applied")
	}
	if read(t, filepath.Join(p.Inbox, "backup", "claude", "projects", encHome, "memory", "a.md")) != "a original\n" {
		t.Error("home copy not backed up before update")
	}
	if read(t, filepath.Join(homeMem, "b.md")) != "b original\nMAC EDIT\n" {
		t.Error("home's own edit was lost")
	}
	if read(t, filepath.Join(homeMem, "MEMORY.md")) != "- [a](a.md)\n- mac line\n" ||
		read(t, filepath.Join(homeMem, "MEMORY.from-lab.md")) != "- [a](a.md)\n- lab line\n" {
		t.Error("both-sides change not kept side by side")
	}
	if read(t, filepath.Join(homeMem, "c.md")) != "c from lab\n" {
		t.Error("new file not added")
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", "projects", encHome, "lab-1.jsonl")); err != nil {
		t.Error("lab chat not resumable at home")
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", "CLAUDE.md")); err == nil {
		t.Error("CLAUDE.md appeared at home")
	}
	if !strings.Contains(read(t, filepath.Join(p.Inbox, "MERGE-REPORT.md")), "MEMORY.from-lab.md") {
		t.Error("report doesn't list the file to combine")
	}
}

func TestUnknownOriginKeepsBoth(t *testing.T) {
	t.Setenv("TOTE_CONFIG_DIR", t.TempDir())
	home := t.TempDir()
	setHome(t, home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	put(t, filepath.Join(home, ".claude", "notes.md"), "home")
	out := t.TempDir()
	put(t, filepath.Join(out, ReturnName), `{"origin":"never-sent-here","from":"lab"}`)
	put(t, filepath.Join(out, "tools", "claude", "notes.md"), "lab")
	all, _ := adapter.All("")
	adapters := map[string]adapter.Adapter{}
	for _, a := range all {
		adapters[a.Name] = a
	}
	p, err := PlanMerge(out, adapters, filepath.Join(home, "inbox"))
	if err != nil {
		t.Fatal(err)
	}
	if p.Known || p.Count("conflict") != 1 || p.Count("update") != 0 {
		t.Fatalf("without a sent record nothing may be overwritten: %+v", p.Actions)
	}
}

func TestStripPointer(t *testing.T) {
	in := "My rules.\n\n<!-- added by tote -->\n## You were just moved…"
	if string(StripPointer([]byte(in))) != "My rules." {
		t.Fatalf("%q", StripPointer([]byte(in)))
	}
}
