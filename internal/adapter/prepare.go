package adapter

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/adityasinghin01-hash/tote/internal/box"
	"github.com/adityasinghin01-hash/tote/internal/digest"
	"github.com/adityasinghin01-hash/tote/internal/scrub"
)

type PrepareOptions struct {
	Out       string // staging folder to create (must not exist or be empty)
	Days      int    // chats active in the last N days get digests; 0 = none
	Raw       bool   // also copy the raw chat logs (big) for native resume
	HostLabel string // shown in INDEX.md
	Now       func() time.Time
}

type PrepareReport struct {
	Out         string
	Carried     map[string]int // "what" → files copied
	NotYours    []string
	Excluded    []string
	Chats       []digest.Chat
	ChatsByTool map[string][]digest.Chat
	Redacted    map[string]int
	Bytes       int64 // total bytes in staging
	FirstRead   int64 // bytes in the files read up front (estimate tokens as /4)
}

// Prepare is PrepareMany for one tool.
func Prepare(a Adapter, opt PrepareOptions) (PrepareReport, error) {
	return PrepareMany([]Adapter{a}, opt)
}

// PrepareMany copies each tool's carry-worthy files into opt.Out, squeezes
// recent chats into digests, blanks secrets, and writes INDEX.md and TASK.md.
// The sending AI then writes START-HERE.md, DONE.md and quiz.json — and, for
// a tool tote doesn't know (Generic), packs that tool's files itself.
func PrepareMany(as []Adapter, opt PrepareOptions) (PrepareReport, error) {
	rep := PrepareReport{Out: opt.Out, Carried: map[string]int{}, Redacted: map[string]int{}, ChatsByTool: map[string][]digest.Chat{}}
	if opt.Now == nil {
		opt.Now = time.Now
	}
	if ents, err := os.ReadDir(opt.Out); err == nil && len(ents) > 0 {
		return rep, fmt.Errorf("%s already has files in it — pick an empty folder", opt.Out)
	}
	if err := os.MkdirAll(opt.Out, 0o700); err != nil {
		return rep, err
	}
	homes := map[string]string{}
	for _, a := range as {
		if a.Generic {
			os.MkdirAll(filepath.Join(opt.Out, "tools", a.Name), 0o700)
			continue
		}
		home, err := a.HomeDir()
		if err != nil {
			return rep, err
		}
		if _, err := os.Stat(home); err != nil {
			return rep, fmt.Errorf("%s folder not found at %s", a.Display, home)
		}
		homes[a.Name] = home
		if err := collect(a, home, opt, &rep); err != nil {
			return rep, err
		}
	}
	if err := ScrubFolder(opt.Out, rep.Redacted); err != nil {
		return rep, err
	}

	idx := index(as, homes, opt, &rep)
	if err := writeFile(filepath.Join(opt.Out, "INDEX.md"), []byte(idx)); err != nil {
		return rep, err
	}
	if err := writeFile(filepath.Join(opt.Out, "TASK.md"), []byte(task(as, opt))); err != nil {
		return rep, err
	}
	filepath.WalkDir(opt.Out, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if st, err := d.Info(); err == nil {
				rep.Bytes += st.Size()
			}
		}
		return nil
	})
	rep.FirstRead = int64(len(idx))
	for _, a := range as {
		if mi := memoryIndex(opt.Out, filepath.Join(opt.Out, "tools", a.Name)); mi != "" {
			if st, err := os.Stat(filepath.Join(opt.Out, mi)); err == nil {
				rep.FirstRead += st.Size()
			}
		}
	}
	return rep, nil
}

// collect copies one known tool's files and digests its recent chats.
func collect(a Adapter, home string, opt PrepareOptions, rep *PrepareReport) error {
	toolDir := filepath.Join(opt.Out, "tools", a.Name)
	for _, c := range a.Carry {
		matches, _ := filepath.Glob(filepath.Join(home, filepath.FromSlash(c.Path)))
		for _, m := range matches {
			rel, _ := filepath.Rel(home, m)
			n, err := copyTree(m, filepath.Join(toolDir, rel), rel, a, rep)
			if err != nil {
				return err
			}
			rep.Carried[a.Display+" "+c.What] += n
		}
	}
	if opt.Days <= 0 || (a.Chats.Format != "claude-jsonl" && a.Chats.Format != "codex-jsonl") {
		return nil
	}
	var titles map[string]string
	if a.Chats.Format == "codex-jsonl" {
		titles = digest.CodexTitles(home)
	}
	cutoff := opt.Now().Add(-time.Duration(opt.Days) * 24 * time.Hour)
	logs, _ := filepath.Glob(filepath.Join(home, filepath.FromSlash(a.Chats.Glob)))
	var chats []digest.Chat
	for _, l := range logs {
		st, err := os.Stat(l)
		if err != nil || st.ModTime().Before(cutoff) || !box.OwnedByMe(st) {
			continue
		}
		var c digest.Chat
		if a.Chats.Format == "codex-jsonl" {
			c, err = digest.Codex(l, titles)
		} else {
			c, err = digest.Claude(l)
		}
		if err != nil || c.Turns == 0 {
			continue
		}
		chats = append(chats, c)
		if err := writeFile(filepath.Join(opt.Out, digestPath(a.Name, c)), []byte(c.Markdown)); err != nil {
			return err
		}
		if opt.Raw {
			rel, _ := filepath.Rel(home, l)
			if _, err := copyTree(l, filepath.Join(opt.Out, "raw", a.Name, rel), rel, a, rep); err != nil {
				return err
			}
		}
	}
	digest.SortNewestFirst(chats)
	rep.ChatsByTool[a.Name] = chats
	rep.Chats = append(rep.Chats, chats...)
	return nil
}

