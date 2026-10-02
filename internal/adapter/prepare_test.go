package adapter

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var plantedKey = "sk-ant-" + "api03-PLANTEDplantedPLANTED0123456789" // built at run time: no key-shaped text in source

func write(t *testing.T, p, s string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func fakeClaude(t *testing.T) string {
	home := t.TempDir()
	write(t, filepath.Join(home, "settings.json"), `{"model":"opus"}`)
	write(t, filepath.Join(home, "settings.local.json"), `{"permissions":{"allow":["Bash(secret-thing)"]}}`)
	write(t, filepath.Join(home, "CLAUDE.md"), "Be terse.")
	write(t, filepath.Join(home, "projects/-Users-me/memory/MEMORY.md"), "- [Project X](x.md) — the big one\n")
	write(t, filepath.Join(home, "projects/-Users-me/memory/x.md"), "Project X lives in ~/dev/x. DB_PASSWORD=Zx81kq0pLmN4vB7c\n")
	write(t, filepath.Join(home, "skills/demo/SKILL.md"), "---\nname: demo\n---\npassword: req.body.password\n")
	write(t, filepath.Join(home, "skills/demo/.env.example"), "API_KEY=\n")
	write(t, filepath.Join(home, "skills/demo/.env"), "API_KEY=realrealreal123\n")
	write(t, filepath.Join(home, "skills/demo/.venv/lib/junk.py"), "x=1")
	chat := strings.Join([]string{
		`{"type":"ai-title","aiTitle":"Build project X","sessionId":"abcd1234-0000"}`,
		`{"type":"user","sessionId":"abcd1234-0000","cwd":"/Users/me/dev/x","timestamp":"2026-10-01T10:00:00Z","message":{"role":"user","content":"set up the db, my key is ` + plantedKey + `"}}`,
		`{"type":"assistant","sessionId":"abcd1234-0000","cwd":"/Users/me/dev/x","timestamp":"2026-10-01T10:00:05Z","message":{"role":"assistant","content":[{"type":"thinking","thinking":"HIDDEN-REASONING"},{"type":"tool_use","name":"Bash","input":{"command":"cat .env","description":"Read env file"}}]}}`,
		`{"type":"user","sessionId":"abcd1234-0000","cwd":"/Users/me/dev/x","timestamp":"2026-10-01T10:00:06Z","message":{"role":"user","content":[{"type":"tool_result","content":"TOOL-OUTPUT-SHOULD-VANISH ` + plantedKey + `"}]}}`,
		`{"type":"assistant","sessionId":"abcd1234-0000","cwd":"/Users/me/dev/x","timestamp":"2026-10-01T10:00:09Z","message":{"role":"assistant","content":[{"type":"text","text":"Database is set up on port 5433."}]}}`,
		`{"type":"user","isSidechain":true,"sessionId":"abcd1234-0000","timestamp":"2026-10-01T10:00:10Z","message":{"role":"user","content":"SUBAGENT-NOISE"}}`,
	}, "\n")
	write(t, filepath.Join(home, "projects/-Users-me-dev-x/abcd1234-0000.jsonl"), chat)
	return home
}

func TestPrepareClaude(t *testing.T) {
	home := fakeClaude(t)
	t.Setenv("CLAUDE_CONFIG_DIR", home)
	a, err := Load("claude", "")
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "box")
	rep, err := Prepare(a, PrepareOptions{Out: out, Days: 7, HostLabel: "test"})
	if err != nil {
		t.Fatal(err)
	}

	read := func(rel string) string {
		b, err := os.ReadFile(filepath.Join(out, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("missing %s", rel)
		}
		return string(b)
	}
	exists := func(rel string) bool {
		_, err := os.Stat(filepath.Join(out, filepath.FromSlash(rel)))
		return err == nil
	}

	// carried word for word (code keeps working)
	if read("tools/claude/skills/demo/SKILL.md") != "---\nname: demo\n---\npassword: req.body.password\n" {
		t.Error("skill code was altered")
	}
	if !exists("tools/claude/skills/demo/.env.example") || !exists("tools/claude/CLAUDE.md") {
		t.Error("template or CLAUDE.md missing")
	}
	// never carried
	for _, p := range []string{"tools/claude/settings.local.json", "tools/claude/skills/demo/.env", "tools/claude/skills/demo/.venv"} {
		if exists(p) {
			t.Errorf("%s should not be packed", p)
		}
	}
	// memory: secret-looking value blanked in notes
	if m := read("tools/claude/projects/-Users-me/memory/x.md"); strings.Contains(m, "Zx81kq0pLmN4vB7c") || !strings.Contains(m, "~/dev/x") {
		t.Errorf("memory scrub wrong: %q", m)
	}
	// digest: words kept, tool output + reasoning + subagent gone, key blanked
	if len(rep.Chats) != 1 {
		t.Fatalf("chats = %d", len(rep.Chats))
	}
	ds, _ := filepath.Glob(filepath.Join(out, "digests/claude/*build-project-x_abcd1234.md"))
	if len(ds) != 1 {
		t.Fatal("digest file not named as expected")
	}
	d, _ := os.ReadFile(ds[0])
	for _, gone := range []string{"TOOL-OUTPUT-SHOULD-VANISH", "HIDDEN-REASONING", "SUBAGENT-NOISE", "PLANTEDplanted"} {
		if strings.Contains(string(d), gone) {
			t.Errorf("digest still contains %s", gone)
		}
	}
	for _, kept := range []string{"set up the db", "Bash: Read env file", "port 5433", "/Users/me/dev/x"} {
		if !strings.Contains(string(d), kept) {
			t.Errorf("digest lost %q", kept)
		}
	}
	idx := read("INDEX.md")
	for _, want := range []string{"Build project X", "MEMORY.md", "demo", "START-HERE.md"} {
		if !strings.Contains(idx, want) {
			t.Errorf("INDEX.md missing %q", want)
		}
	}
	if !strings.Contains(read("TASK.md"), "DONE.md") {
		t.Error("TASK.md incomplete")
	}
	if rep.Redacted["anthropic"] == 0 {
		t.Errorf("planted key not counted: %v", rep.Redacted)
	}

	// a second prepare into the same folder must refuse
	if _, err := Prepare(a, PrepareOptions{Out: out, Days: 7}); err == nil {
		t.Error("prepare overwrote a non-empty folder")
	}
}

func TestOldChatsLeftOut(t *testing.T) {
	home := fakeClaude(t)
	old := time.Now().Add(-30 * 24 * time.Hour)
	os.Chtimes(filepath.Join(home, "projects/-Users-me-dev-x/abcd1234-0000.jsonl"), old, old)
	t.Setenv("CLAUDE_CONFIG_DIR", home)
	a, _ := Load("claude", "")
	rep, err := Prepare(a, PrepareOptions{Out: filepath.Join(t.TempDir(), "b"), Days: 7})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Chats) != 0 {
		t.Fatal("30-day-old chat included with --days 7")
	}
}
