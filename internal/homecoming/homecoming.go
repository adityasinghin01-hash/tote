// Package homecoming sends the work done on a guest computer back home and
// merges it there without ever losing an edit made at home.
package homecoming

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/adityasinghin01-hash/tote/internal/adapter"
	"github.com/adityasinghin01-hash/tote/internal/box"
	"github.com/adityasinghin01-hash/tote/internal/digest"
	"github.com/adityasinghin01-hash/tote/internal/guest"
	"github.com/adityasinghin01-hash/tote/internal/sent"
)

// Return is RETURN.json: what a homecoming box came from.
type Return struct {
	Origin  string    `json:"origin"`   // ID of the box that went out
	From    string    `json:"from"`     // label of the guest computer
	LabHome string    `json:"lab_home"` // home folder over there
	Created time.Time `json:"created"`
}

const ReturnName = "RETURN.json"

type SendReport struct {
	Out     string
	Changed map[string]int // tool → files changed here
	Added   map[string]int // tool → files new here
	Chats   int
}

// SendHome collects what changed in the guest's private AI folders since the
// box arrived, plus every chat held here, into out (a folder for tote send).
// Project folders are renamed back to the home computer's names.
func SendHome(st guest.State, adapters map[string]adapter.Adapter, out, label string) (SendReport, error) {
	rep := SendReport{Out: out, Changed: map[string]int{}, Added: map[string]int{}}
	if ents, err := os.ReadDir(out); err == nil && len(ents) > 0 {
		return rep, fmt.Errorf("%s already has files in it — pick an empty folder", out)
	}
	myHome, _ := os.UserHomeDir()
	labEnc, homeEnc := guest.EncodeProject(myHome), guest.EncodeProject(st.SourceHome)
	toHome := func(a adapter.Adapter, rel string) string {
		if a.ProjectDirs == "" || st.SourceHome == "" || labEnc == homeEnc {
			return rel
		}
		parts := strings.SplitN(filepath.ToSlash(rel), "/", 3)
		if len(parts) >= 2 && parts[0] == a.ProjectDirs && (parts[1] == labEnc || strings.HasPrefix(parts[1], labEnc+"-")) {
			parts[1] = homeEnc + strings.TrimPrefix(parts[1], labEnc)
		}
		return filepath.FromSlash(strings.Join(parts, "/"))
	}

	var tools []string
	for _, c := range st.Tools {
		a, ok := adapters[c.Tool]
		if !ok {
			continue
		}
		tools = append(tools, a.Name)
		priv := filepath.Join(c.Home, a.FilesSubdir)
		arrived := filepath.Join(st.Box, "tools", a.Name)
		for _, cp := range a.Carry {
			matches, _ := filepath.Glob(filepath.Join(priv, filepath.FromSlash(cp.Path)))
			for _, m := range matches {
				err := filepath.WalkDir(m, func(p string, d fs.DirEntry, err error) error {
					if err != nil {
						return err
					}
					name := d.Name()
					if adapter.IsJunk(name) || a.Excluded(name) {
						if d.IsDir() {
							return filepath.SkipDir
						}
						return nil
					}
					if d.IsDir() || !d.Type().IsRegular() {
						return nil
					}
					rel, _ := filepath.Rel(priv, p)
					relHome := toHome(a, rel)
					was, errWas := os.ReadFile(filepath.Join(arrived, relHome))
					now, err := os.ReadFile(p)
					if err != nil {
						return err
					}
					if a.InstructionFile != "" && filepath.ToSlash(rel) == a.InstructionFile {
						// tote's own "you were just moved" note must never go home.
						now = StripPointer(now)
						if errWas != nil && len(bytes.TrimSpace(now)) == 0 {
							return nil
						}
					}
					if errWas == nil && bytes.Equal(was, now) {
						return nil // unchanged since it arrived
					}
					if errWas == nil {
						rep.Changed[a.Name]++
					} else {
						rep.Added[a.Name]++
					}
					return writeFile(filepath.Join(out, "tools", a.Name, relHome), now)
				})
				if err != nil {
					return rep, err
				}
			}
		}
		// Every chat in the private folder happened here: summarise it, and
		// carry the raw log so it can be resumed at home.
		if a.Chats.Format == "claude-jsonl" || a.Chats.Format == "codex-jsonl" {
			titles := map[string]string{}
			if a.Chats.Format == "codex-jsonl" {
				titles = digest.CodexTitles(priv)
			}
			logs, _ := filepath.Glob(filepath.Join(priv, filepath.FromSlash(a.Chats.Glob)))
			for _, l := range logs {
				var c digest.Chat
				var err error
				if a.Chats.Format == "codex-jsonl" {
					c, err = digest.Codex(l, titles)
				} else {
					c, err = digest.Claude(l)
				}
				if err != nil || c.Turns == 0 {
					continue
				}
				rep.Chats++
				name := c.End.Local().Format("2006-01-02") + "_" + slug(c.Title) + "_" + short(c.ID) + ".md"
				if err := writeFile(filepath.Join(out, "digests", a.Name, name), []byte(c.Markdown)); err != nil {
					return rep, err
				}
				rel, _ := filepath.Rel(priv, l)
				raw, err := os.ReadFile(l)
				if err != nil {
					return rep, err
				}
				if err := writeFile(filepath.Join(out, "raw", a.Name, toHome(a, rel)), raw); err != nil {
					return rep, err
				}
			}
		}
	}

	ret := Return{Origin: st.BoxID, From: label, LabHome: myHome, Created: time.Now().UTC()}
	rb, _ := json.MarshalIndent(ret, "", "  ")
	if err := writeFile(filepath.Join(out, ReturnName), rb); err != nil {
		return rep, err
	}
	if err := writeFile(filepath.Join(out, "TASK.md"), []byte(returnTask(out, tools))); err != nil {
		return rep, err
	}
	red := map[string]int{}
	return rep, adapter.ScrubFolder(out, red)
}

