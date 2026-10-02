package guest

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/adityasinghin01-hash/tote/internal/adapter"
	"github.com/adityasinghin01-hash/tote/internal/box"
)

// DefaultTTL is how long a guest folder lives if nobody runs `tote leave`.
const DefaultTTL = 24 * time.Hour

// Asker asks the person a yes/no question; def is the answer for Enter.
type Asker func(question string, def bool) bool

// Choice is what guest mode will do for one tool.
type Choice struct {
	Tool    string `json:"tool"`
	Binary  string `json:"binary"`  // program to launch
	Install bool   `json:"install"` // install a private copy first
	Home    string `json:"home"`    // the tool's private settings folder
}

// State is saved in <root>/state.json so later commands know what exists.
type State struct {
	Root    string    `json:"root"`
	Created time.Time `json:"created"`
	Expires time.Time `json:"expires"`
	Box     string    `json:"box"`
	Tools   []Choice  `json:"tools"`
	// BoxID and SourceHome let `tote send-home` say what this came from.
	BoxID      string `json:"box_id,omitempty"`
	SourceHome string `json:"source_home,omitempty"`
	FromLabel  string `json:"from_label,omitempty"`
}

// Decide turns a scan into choices, asking only when there's a real choice.
func Decide(sc Scan, tools []string, ask Asker, say func(string)) ([]Choice, error) {
	var out []Choice
	for _, name := range tools {
		t, ok := sc.Find(name)
		if !ok {
			say(fmt.Sprintf("• %s: tote doesn't know this tool yet — start it yourself inside the box folder shown below; it will find AGENTS.md there.", name))
			continue
		}
		disp := t.Adapter.Display
		if !t.Adapter.Verified && !t.Adapter.FromBox {
			say(fmt.Sprintf("• Note: tote's support for %s is written from its docs and not yet tested for real.", disp))
		}
		why := "isn't installed here"
		if t.Path != "" && !t.TooOld {
			v := t.Version
			if v == "" {
				v = "(unknown version)"
			}
			if ask(fmt.Sprintf("%s %s is already installed here. Use it? (your login and files stay separate from the owner's)", disp, v), true) {
				out = append(out, Choice{Tool: name, Binary: t.Path})
				continue
			}
			why = "— you chose not to use the installed one"
		} else if t.TooOld {
			why = fmt.Sprintf("here is version %s, too old to keep your login apart from the owner's", t.Version)
		}
		if t.Adapter.Install.Unix == "" && t.Adapter.Install.Windows == "" {
			say(fmt.Sprintf("• %s %s and tote can't install it — skipping.", disp, why))
			continue
		}
		q := fmt.Sprintf("%s %s. Install a private copy just for you (removed when you leave)?", disp, why)
		if t.Adapter.FromBox { // written by an AI, not by tote: show exactly what would run
			cmd := t.Adapter.Install.Unix
			if runtime.GOOS == "windows" {
				cmd = t.Adapter.Install.Windows
			}
			q = fmt.Sprintf("%s %s. The box says to install it with:\n    %s\n  Run that (only for you, removed when you leave)?", disp, why, cmd)
		}
		if ask(q, !t.Adapter.FromBox) {
			out = append(out, Choice{Tool: name, Install: true})
		} else {
			say(fmt.Sprintf("• Skipping %s.", disp))
		}
	}
	return out, nil
}

// Roots is where guest folders live: ~/.tote-guest (TOTE_GUEST_ROOT overrides).
func Roots() (string, error) {
	if d := os.Getenv("TOTE_GUEST_ROOT"); d != "" {
		return d, nil
	}
	h, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(h, ".tote-guest"), nil
}

