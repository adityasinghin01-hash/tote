package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/adityasinghin01-hash/tote/internal/adapter"
	"github.com/adityasinghin01-hash/tote/internal/box"
	"github.com/adityasinghin01-hash/tote/internal/config"
	"github.com/adityasinghin01-hash/tote/internal/core"
	"github.com/adityasinghin01-hash/tote/internal/guest"
	"github.com/adityasinghin01-hash/tote/internal/lock"
	"github.com/adityasinghin01-hash/tote/internal/mailbox"
)

func adapters() (map[string]adapter.Adapter, []adapter.Adapter) {
	cdir, _ := config.Dir()
	all, _ := adapter.All(filepath.Join(cdir, "adapters"))
	m := map[string]adapter.Adapter{}
	for _, a := range all {
		m[a.Name] = a
	}
	return m, all
}

func asker(yes bool) guest.Asker {
	interactive := !yes && term.IsTerminal(int(os.Stdin.Fd()))
	in := bufio.NewReader(os.Stdin)
	return func(q string, def bool) bool {
		hint := "[Y/n]"
		if !def {
			hint = "[y/N]"
		}
		if !interactive {
			fmt.Printf("%s %s %s\n", q, hint, map[bool]string{true: "yes (default)", false: "no (default)"}[def])
			return def
		}
		for {
			fmt.Printf("%s %s ", q, hint)
			line, err := in.ReadString('\n')
			a := strings.ToLower(strings.TrimSpace(line))
			switch {
			case a == "" || err != nil:
				return def
			case a == "y" || a == "yes" || a == "haan" || a == "ha":
				return true
			case a == "n" || a == "no" || a == "nahi":
				return false
			}
		}
	}
}

func cmdGuest(args []string) error {
	fs := flag.NewFlagSet("guest", flag.ContinueOnError)
	pin := fs.String("pin", "", "the 6-digit PIN (asked for if not given)")
	yes := fs.Bool("yes", false, "accept the default answer to every question")
	hours := fs.Float64("hours", guest.DefaultTTL.Hours(), "wipe everything after this many hours")
	thenRun := fs.Bool("then-run", false, "offer to start the AI as soon as it's set up")
	src, err := parse(fs, args)
	if err != nil {
		return err
	}
	byName, all := adapters()
	if n := guest.ReapExpired(byName); n > 0 {
		fmt.Printf("(Wiped %d expired guest folder(s) from earlier.)\n", n)
	}

	// 1. Look, don't touch.
	fmt.Println("Checking this computer (only looking, nothing is changed)…")
	sc := guest.Run(all)
	fmt.Printf("  System: %s/%s", sc.OS, sc.Arch)
	if sc.FreeBytes > 0 {
		fmt.Printf(", %s free", human(int64(sc.FreeBytes)))
	}
	fmt.Println()
	for _, t := range sc.Tools {
		state := "not installed"
		if t.Path != "" {
			state = "installed " + t.Version
		}
		owner := ""
		if t.OwnerHomeUsed {
			owner = " — someone already uses it here; their files won't be touched"
		}
		fmt.Printf("  %s: %s%s\n", t.Adapter.Display, state, owner)
	}

	// 2. Get the box into a holding folder under the guest area.
	base, err := guest.Roots()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(base, 0o700); err != nil {
		return err
	}
	incoming, err := os.MkdirTemp(base, "incoming-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(incoming)
	boxDir := filepath.Join(incoming, "box")
	m, t, err := fetchBox(src, *pin, boxDir)
	if err != nil {
		return err
	}
	fmt.Printf("\nBox from %s: %d files, %s, tools: %s\n\n", orDefault(m.Source.HostLabel, "the other computer"), len(m.Files), human(m.TotalSize()), strings.Join(m.Tools, ", "))
	if len(m.Tools) == 0 {
		m.Tools = toolsInBox(boxDir)
	}
	// Tool descriptions an AI wrote and packed (for tools tote doesn't know).
	var fromBox []adapter.Adapter
	files, _ := filepath.Glob(filepath.Join(boxDir, "adapters", "*.json"))
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		a, err := adapter.Parse(b)
		if _, known := byName[a.Name]; err != nil || known {
			continue
		}
		a.FromBox = true
		byName[a.Name] = a
		fromBox = append(fromBox, a)
	}
	if len(fromBox) > 0 {
		extra := guest.Run(fromBox)
		sc.Tools = append(sc.Tools, extra.Tools...)
	}
	if sc.FreeBytes > 0 && sc.FreeBytes < uint64(m.TotalSize())*2+500<<20 {
		return fmt.Errorf("not enough free disk space here (%s free)", human(int64(sc.FreeBytes)))
	}

	// 3. Ask only what matters.
	say := func(s string) { fmt.Println(s) }
	choices, err := guest.Decide(sc, m.Tools, asker(*yes), say)
	if err != nil {
		return err
	}
	st, err := guest.Setup(boxDir, m, choices, byName, time.Duration(*hours*float64(time.Hour)), say)
	if err != nil {
		return err
	}
	if t != nil {
		if err := removeTicket(t); err != nil {
			fmt.Printf("(Couldn't delete the box from the mailbox: %v — it expires on its own.)\n", err)
		}
	}
	self, _ := os.Executable()
	reaper := guest.StartReaper(self, st.Root) == nil

	fmt.Printf("\nReady. Everything of yours is in one private folder:\n  %s\n", st.Root)
	for _, c := range st.Tools {
		if ts, ok := sc.Find(c.Tool); ok && ts.OwnerInstrFile != "" {
			fmt.Printf("\nHeads-up: %s also reads the owner's %s (a known %s behaviour). It won't change it, but its rules may mix with yours.\n",
				byName[c.Tool].Display, ts.OwnerInstrFile, byName[c.Tool].Display)
		}
	}
	fmt.Printf("\nIt wipes itself at %s", st.Expires.Local().Format("2 Jan 15:04"))
	if !reaper {
		fmt.Print(" (next time tote runs)")
	}
	me := selfName()
	fmt.Printf(", or sooner with:  %s leave\n", me)
	fmt.Printf("\nAny other AI: open a terminal in  %s\n  and start it there (it reads AGENTS.md), or tell it: Read %s and follow it.\n",
		st.WorkDir(), filepath.Join(st.Box, "START-HERE.md"))
	if len(st.Tools) == 0 {
		return nil
	}
	tool := st.Tools[0].Tool
	const prompt = "Get up to speed from the tote box, then tell me where we stopped."
	interactive := !*yes && term.IsTerminal(int(os.Stdin.Fd()))
	if *thenRun && interactive && asker(false)(fmt.Sprintf("Start %s now?", byName[tool].Display), true) {
		fmt.Printf("\nWhen it opens, log in if asked, then paste:\n\n  %s\n\nWhen you're done, just exit it (/exit) — tote will offer to wipe everything.\n\n", prompt)
		code, err := runTool([]string{tool})
		if err != nil {
			return err
		}
		afterRun(me, code)
		return nil
	}
	fmt.Printf("\nStart your AI:  %s run %s\n", me, tool)
	fmt.Printf("Then paste:     %s\n", prompt)
	return nil
}

