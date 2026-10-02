package digest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCodexDigest(t *testing.T) {
	dir := t.TempDir()
	log := strings.Join([]string{
		`{"timestamp":"2026-10-01T10:00:00Z","type":"session_meta","payload":{"id":"abc-1","cwd":"/Users/me/dev/x","base_instructions":"HUGE SYSTEM PROMPT"}}`,
		`{"timestamp":"2026-10-01T10:00:01Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"<environment_context>\n<cwd>/x</cwd>\n</environment_context>"},{"type":"input_text","text":"<recommended_plugins>\n- Dropbox\n</recommended_plugins>"}]}}`,
		`{"timestamp":"2026-10-01T10:00:02Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"Shrink the PDF under 2 MB"}]}}`,
		`{"timestamp":"2026-10-01T10:00:03Z","type":"response_item","payload":{"type":"reasoning","summary":[],"encrypted_content":"SECRET-REASONING"}}`,
		`{"timestamp":"2026-10-01T10:00:04Z","type":"response_item","payload":{"type":"custom_tool_call","name":"exec","input":"pdfinfo slides.pdf"}}`,
		`{"timestamp":"2026-10-01T10:00:05Z","type":"response_item","payload":{"type":"custom_tool_call_output","output":"TOOL-OUTPUT-GONE"}}`,
		`{"timestamp":"2026-10-01T10:00:06Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Done: 1.8 MB."}]}}`,
	}, "\n")
	p := filepath.Join(dir, "rollout-x.jsonl")
	os.WriteFile(p, []byte(log), 0o600)
	c, err := Codex(p, map[string]string{"abc-1": "Shrink slides"})
	if err != nil {
		t.Fatal(err)
	}
	if c.Title != "Shrink slides" || c.Cwd != "/Users/me/dev/x" || c.Turns != 1 {
		t.Fatalf("%+v", c)
	}
	for _, gone := range []string{"Dropbox", "HUGE SYSTEM", "SECRET-REASONING", "TOOL-OUTPUT-GONE", "<cwd>"} {
		if strings.Contains(c.Markdown, gone) {
			t.Errorf("digest still has %q", gone)
		}
	}
	for _, kept := range []string{"Shrink the PDF under 2 MB", "exec: pdfinfo slides.pdf", "Done: 1.8 MB."} {
		if !strings.Contains(c.Markdown, kept) {
			t.Errorf("digest lost %q", kept)
		}
	}
}
