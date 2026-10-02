package digest

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Codex wrapper blocks injected into "user" turns that aren't the person.
var codexNoise = regexp.MustCompile(`(?s)<(environment_context|recommended_plugins|user_instructions|permissions instructions|turn_aborted|skills_instructions)>.*?</(environment_context|recommended_plugins|user_instructions|permissions instructions|turn_aborted|skills_instructions)>`)

type codexLine struct {
	Timestamp time.Time       `json:"timestamp"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
}

type codexPayload struct {
	Type      string          `json:"type"`
	Role      string          `json:"role"`
	ID        string          `json:"id"`
	Cwd       string          `json:"cwd"`
	Name      string          `json:"name"`
	Input     string          `json:"input"`
	Arguments string          `json:"arguments"`
	Content   json.RawMessage `json:"content"`
}

// CodexTitles reads ~/.codex/session_index.jsonl (id → thread name).
func CodexTitles(codexHome string) map[string]string {
	out := map[string]string{}
	f, err := os.Open(filepath.Join(codexHome, "session_index.jsonl"))
	if err != nil {
		return out
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var e struct {
			ID   string `json:"id"`
			Name string `json:"thread_name"`
		}
		if json.Unmarshal(sc.Bytes(), &e) == nil && e.Name != "" {
			out[e.ID] = e.Name
		}
	}
	return out
}

// Codex reads one Codex CLI rollout log (sessions/YYYY/MM/DD/rollout-*.jsonl).
func Codex(path string, titles map[string]string) (Chat, error) {
	f, err := os.Open(path)
	if err != nil {
		return Chat{}, err
	}
	defer f.Close()
	var c Chat
	var b strings.Builder
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
	lastRole := ""
	for sc.Scan() {
		var l codexLine
		if json.Unmarshal(sc.Bytes(), &l) != nil {
			continue
		}
		var p codexPayload
		if json.Unmarshal(l.Payload, &p) != nil {
			continue
		}
		if l.Type == "session_meta" {
			c.ID, c.Cwd = p.ID, p.Cwd
			continue
		}
		if l.Type != "response_item" {
			continue
		}
		stamp := func() {
			if l.Timestamp.IsZero() {
				return
			}
			if c.Start.IsZero() || l.Timestamp.Before(c.Start) {
				c.Start = l.Timestamp
			}
			if l.Timestamp.After(c.End) {
				c.End = l.Timestamp
			}
		}
		switch p.Type {
		case "message":
			var parts []struct {
				Text string `json:"text"`
			}
			json.Unmarshal(p.Content, &parts)
			var texts []string
			for _, pt := range parts {
				texts = append(texts, pt.Text)
			}
			t := strings.TrimSpace(strings.Join(texts, "\n"))
			switch p.Role {
			case "user":
				t = strings.TrimSpace(codexNoise.ReplaceAllString(t, ""))
				if t == "" || strings.HasPrefix(t, "# AGENTS.md instructions") || strings.HasPrefix(t, "## AGENTS.md instructions") {
					continue
				}
				stamp()
				c.Turns++
				b.WriteString("\n**You:** " + clip(t, maxUser) + "\n")
				lastRole = "user"
			case "assistant":
				if t == "" {
					continue
				}
				stamp()
				if lastRole != "assistant" {
					b.WriteString("\n**AI:** ")
				} else {
					b.WriteString("\n")
				}
				b.WriteString(clip(t, maxAssistant) + "\n")
				lastRole = "assistant"
			}
		case "function_call", "custom_tool_call", "local_shell_call":
			in := p.Input
			if in == "" {
				in = p.Arguments
			}
			line := p.Name
			if s := oneLine(in); s != "" {
				line += ": " + clip(s, 140)
			}
			b.WriteString("- " + line + "\n")
		}
	}
	if err := sc.Err(); err != nil {
		return c, err
	}
	if c.Title = titles[c.ID]; c.Title == "" {
		c.Title = "(untitled chat)"
	}
	c.Markdown = header(c) + b.String()
	return c, nil
}