// selfName is how to call tote again: plain "tote" if it's installed
// normally, the full path if the one-line installer put it in the guest area.
func selfName() string {
	exe, err := os.Executable()
	if err != nil {
		return "tote"
	}
	base, _ := guest.Roots()
	if base != "" && strings.HasPrefix(exe, base) {
		if strings.ContainsRune(exe, ' ') {
			return `"` + exe + `"`
		}
		return exe
	}
	return "tote"
}

func fetchBox(src, pin, dest string) (box.Manifest, *mailbox.Ticket, error) {
	if strings.HasPrefix(src, "tote1_") {
		t, err := mailbox.ParseTicket(src)
		if err != nil {
			return box.Manifest{}, nil, err
		}
		if pin == "" {
			pin = os.Getenv("TOTE_PIN")
		}
		if pin == "" {
			fmt.Print("PIN (6 digits, sent to you separately): ")
			line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
			pin = strings.TrimSpace(line)
		}
		if !lock.ValidPIN(pin) {
			return box.Manifest{}, nil, errors.New("the PIN is 6 digits")
		}
		rc, _, err := mailbox.Fetch(context.Background(), t)
		if err != nil {
			return box.Manifest{}, nil, err
		}
		defer rc.Close()
		m, err := core.OpenReader(rc, t.Key, pin, dest)
		if errors.Is(err, lock.ErrWrongCode) {
			return m, nil, errors.New("wrong PIN")
		}
		return m, &t, err
	}
	// A folder that's already unpacked (USB stick, `tote get` earlier).
	st, err := os.Stat(src)
	if err != nil || !st.IsDir() {
		return box.Manifest{}, nil, fmt.Errorf("%s is neither a tote ticket nor a box folder", src)
	}
	if err := os.CopyFS(dest, os.DirFS(src)); err != nil {
		return box.Manifest{}, nil, err
	}
	home, _ := os.UserHomeDir()
	m := box.Manifest{Source: box.Source{Home: guessSourceHome(dest, home)}, Tools: toolsInBox(dest)}
	filepath.WalkDir(dest, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if info, err := d.Info(); err == nil {
				m.Files = append(m.Files, box.File{Size: info.Size()})
			}
		}
		return nil
	})
	return m, nil, nil
}