// ScrubFolder blanks secrets in a prepared folder: real key formats
// everywhere; password-style guesses only in notes and chats (code must keep
// working). Safe to run twice — tote send runs it again to cover files an AI
// copied in after prepare.
func ScrubFolder(root string, counts map[string]int) error {
	return scrubTree(root, counts, func(rel string) scrub.Mode {
		if strings.HasPrefix(rel, "digests/") || strings.HasPrefix(rel, "raw/") || strings.Contains(rel, "/memory/") ||
			rel == "START-HERE.md" || rel == "DONE.md" {
			return scrub.Full
		}
		return scrub.KeysOnly
	})
}

var slugRe = regexp.MustCompile(`[^a-z0-9]+`)

func digestPath(tool string, c digest.Chat) string {
	slug := strings.Trim(slugRe.ReplaceAllString(strings.ToLower(c.Title), "-"), "-")
	if len(slug) > 50 {
		slug = slug[:50]
	}
	id := c.ID
	if len(id) > 8 {
		id = id[:8]
	}
	return filepath.Join("digests", tool, c.End.Local().Format("2006-01-02")+"_"+slug+"_"+id+".md")
}

// copyTree copies src (file or folder) to dst, following shortcuts, leaving
// out other users' files and never-pack names. Returns files copied.
func copyTree(src, dst, rel string, a Adapter, rep *PrepareReport) (int, error) {
	info, err := os.Stat(src) // follows shortcuts
	if err != nil {
		return 0, nil // broken shortcut: nothing to carry
	}
	if IsJunk(filepath.Base(src)) {
		return 0, nil
	}
	if a.Excluded(filepath.Base(src)) {
		rep.Excluded = append(rep.Excluded, filepath.ToSlash(rel))
		return 0, nil
	}
	if !box.OwnedByMe(info) {
		rep.NotYours = append(rep.NotYours, filepath.ToSlash(rel))
		return 0, nil
	}
	if !info.IsDir() {
		if !info.Mode().IsRegular() {
			return 0, nil
		}
		return 1, copyFile(src, dst, info)
	}
	ents, err := os.ReadDir(src)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, e := range ents {
		k, err := copyTree(filepath.Join(src, e.Name()), filepath.Join(dst, e.Name()), filepath.Join(rel, e.Name()), a, rep)
		if err != nil {
			return n, err
		}
		n += k
	}
	return n, nil
}

func copyFile(src, dst string, info fs.FileInfo) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Chtimes(dst, info.ModTime(), info.ModTime())
}

func writeFile(p string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	return os.WriteFile(p, b, 0o600)
}

const maxScrub = 256 << 20

func scrubTree(root string, counts map[string]int, modeFor func(rel string) scrub.Mode) error {
	return filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		st, err := d.Info()
		if err != nil || st.Size() == 0 || st.Size() > maxScrub {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil || !scrub.IsText(b) {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		out, c := scrub.Text(b, modeFor(filepath.ToSlash(rel)))
		if len(c) == 0 {
			return nil
		}
		for k, v := range c {
			counts[k] += v
		}
		return os.WriteFile(p, out, st.Mode().Perm())
	})
}

// memoryIndex finds the main MEMORY.md inside the carried tool folder and
// returns its path relative to the staging root out.
func memoryIndex(out, toolDir string) string {
	ms, _ := filepath.Glob(filepath.Join(toolDir, "projects", "*", "memory", "MEMORY.md"))
	best, bestSize := "", int64(-1)
	for _, m := range ms {
		if st, err := os.Stat(m); err == nil && st.Size() > bestSize {
			best, bestSize = m, st.Size()
		}
	}
	if best == "" {
		return ""
	}
	rel, _ := filepath.Rel(out, best)
	return filepath.ToSlash(rel)
}

