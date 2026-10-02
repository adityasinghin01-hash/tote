// Package digest squeezes AI chat logs into short readable summaries: what
// you asked, what the AI said, and one line per action it took. Tool output
// (≈78% of a Claude log) and hidden reasoning are dropped.
package digest

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Chat is one squeezed conversation.
type Chat struct {
	ID       string
	Title    string
	Cwd      string
	Start    time.Time
	End      time.Time
	Turns    int // your messages
	Markdown string
}

const (
	maxUser      = 2000 // characters kept per message you wrote
	maxAssistant = 3000 // characters kept per AI reply
)

// Wrapper tags Claude Code injects into "user" turns that aren't the person.
var noise = regexp.MustCompile(`(?s)<(system-reminder|local-command-stdout|local-command-stderr|command-message|command-args|task-notification|bash-stdout|bash-stderr)>.*?</(system-reminder|local-command-stdout|local-command-stderr|command-message|command-args|task-notification|bash-stdout|bash-stderr)>`)
var cmdName = regexp.MustCompile(`<command-name>(.*?)</command-name>`)

type line struct {
	Type        string          `json:"type"`
	SessionID   string          `json:"sessionId"`
	Cwd         string          `json:"cwd"`
	Timestamp   time.Time       `json:"timestamp"`
	IsSidechain bool            `json:"isSidechain"`
	IsMeta      bool            `json:"isMeta"`
	AITitle     string          `json:"aiTitle"`
	Message     json.RawMessage `json:"message"`
}

type msg struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type part struct {
	Type  string          `json:"type"`
	Text  string          `json:"text"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

// Claude reads one Claude Code chat log (projects/<dir>/<id>.jsonl).
func Claude(path string) (Chat, error) {
	f, err := os.Open(path)
	if err != nil {
		return Chat{}, err
	}
	defer f.Close()
	var c Chat
	var b strings.Builder
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20) // tool results can be huge single lines
	lastRole := ""
	for sc.Scan() {
		var l line
		if json.Unmarshal(sc.Bytes(), &l) != nil {
			continue
		}
		if l.SessionID != "" && c.ID == "" {
			c.ID = l.SessionID
		}
		if l.Type == "ai-title" && l.AITitle != "" {
			c.Title = l.AITitle
		}
		if (l.Type != "user" && l.Type != "assistant") || l.IsSidechain || l.IsMeta {
			continue
		}
		if c.Cwd == "" {
			c.Cwd = l.Cwd
		}
		if !l.Timestamp.IsZero() {
			if c.Start.IsZero() || l.Timestamp.Before(c.Start) {
				c.Start = l.Timestamp
			}
			if l.Timestamp.After(c.End) {
				c.End = l.Timestamp
			}
		}
		var m msg
		if json.Unmarshal(l.Message, &m) != nil {
			continue
		}
		texts, actions := split(m.Content)
		if l.Type == "user" {
			t := cleanUser(strings.Join(texts, "\n"))
			if t == "" {
				continue
			}
			c.Turns++
			b.WriteString("\n**You:** " + clip(t, maxUser) + "\n")
			lastRole = "user"
			continue
		}
		for _, a := range actions {
			b.WriteString("- " + a + "\n")
		}
		if t := strings.TrimSpace(strings.Join(texts, "\n")); t != "" {
			if lastRole != "assistant" {
				b.WriteString("\n**AI:** ")
			} else {
				b.WriteString("\n")
			}
			b.WriteString(clip(t, maxAssistant) + "\n")
			lastRole = "assistant"
		}
	}
	if err := sc.Err(); err != nil {
		return c, err
	}
	if c.Title == "" {
		c.Title = "(untitled chat)"
	}
	c.Markdown = header(c) + b.String()
	return c, nil
}

func header(c Chat) string {
	return fmt.Sprintf("# %s\n\n- Chat: %s\n- Folder: %s\n- When: %s → %s\n- Your messages: %d\n",
		c.Title, c.ID, c.Cwd, c.Start.Local().Format("2006-01-02 15:04"), c.End.Local().Format("2006-01-02 15:04"), c.Turns)
}

// split returns the plain text parts and one-line descriptions of tool calls.
// Tool results, images and hidden reasoning are dropped.
func split(raw json.RawMessage) (texts, actions []string) {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return []string{s}, nil
	}
	var parts []part
	if json.Unmarshal(raw, &parts) != nil {
		return nil, nil
	}
	for _, p := range parts {
		switch p.Type {
		case "text":
			texts = append(texts, p.Text)
		case "tool_use":
			actions = append(actions, describe(p.Name, p.Input))
		}
	}
	return texts, actions
}

// describe turns a tool call into one short line, e.g. "Edit: internal/box/box.go".
func describe(name string, input json.RawMessage) string {
	var in map[string]any
	json.Unmarshal(input, &in)
	for _, k := range []string{"description", "file_path", "path", "pattern", "query", "url", "command", "prompt"} {
		if v, ok := in[k].(string); ok && v != "" {
			return name + ": " + clip(oneLine(v), 140)
		}
	}
	return name
}

func cleanUser(t string) string {
	if m := cmdName.FindStringSubmatch(t); m != nil { // a slash command
		t = cmdName.ReplaceAllString(t, "")
		t = strings.TrimSpace(noise.ReplaceAllString(t, ""))
		return strings.TrimSpace("(ran " + m[1] + ") " + t)
	}
	t = noise.ReplaceAllString(t, "")
	t = strings.TrimSpace(t)
	if strings.HasPrefix(t, "[Request interrupted") {
		return "(interrupted the AI)"
	}
	return t
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + fmt.Sprintf(" …[%d more characters cut]", len(r)-n)
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// SortNewestFirst orders chats by when they last had activity.
func SortNewestFirst(cs []Chat) {
	sort.Slice(cs, func(i, j int) bool { return cs[i].End.After(cs[j].End) })
}