// Setup creates the private folder, moves the opened box in, installs what
// was chosen, and places each tool's files in its private settings folder.
func Setup(boxDir string, m box.Manifest, choices []Choice, adapters map[string]adapter.Adapter, ttl time.Duration, say func(string)) (State, error) {
	base, err := Roots()
	if err != nil {
		return State{}, err
	}
	if err := os.MkdirAll(base, 0o700); err != nil {
		return State{}, err
	}
	id := make([]byte, 4)
	rand.Read(id)
	root := filepath.Join(base, time.Now().Format("20060102-1504")+"-"+hex.EncodeToString(id))
	if err := os.Mkdir(root, 0o700); err != nil {
		return State{}, err
	}
	st := State{Root: root, Created: time.Now().UTC(), Expires: time.Now().Add(ttl).UTC(), Box: filepath.Join(root, "box"),
		BoxID: m.ID, SourceHome: m.Source.Home, FromLabel: m.Source.HostLabel}
	ok := false
	defer func() {
		if !ok {
			os.RemoveAll(root)
		}
	}()
	if err := os.Rename(boxDir, st.Box); err != nil { // same disk in practice; fall back to copy
		if err := copyDir(boxDir, st.Box); err != nil {
			return st, err
		}
		os.RemoveAll(boxDir)
	}
	if err := writeState(st); err != nil { // early, so a failed setup is still reapable
		return st, err
	}
	if err := writeUniversal(st); err != nil {
		return st, err
	}

	for _, c := range choices {
		a := adapters[c.Tool]
		c.Home = filepath.Join(root, "homes", c.Tool)
		if err := os.MkdirAll(c.Home, 0o700); err != nil {
			return st, err
		}
		if c.Install {
			say(fmt.Sprintf("Installing a private %s (this can take a minute)…", a.Display))
			bin, err := install(a, filepath.Join(root, "install"))
			if err != nil {
				return st, fmt.Errorf("installing %s: %w", a.Display, err)
			}
			c.Binary = bin
		}
		if err := placeFiles(a, st.Box, c.Home, m.Source.Home); err != nil {
			return st, err
		}
		// Always, even if the box had nothing for this tool: e.g. Codex must
		// keep its login in its own folder, never the machine's Keychain.
		if err := applySetup(a, filepath.Join(c.Home, a.FilesSubdir)); err != nil {
			return st, err
		}
		st.Tools = append(st.Tools, c)
	}
	if err := writeState(st); err != nil {
		return st, err
	}
	ok = true
	return st, nil
}

// Pointer is the "you were just moved" note any AI gets.
func Pointer(boxRoot string) string {
	ptr := fmt.Sprintf("## You were just moved to a new computer\n\nBefore anything else, read `%s` and follow it, then `%s` (never redo those).\nThe full box is in `%s`; `INDEX.md` maps it.\n",
		filepath.Join(boxRoot, "START-HERE.md"), filepath.Join(boxRoot, "DONE.md"), boxRoot)
	if _, err := os.Stat(filepath.Join(boxRoot, "quiz.lock")); err == nil {
		self, _ := os.Executable()
		ptr += fmt.Sprintf("\nThen prove you're up to speed: run `%s quiz --box %s` to see the questions, answer them honestly from what you read (one per line, `1. answer`, into a file), and run `%s quiz --box %s --answers <file>`. Show the user the score.\n",
			self, boxRoot, self, boxRoot)
	}
	return ptr
}

// writeUniversal puts the pointer inside the box folder: start ANY AI there
// and it finds it — AGENTS.md is read by 25+ tools; CLAUDE.md and GEMINI.md
// cover the two big ones that use their own name. It must be inside the
// folder the AI starts in: AI tools refuse to read outside it (OpenCode
// auto-rejected a pointer that lived next door).
func writeUniversal(st State) error {
	work := st.WorkDir()
	body := []byte("<!-- written by tote -->\n" + Pointer(st.Box))
	for _, n := range []string{"AGENTS.md", "CLAUDE.md", "GEMINI.md"} {
		if err := os.WriteFile(filepath.Join(work, n), body, 0o600); err != nil {
			return err
		}
	}
	return nil
}

// WorkDir is where any AI can be started to find the pointer: the box.
func (st State) WorkDir() string { return st.Box }