// guessSourceHome reads the "sender's home was" line INDEX.md writes.
func guessSourceHome(boxDir, fallback string) string {
	b, err := os.ReadFile(filepath.Join(boxDir, "INDEX.md"))
	if err != nil {
		return fallback
	}
	const marker = "the sender's home was `"
	if i := strings.Index(string(b), marker); i >= 0 {
		rest := string(b)[i+len(marker):]
		if j := strings.Index(rest, "`"); j > 0 {
			return rest[:j]
		}
	}
	return fallback
}

func toolsInBox(boxDir string) []string {
	ents, _ := os.ReadDir(filepath.Join(boxDir, "tools"))
	var out []string
	for _, e := range ents {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	return out
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

func latestGuest() (guest.State, error) {
	sts, err := guest.List()
	if err != nil {
		return guest.State{}, err
	}
	if len(sts) == 0 {
		return guest.State{}, errors.New("no guest folder on this computer — start with: tote guest <ticket>")
	}
	return sts[0], nil
}

func cmdRun(args []string) error {
	code, err := runTool(args)
	if err != nil {
		return err
	}
	afterRun(selfName(), code)
	return nil
}

// afterRun: the moment the AI closes is when people forget to clean up, and
// "tote" isn't on PATH when the installer put it in the guest area — so ask.
func afterRun(me string, code int) {
	st, err := latestGuest()
	if err != nil {
		os.Exit(code)
	}
	fmt.Println()
	if term.IsTerminal(int(os.Stdin.Fd())) && asker(false)("Done here? Log out and wipe everything from this computer now?", true) {
		if err := cmdLeave(nil); err != nil {
			fmt.Fprintln(os.Stderr, "tote:", err)
			os.Exit(1)
		}
		os.Exit(code)
	}
	fmt.Printf("Kept. To continue later:  %s run\nTo wipe:                  %s leave   (or it wipes itself at %s)\n",
		me, me, st.Expires.Local().Format("2 Jan 15:04"))
	os.Exit(code)
}

// runTool starts the AI with its private settings and returns its exit code.
func runTool(args []string) (int, error) {
	byName, _ := adapters()
	guest.ReapExpired(byName)
	st, err := latestGuest()
	if err != nil {
		return 0, err
	}
	var c *guest.Choice
	for i := range st.Tools {
		if len(args) == 0 || st.Tools[i].Tool == args[0] {
			c = &st.Tools[i]
			break
		}
	}
	if c == nil {
		return 0, fmt.Errorf("no %s set up in the guest folder", strings.Join(args, " "))
	}
	a := byName[c.Tool]
	cmd := exec.Command(c.Binary, args[min(1, len(args)):]...)
	cmd.Env = guest.Env(a, *c)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if t := realTTY(); t != nil {
		defer t.Close()
		cmd.Stdin = t
	}
	err = cmd.Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode(), nil
	}
	return 0, err
}

func cmdLeave(args []string) error {
	fs := flag.NewFlagSet("leave", flag.ContinueOnError)
	all := fs.Bool("all", false, "remove every tote guest folder for this user")
	if err := fs.Parse(args); err != nil {
		return err
	}
	byName, _ := adapters()
	sts, err := guest.List()
	if err != nil {
		return err
	}
	if len(sts) == 0 {
		fmt.Println("Nothing to remove — no tote guest folder here.")
		return nil
	}
	if !*all {
		sts = sts[:1]
	}
	for _, st := range sts {
		n, err := guest.Leave(st, byName)
		if err != nil {
			return err
		}
		fmt.Printf("Logged out and removed %d files (%s). Nothing of yours is left in it.\n", n, st.Root)
	}
	return nil
}

func cmdStatus() error {
	byName, _ := adapters()
	guest.ReapExpired(byName)
	sts, err := guest.List()
	if err != nil {
		return err
	}
	if len(sts) == 0 {
		fmt.Println("No tote guest folder on this computer.")
		return nil
	}
	for _, st := range sts {
		var tools []string
		for _, c := range st.Tools {
			how := "using the installed program"
			if c.Install {
				how = "private copy"
			}
			tools = append(tools, fmt.Sprintf("%s (%s)", c.Tool, how))
		}
		fmt.Printf("%s\n  tools: %s\n  wipes itself: %s\n", st.Root, strings.Join(tools, ", "), st.Expires.Local().Format("2 Jan 15:04"))
	}
	return nil
}

func cmdReap(args []string) error {
	if len(args) != 1 {
		return errors.New("usage: tote __reap <guest folder>")
	}
	byName, _ := adapters()
	guest.Reap(args[0], byName, 30*time.Second)
	return nil
}

func removeTicket(t *mailbox.Ticket) error { return mailbox.Remove(context.Background(), *t) }