// PointerMarker starts the note guest mode appends to an AI's instruction file.
const PointerMarker = "<!-- added by tote -->"

// StripPointer removes guest mode's appended note from an instruction file.
func StripPointer(b []byte) []byte {
	if i := bytes.Index(b, []byte(PointerMarker)); i >= 0 {
		return bytes.TrimRight(b[:i], "\n")
	}
	return b
}

func returnTask(out string, tools []string) string {
	return fmt.Sprintf(`# TASK — send your work home

**For the AI on this guest computer.** tote collected what changed in
%s here since the box arrived (`+"`tools/`"+`), every chat held here
(`+"`digests/`"+`, `+"`raw/`"+`). Write two short files so the AI at home knows
what happened, then send it.

## 1. Write `+"`START-HERE.md`"+` (under ~500 words)

First line: "Before doing anything, also read `+"`DONE.md`"+` (do not redo those)."

- What was worked on here, and what is finished.
- What is half-done, and the exact next step.
- Anything changed in memory/settings here that home should know about.

## 2. Write `+"`DONE.md`"+`

Irreversible things done on THIS computer (pushes, deploys, messages, deletions):
what, where, when. "Nothing irreversible." if none.

## 3. Send it

    tote send "%s"

At home they run `+"`tote merge <ticket>`"+`: nothing at home is overwritten if it
was also changed there — both copies are kept for the AI to combine.
`, strings.Join(tools, ", "), out)
}

// Action is one file's fate during a merge.
type Action struct {
	Kind string // "add", "update", "same", "conflict", "chat"
	Tool string
	Rel  string // path inside the tool's folder
	Dst  string // where it goes (for a conflict: the side-by-side copy)
}

type Plan struct {
	Return  Return
	Known   bool // home sent the original box, so "changed only there" is detectable
	Actions []Action
	Unknown []string // tools in the box this computer has no description for
	Inbox   string
	boxDir  string
}

func (p Plan) Count(kind string) (n int) {
	for _, a := range p.Actions {
		if a.Kind == kind {
			n++
		}
	}
	return
}

var labelRe = regexp.MustCompile(`[^a-z0-9]+`)

// PlanMerge works out what merging an opened homecoming box would do. It
// changes nothing.
func PlanMerge(boxDir string, adapters map[string]adapter.Adapter, inboxRoot string) (Plan, error) {
	var p Plan
	p.boxDir = boxDir
	rb, err := os.ReadFile(filepath.Join(boxDir, ReturnName))
	if err != nil {
		return p, errors.New("this box isn't work sent home (no RETURN.json) — use tote get or tote guest for it")
	}
	if err := json.Unmarshal(rb, &p.Return); err != nil {
		return p, fmt.Errorf("RETURN.json unreadable: %w", err)
	}
	rec, known := sent.Load(p.Return.Origin)
	p.Known = known
	label := strings.Trim(labelRe.ReplaceAllString(strings.ToLower(p.Return.From), "-"), "-")
	if label == "" {
		label = "away"
	}
	p.Inbox = filepath.Join(inboxRoot, time.Now().Format("2006-01-02-1504")+"-"+label)

	ents, _ := os.ReadDir(filepath.Join(boxDir, "tools"))
	tools := map[string]bool{}
	for _, e := range ents {
		tools[e.Name()] = true
	}
	ents, _ = os.ReadDir(filepath.Join(boxDir, "raw"))
	for _, e := range ents {
		tools[e.Name()] = true
	}
	var names []string
	for t := range tools {
		names = append(names, t)
	}
	sort.Strings(names)
	for _, tool := range names {
		a, ok := adapters[tool]
		if !ok || a.Generic {
			p.Unknown = append(p.Unknown, tool)
			continue
		}
		home, err := a.HomeDir()
		if err != nil {
			return p, err
		}
		if st, err := os.Stat(home); err == nil && !box.OwnedByMe(st) {
			return p, fmt.Errorf("%s belongs to another user — tote only merges into your own folders", home)
		}
		walk := func(sub string, chat bool) error {
			root := filepath.Join(boxDir, sub, tool)
			return filepath.WalkDir(root, func(f string, d fs.DirEntry, err error) error {
				if err != nil || d.IsDir() {
					if os.IsNotExist(err) {
						return nil
					}
					return err
				}
				rel, _ := filepath.Rel(root, f)
				dst := filepath.Join(home, rel)
				act := Action{Tool: tool, Rel: filepath.ToSlash(rel), Dst: dst}
				cur, errCur := os.ReadFile(dst)
				incoming, err := os.ReadFile(f)
				if err != nil {
					return err
				}
				switch {
				case errCur != nil:
					act.Kind = "add"
					if chat {
						act.Kind = "chat"
					}
				case bytes.Equal(cur, incoming):
					act.Kind = "same"
				case chat:
					act.Kind = "same" // never touch a chat log that already exists
				case known && rec.Files["tools/"+tool+"/"+filepath.ToSlash(rel)] == sha(cur):
					act.Kind = "update" // home's copy is exactly what was sent: only changed over there
				default:
					act.Kind = "conflict" // changed on both sides (or we can't tell): keep both
					ext := filepath.Ext(dst)
					act.Dst = strings.TrimSuffix(dst, ext) + ".from-" + label + ext
				}
				p.Actions = append(p.Actions, act)
				return nil
			})
		}
		if err := walk("tools", false); err != nil {
			return p, err
		}
		if err := walk("raw", true); err != nil {
			return p, err
		}
	}
	return p, nil
}