// install runs the tool's official installer with HOME pointed inside the
// guest folder, so nothing lands in the owner's home.
func install(a adapter.Adapter, fakeHome string) (string, error) {
	if err := os.MkdirAll(fakeHome, 0o700); err != nil {
		return "", err
	}
	var cmd *exec.Cmd
	osKey := "unix"
	if runtime.GOOS == "windows" {
		osKey = "windows"
		if a.Install.Windows == "" {
			return "", errors.New("no Windows installer known")
		}
		cmd = exec.Command("powershell", "-NoProfile", "-ExecutionPolicy", "Bypass", "-Command", a.Install.Windows)
	} else {
		if a.Install.Unix == "" {
			return "", errors.New("no installer known for this system")
		}
		cmd = exec.Command("sh", "-c", a.Install.Unix)
	}
	cmd.Env = append(os.Environ(), "HOME="+fakeHome, "USERPROFILE="+fakeHome, "XDG_DATA_HOME="+filepath.Join(fakeHome, ".local", "share"),
		"XDG_CONFIG_HOME="+filepath.Join(fakeHome, ".config"), "XDG_CACHE_HOME="+filepath.Join(fakeHome, ".cache"))
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	if err := cmd.Run(); err != nil {
		return "", err
	}
	rel := a.InstalledBinary[osKey]
	if rel == "" {
		return "", errors.New("installer ran but tote doesn't know where it put the program")
	}
	bin := filepath.Join(fakeHome, filepath.FromSlash(rel))
	if _, err := os.Stat(bin); err != nil {
		return "", fmt.Errorf("installer finished but %s is missing", bin)
	}
	return bin, nil
}

var nonAlnum = regexp.MustCompile(`[^A-Za-z0-9]`)

// encodeProject is how Claude names a project folder: every non-letter/digit
// in the path becomes "-" (/Users/me/dev/x → -Users-me-dev-x).
func EncodeProject(p string) string { return nonAlnum.ReplaceAllString(filepath.ToSlash(p), "-") }

// placeFiles copies box/tools/<tool>/ into the private settings folder,
// renaming project folders from the sender's home to this computer's, and
// adds a pointer to START-HERE.md to the tool's instruction file.
func placeFiles(a adapter.Adapter, boxRoot, home, srcHome string) error {
	from := filepath.Join(boxRoot, "tools", a.Name)
	myHome, _ := os.UserHomeDir()
	oldPrefix, newPrefix := EncodeProject(srcHome), EncodeProject(myHome)
	home = filepath.Join(home, a.FilesSubdir)
	if err := os.MkdirAll(home, 0o700); err != nil {
		return err
	}
	if _, err := os.Stat(from); err == nil { // the box may have nothing for this tool
		if err := copyDir(from, home); err != nil {
			return err
		}
	}
	if a.ProjectDirs != "" && srcHome != "" && oldPrefix != newPrefix {
		pd := filepath.Join(home, a.ProjectDirs)
		ents, _ := os.ReadDir(pd)
		for _, e := range ents {
			if e.IsDir() && (e.Name() == oldPrefix || strings.HasPrefix(e.Name(), oldPrefix+"-")) {
				to := filepath.Join(pd, newPrefix+strings.TrimPrefix(e.Name(), oldPrefix))
				if _, err := os.Stat(to); err == nil {
					continue
				}
				if err := os.Rename(filepath.Join(pd, e.Name()), to); err != nil {
					return err
				}
			}
		}
	}
	if a.InstructionFile != "" {
		f := filepath.Join(home, a.InstructionFile)
		old, _ := os.ReadFile(f)
		ptr := "\n\n<!-- added by tote -->\n" + Pointer(boxRoot)
		if err := os.WriteFile(f, append(old, []byte(ptr)...), 0o600); err != nil {
			return err
		}
	}
	return nil
}