func tok(n int64) string {
	t := n / 4
	if t < 1000 {
		return fmt.Sprintf("~%d", t)
	}
	return fmt.Sprintf("~%.1fK", float64(t)/1000)
}

func dirStats(root string) (files int, bytes int64) {
	filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			files++
			if st, err := d.Info(); err == nil {
				bytes += st.Size()
			}
		}
		return nil
	})
	return
}

func index(as []Adapter, homes map[string]string, opt PrepareOptions, rep *PrepareReport) string {
	var b strings.Builder
	label := opt.HostLabel
	if label == "" {
		label = "the sending computer"
	}
	var names []string
	for _, a := range as {
		names = append(names, a.Display)
	}
	fmt.Fprintf(&b, "# INDEX — everything in this box\n\nPacked from %s (%s) on %s. Token counts are rough (characters ÷ 4).\nOpen files only when the work needs them.\n\n",
		label, strings.Join(names, ", "), opt.Now().Format("2 Jan 2006 15:04"))
	b.WriteString("## Read first\n\n")
	b.WriteString("- `START-HERE.md` — written by the sending AI: who, what's active, where it stopped\n")
	b.WriteString("- `DONE.md` — already done; **do not redo**\n")
	for _, a := range as {
		if mi := memoryIndex(opt.Out, filepath.Join(opt.Out, "tools", a.Name)); mi != "" {
			st, _ := os.Stat(filepath.Join(opt.Out, mi))
			fmt.Fprintf(&b, "- `%s` — %s's memory index, one line per memory (%s tokens)\n", mi, a.Display, tok(st.Size()))
		}
	}
	for _, a := range as {
		toolSection(&b, a, opt, rep)
	}
	if len(rep.Redacted) > 0 {
		var parts []string
		for _, k := range scrub.Kinds(rep.Redacted) {
			parts = append(parts, fmt.Sprintf("%d %s", rep.Redacted[k], k))
		}
		fmt.Fprintf(&b, "\n## Blanked secrets\n\n%s — shown as `[REDACTED:kind]`. Ask the user if you need one.\n", strings.Join(parts, ", "))
	}
	sort.Strings(rep.NotYours)
	return b.String()
}

func toolSection(b *strings.Builder, a Adapter, opt PrepareOptions, rep *PrepareReport) {
	toolDir := filepath.Join(opt.Out, "tools", a.Name)
	fmt.Fprintf(b, "\n# %s — `tools/%s/`\n", a.Display, a.Name)
	if a.Generic {
		fmt.Fprintf(b, "\ntote doesn't know %s's files; the sending AI copied what matters into `tools/%s/` and its chat summaries into `digests/%s/`. See `adapters/%s.json` if present.\n", a.Display, a.Name, a.Name, a.Name)
		return
	}
	mems, _ := filepath.Glob(filepath.Join(toolDir, "projects", "*", "memory"))
	if len(mems) > 0 {
		b.WriteString("\n## Memory — open a file when its topic comes up\n\n| Folder | Files | Size |\n|---|---|---|\n")
		for _, m := range mems {
			f, sz := dirStats(m)
			rel, _ := filepath.Rel(opt.Out, m)
			fmt.Fprintf(b, "| `%s/` | %d | %s tokens |\n", filepath.ToSlash(rel), f, tok(sz))
		}
		uh, _ := os.UserHomeDir()
		fmt.Fprintf(b, "\nMemory folder names are the original project folders with `/` turned into `-` (the sender's home was `%s`).\n", uh)
	}
	if ents, err := os.ReadDir(filepath.Join(toolDir, "skills")); err == nil && len(ents) > 0 {
		var names []string
		for _, e := range ents {
			if e.IsDir() {
				names = append(names, e.Name())
			}
		}
		fmt.Fprintf(b, "\n## Skills (%d) — in `tools/%s/skills/<name>/SKILL.md`\n\n%s\n", len(names), a.Name, strings.Join(names, ", "))
	}
	if chats := rep.ChatsByTool[a.Name]; len(chats) > 0 {
		fmt.Fprintf(b, "\n## Chats from the last %d days (%d) — newest first; each is a squeezed digest\n\n| Last active | Title | Folder | Digest | Size |\n|---|---|---|---|---|\n", opt.Days, len(chats))
		for _, c := range chats {
			fmt.Fprintf(b, "| %s | %s | `%s` | `%s` | %s tokens |\n", c.End.Local().Format("2 Jan 15:04"),
				strings.ReplaceAll(c.Title, "|", "/"), c.Cwd, filepath.ToSlash(digestPath(a.Name, c)), tok(int64(len(c.Markdown))))
		}
		if opt.Raw {
			fmt.Fprintf(b, "\nRaw chat logs (for native resume only, rarely worth reading) are under `raw/%s/`.\n", a.Name)
		}
	}
	var other []string
	for _, c := range a.Carry {
		if c.What == "memory" || c.What == "skills" {
			continue
		}
		if ms, _ := filepath.Glob(filepath.Join(toolDir, filepath.FromSlash(c.Path))); len(ms) > 0 {
			other = append(other, fmt.Sprintf("- `tools/%s/%s` — %s", a.Name, c.Path, c.What))
		}
	}
	if len(other) > 0 {
		b.WriteString("\n## Other carried files\n\n" + strings.Join(other, "\n") + "\n")
	}
}