// Apply carries out a plan. Updates back up the home copy into the inbox
// first; nothing is ever deleted.
func Apply(p Plan) error {
	if err := os.MkdirAll(p.Inbox, 0o700); err != nil {
		return err
	}
	for _, a := range p.Actions {
		src := filepath.Join(p.boxDir, "tools", a.Tool, filepath.FromSlash(a.Rel))
		if a.Kind == "chat" {
			src = filepath.Join(p.boxDir, "raw", a.Tool, filepath.FromSlash(a.Rel))
		}
		switch a.Kind {
		case "same":
			continue
		case "update":
			old, err := os.ReadFile(a.Dst)
			if err != nil {
				return err
			}
			if err := writeFile(filepath.Join(p.Inbox, "backup", a.Tool, filepath.FromSlash(a.Rel)), old); err != nil {
				return err
			}
		}
		b, err := os.ReadFile(src)
		if err != nil {
			return err
		}
		if err := writeFile(a.Dst, b); err != nil {
			return err
		}
	}
	// The note from over there, the chat summaries, and a report for the AI.
	for _, n := range []string{"START-HERE.md", "DONE.md", ReturnName} {
		if b, err := os.ReadFile(filepath.Join(p.boxDir, n)); err == nil {
			if err := writeFile(filepath.Join(p.Inbox, n), b); err != nil {
				return err
			}
		}
	}
	if d := filepath.Join(p.boxDir, "digests"); exists(d) {
		if err := os.CopyFS(filepath.Join(p.Inbox, "digests"), os.DirFS(d)); err != nil {
			return err
		}
	}
	for _, t := range p.Unknown {
		for _, sub := range []string{"tools", "raw"} {
			if d := filepath.Join(p.boxDir, sub, t); exists(d) {
				if err := os.CopyFS(filepath.Join(p.Inbox, sub, t), os.DirFS(d)); err != nil {
					return err
				}
			}
		}
	}
	return writeFile(filepath.Join(p.Inbox, "MERGE-REPORT.md"), []byte(report(p)))
}

func report(p Plan) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Work brought home from %s\n\nRead `START-HERE.md` here first (written by the AI over there), then `DONE.md`.\n\n", orDefault(p.Return.From, "the other computer"))
	fmt.Fprintf(&b, "- Added: %d files\n- Updated (changed only over there; home copy backed up in `backup/`): %d\n- New chats you can resume: %d\n- **Changed on both sides: %d** — both kept\n",
		p.Count("add"), p.Count("update"), p.Count("chat"), p.Count("conflict"))
	if n := p.Count("conflict"); n > 0 {
		b.WriteString("\n## To combine (ask your AI)\n\nEach pair: the home file and the `.from-…` copy next to it. Merge the copy's new parts into the home file, then delete the copy.\n\n")
		for _, a := range p.Actions {
			if a.Kind == "conflict" {
				fmt.Fprintf(&b, "- `%s`\n", a.Dst)
			}
		}
	}
	if len(p.Unknown) > 0 {
		fmt.Fprintf(&b, "\nNot merged (no description of these tools here): %s — their files are in this folder.\n", strings.Join(p.Unknown, ", "))
	}
	if !p.Known {
		b.WriteString("\nNote: this computer didn't send the original box, so every changed file was treated as changed on both sides.\n")
	}
	return b.String()
}

func sha(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func writeFile(p string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	tmp := p + ".tote-part"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, bytes.NewReader(b)); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

var slugRe = regexp.MustCompile(`[^a-z0-9]+`)

func slug(s string) string {
	s = strings.Trim(slugRe.ReplaceAllString(strings.ToLower(s), "-"), "-")
	if len(s) > 50 {
		s = s[:50]
	}
	return s
}

func short(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}