// applySetup forces guest-only config lines (e.g. Codex: keep the login in
// a file in its own folder, never the shared Keychain).
func applySetup(a adapter.Adapter, home string) error {
	for _, sl := range a.PrivateSetup {
		f := filepath.Join(home, filepath.FromSlash(sl.File))
		old, _ := os.ReadFile(f)
		var keep []string
		for _, line := range strings.Split(string(old), "\n") {
			if !strings.HasPrefix(strings.TrimSpace(line), sl.Key) {
				keep = append(keep, line)
			}
		}
		body := sl.Line + "\n" + strings.TrimLeft(strings.Join(keep, "\n"), "\n")
		if err := os.MkdirAll(filepath.Dir(f), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(f, []byte(body), 0o600); err != nil {
			return err
		}
	}
	return nil
}

// Env is what a tool needs to run privately.
func Env(a adapter.Adapter, c Choice) []string {
	env := os.Environ()
	set := func(k, v string) {
		pre := k + "="
		for i, e := range env {
			if strings.HasPrefix(e, pre) {
				env[i] = pre + v
				return
			}
		}
		env = append(env, pre+v)
	}
	if a.Home.Env != "" {
		set(a.Home.Env, c.Home)
	}
	if a.SecureEnv != "" {
		set(a.SecureEnv, c.Home) // own login slot, never the owner's
	}
	pk := make([]string, 0, len(a.PrivateEnv))
	for k := range a.PrivateEnv {
		pk = append(pk, k)
	}
	sort.Strings(pk)
	for _, k := range pk {
		set(k, strings.ReplaceAll(a.PrivateEnv[k], "{home}", c.Home))
	}
	keys := make([]string, 0, len(a.RunEnv))
	for k := range a.RunEnv {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		set(k, a.RunEnv[k])
	}
	return env
}

// Leave logs each tool out of its private slot, then deletes the folder.
func Leave(st State, adapters map[string]adapter.Adapter) (removed int, err error) {
	for _, c := range st.Tools {
		a, ok := adapters[c.Tool]
		if !ok || len(a.Logout) == 0 || c.Binary == "" {
			continue
		}
		cmd := exec.Command(c.Binary, a.Logout...)
		cmd.Env = Env(a, c)
		cmd.Stdin = nil
		cmd.Run() // not logged in is fine
	}
	filepath.WalkDir(st.Root, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			removed++
		}
		return nil
	})
	if err := os.RemoveAll(st.Root); err != nil {
		return removed, err
	}
	CleanBase()
	return removed, nil
}

// CleanBase removes the whole guest area — including the tote program the
// one-line installer put in bin/ — once no guest folders are left.
func CleanBase() {
	base, err := Roots()
	if err != nil {
		return
	}
	if sts, _ := List(); len(sts) > 0 {
		return
	}
	ents, _ := os.ReadDir(base)
	for _, e := range ents {
		if e.Name() != "bin" && e.Name() != "cfg" && !strings.HasPrefix(e.Name(), "incoming-") {
			return // something tote didn't make: leave it alone
		}
	}
	removeLater(base)
}

// List returns every guest folder on this computer for this user.
func List() ([]State, error) {
	base, err := Roots()
	if err != nil {
		return nil, err
	}
	ents, err := os.ReadDir(base)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []State
	for _, e := range ents {
		b, err := os.ReadFile(filepath.Join(base, e.Name(), "state.json"))
		if err != nil {
			continue
		}
		var st State
		if json.Unmarshal(b, &st) == nil && st.Root == filepath.Join(base, e.Name()) {
			out = append(out, st)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created.After(out[j].Created) })
	return out, nil
}

// ReapExpired wipes guest folders past their expiry. Every tote command
// calls it, so a forgotten folder dies the next time tote runs.
func ReapExpired(adapters map[string]adapter.Adapter) int {
	sts, _ := List()
	n := 0
	for _, st := range sts {
		if time.Now().After(st.Expires) {
			if _, err := Leave(st, adapters); err == nil {
				n++
			}
		}
	}
	return n
}

// StartReaper launches a background `tote __reap <root>` that wipes the
// folder at expiry even if tote is never run again (until a reboot).
func StartReaper(self, root string) error {
	cmd := exec.Command(self, "__reap", root)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

// Reap waits until the folder's expiry, then wipes it. Exits early if the
// folder disappears (someone ran `tote leave`).
func Reap(root string, adapters map[string]adapter.Adapter, poll time.Duration) {
	for {
		b, err := os.ReadFile(filepath.Join(root, "state.json"))
		if err != nil {
			return
		}
		var st State
		if json.Unmarshal(b, &st) != nil {
			return
		}
		if time.Now().After(st.Expires) {
			Leave(st, adapters)
			return
		}
		time.Sleep(min(poll, time.Until(st.Expires)+time.Second))
	}
}

func writeState(st State) error {
	b, _ := json.MarshalIndent(st, "", "  ")
	return os.WriteFile(filepath.Join(st.Root, "state.json"), b, 0o600)
}

func copyDir(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		t := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(t, 0o700)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		in, err := os.Open(p)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(t, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, in); err != nil {
			out.Close()
			return err
		}
		return out.Close()
	})
}