func task(as []Adapter, opt PrepareOptions) string {
	var names []string
	var generic []Adapter
	for _, a := range as {
		names = append(names, a.Display)
		if a.Generic {
			generic = append(generic, a)
		}
	}
	var g strings.Builder
	for _, a := range generic {
		fmt.Fprintf(&g, `
## 0. Pack %[1]s yourself — tote doesn't know it yet

You are %[1]s, or are working with it. You know (or can find) where it keeps this
user's instructions, memory, skills, custom commands and settings on this computer.

1. Copy those files into `+"`tools/%[1]s/`"+`, keeping their folder layout. **Only files
   that belong to this user. Never** logins, tokens, API keys, `+"`.env`"+` files, caches,
   or other people's files.
2. If you can read its recent chats, write one summary per chat to
   `+"`digests/%[1]s/<date>_<topic>.md`"+`: what was asked, what was decided, what is
   still open. Leave out tool output.
3. Describe %[1]s for tote in `+"`adapters/%[1]s.json`"+` so next time this is automatic:

    {"name": "%[1]s", "display": "…", "command": "<program name>",
     "home": {"env": "<env var that relocates its folder, if any>", "default": "~/.<folder>"},
     "install": {"unix": "<official install command>", "windows": "<official install command>"},
     "installed_binary": {"unix": "<path the installer creates, relative to HOME>"},
     "instruction_file": "<file in its folder it reads on start, e.g. AGENTS.md>",
     "carry": [{"path": "<relative path or glob>", "what": "memory|skills|settings|…"}],
     "never": ["<file names that hold logins or keys>"],
     "private_env": {"<ENV_VAR>": "{home}"}}

   Only write what you are sure of; leave a field out rather than guess.
`, a.Name)
	}
	return fmt.Sprintf(`# TASK — finish packing this box

**For the AI on the sending computer.** tote has packed %[1]s into this
folder and squeezed recent chats into `+"`digests/`"+`. Write the files below so
the AI on the other computer is up to speed after reading about 10K tokens,
then send the box.

Use only what is in this folder and what you already know. Quote facts;
never invent. If unsure, say "unsure". Never write secrets.
%[3]s
## 1. Write `+"`START-HERE.md`"+` (under ~1,500 words)

Its very first line must be:
"Before doing anything, also read `+"`DONE.md`"+` (do not redo those) and skim `+"`INDEX.md`"+`."
(A test showed a fresh AI skips files that START-HERE doesn't tell it to read.)

1. **Who the user is** and how they want to be worked with — rules they insist on.
2. **Active work** — for each live project: folder path, current state, the very next step.
3. **Where we stopped** — the last thing being done in the most recent chat, and what was about to happen.
4. **Waiting on the user** — anything you asked them that has no answer yet.
5. **How to find more** — point to `+"`INDEX.md`"+`; name the 3–5 memory files or digests most worth opening first.

## 2. Write `+"`DONE.md`"+`

Things already done that must **not** be repeated: pushes, deploys, emails,
messages, purchases, deletions, migrations. One line each: what, where, when.
Write "Nothing irreversible." if there are none.

## 3. Write `+"`quiz.json`"+` — the proof the handoff worked

10 questions the receiving AI must answer from this box. tote locks the
answers (only fingerprints travel) and grades the other AI itself.

    {"questions": [
      {"q": "Which folder holds the active project?", "answers": ["~/dev/x"], "source": "START-HERE.md"},
      {"q": "What is the price per month?", "answers": ["1500", "₹1,500"], "source": "tools/<ai>/…/memory/pricing.md"}
    ]}

- Answers: short and exact — a name, number, path, date or yes/no (max 8 words). List spelling variants.
- Mix: ~4 from START-HERE.md, ~2 from DONE.md, ~4 that need opening a file START-HERE points to.
- `+"`source`"+` = the file in this box that holds the answer.
- Never put the answer inside the question. Ask what matters for continuing the work.

## 4. Send it

When the files exist, run this (or ask the user to):

    tote send "%[2]s"

Then show the user the ticket and the PIN, and remind them to send the PIN a
different way. (quiz.json and this file stay here; only the locked quiz is sent.)
`, strings.Join(names, ", "), opt.Out, g.String())
}
