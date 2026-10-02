// e2e runs tote's whole journey on whatever machine it's on — Windows, Linux
// or macOS — using a made-up AI folder (never real data):
//
//	sender: prepare → write START-HERE/DONE/quiz → send (folder mailbox)
//	guest:  open on a blank "computer" → private install of the AI if missing
//	        → quiz → send-home → leave → the blank home must be empty again
//
//	go run ./tools/e2e --tote ./tote
package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

var (
	tote    = flag.String("tote", "", "path to the tote program to test")
	install = flag.Bool("install-ai", true, "let guest mode install a private Claude Code if missing (needs network)")
	failed  = 0
)

func step(name string) { fmt.Printf("\n=== %s\n", name) }

func check(ok bool, what string, detail ...any) {
	if ok {
		fmt.Printf("PASS  %s\n", what)
		return
	}
	failed++
	fmt.Printf("FAIL  %s %v\n", what, detail)
}

func must(err error) {
	if err != nil {
		fmt.Println("FATAL", err)
		os.Exit(1)
	}
}

// run tote with a clean environment that points home at home.
func run(home string, extra []string, stdin string, args ...string) (string, error) {
	cmd := exec.Command(*tote, args...)
	env := []string{}
	for _, e := range os.Environ() {
		k := strings.ToUpper(strings.SplitN(e, "=", 2)[0])
		switch k {
		case "HOME", "USERPROFILE", "TOTE_CONFIG_DIR", "TOTE_GUEST_ROOT", "CLAUDE_CONFIG_DIR", "XDG_CONFIG_HOME", "APPDATA", "LOCALAPPDATA":
			continue
		}
		env = append(env, e)
	}
	env = append(env, "HOME="+home, "USERPROFILE="+home)
	if runtime.GOOS == "windows" {
		env = append(env, "APPDATA="+filepath.Join(home, "AppData", "Roaming"), "LOCALAPPDATA="+filepath.Join(home, "AppData", "Local"))
	}
	cmd.Env = append(env, extra...)
	cmd.Stdin = strings.NewReader(stdin)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	return out.String(), err
}

func write(p, s string) {
	must(os.MkdirAll(filepath.Dir(p), 0o755))
	must(os.WriteFile(p, []byte(s), 0o644))
}

func encode(p string) string {
	return regexp.MustCompile(`[^A-Za-z0-9]`).ReplaceAllString(filepath.ToSlash(p), "-")
}

func main() {
	flag.Parse()
	if *tote == "" {
		fmt.Println("usage: e2e --tote <path>")
		os.Exit(2)
	}
	abs, err := filepath.Abs(*tote)
	must(err)
	*tote = abs
	root, err := os.MkdirTemp("", "tote-e2e-")
	must(err)
	defer os.RemoveAll(root)
	sender, lab, drive := filepath.Join(root, "sender"), filepath.Join(root, "lab"), filepath.Join(root, "drive")
	must(os.MkdirAll(lab, 0o755))
	fmt.Printf("tote e2e on %s/%s\n", runtime.GOOS, runtime.GOARCH)

	// A made-up Claude folder on the sending computer.
	step("sender: prepare + send")
	mem := filepath.Join(sender, ".claude", "projects", encode(sender), "memory")
	write(filepath.Join(mem, "MEMORY.md"), "- [Project Zephyr](zephyr.md) — the demo project\n")
	write(filepath.Join(mem, "zephyr.md"), "Project Zephyr ships on Friday. Pilot city: Lisbon.\n")
	write(filepath.Join(sender, ".claude", "skills", "demo", "SKILL.md"), "---\nname: demo\n---\nA demo skill.\n")
	write(filepath.Join(sender, ".claude", "settings.local.json"), `{"never":"travels"}`)
	scfg := []string{"TOTE_CONFIG_DIR=" + filepath.Join(sender, "cfg")}
	out, err := run(sender, scfg, "", "mailbox", "use", "folder", drive)
	check(err == nil, "mailbox use folder", out)
	box := filepath.Join(sender, "outbox")
	out, err = run(sender, scfg, "", "prepare", "--tool", "claude", "--days", "30", "-o", box)
	check(err == nil && strings.Contains(out, "Prepared"), "prepare", out)
	_, err = os.Stat(filepath.Join(box, "tools", "claude", "settings.local.json"))
	check(os.IsNotExist(err), "private settings file never packed")
	write(filepath.Join(box, "START-HERE.md"), "Before doing anything, also read DONE.md.\n\nActive project: Zephyr. Details: tools/claude/projects/"+encode(sender)+"/memory/zephyr.md\n")
	write(filepath.Join(box, "DONE.md"), "Nothing irreversible.\n")
	write(filepath.Join(box, "quiz.json"), `{"questions":[
		{"q":"Which project is active?","answers":["Zephyr"],"source":"START-HERE.md"},
		{"q":"When does it ship?","answers":["Friday"],"source":"tools/claude/projects/x/memory/zephyr.md"},
		{"q":"Was anything irreversible done (yes or no)?","answers":["no"],"source":"DONE.md"}]}`)
	out, err = run(sender, scfg, "", "send", box, "--label", "sender")
	check(err == nil && strings.Contains(out, "Proof quiz locked in"), "send", out)
	ticket := regexp.MustCompile(`tote1_[A-Za-z0-9_-]+`).FindString(out)
	pin := ""
	if m := regexp.MustCompile(`PIN: (\d{6})`).FindStringSubmatch(out); m != nil {
		pin = m[1]
	}
	check(ticket != "" && pin != "", "ticket + PIN printed")

	// A blank computer.
	step("guest: open on a blank computer")
	extra := []string{"TOTE_PIN=" + pin}
	if !*install {
		extra = append(extra, "PATH="+filepath.Dir(*tote)) // no AI tools at all
	}
	out, err = run(lab, extra, "", "guest", ticket, "--yes")
	fmt.Println(indent(out))
	check(err == nil && strings.Contains(out, "Ready."), "guest set up")
	guests, _ := filepath.Glob(filepath.Join(lab, ".tote-guest", "2*"))
	check(len(guests) == 1, "one private guest folder", guests)
	if len(guests) == 1 {
		g := guests[0]
		_, err = os.Stat(filepath.Join(g, "box", "AGENTS.md"))
		check(err == nil, "universal AGENTS.md inside the box")
		_, err = os.Stat(filepath.Join(g, "homes", "claude", "projects", encode(lab), "memory", "zephyr.md"))
		check(err == nil, "memory placed under this computer's project name", encode(lab))
		outside := listOutside(lab)
		check(len(outside) == 0, "nothing written outside the guest folder", outside)

		if *install {
			out, err = run(lab, nil, "", "run", "claude", "--version")
			check(err == nil && regexp.MustCompile(`\d+\.\d+\.\d+`).MatchString(out), "private Claude Code runs", out)
		}

		step("guest: quiz")
		ans := filepath.Join(root, "answers.txt")
		write(ans, "1. Zephyr\n2. It ships on Friday\n3. No\n")
		out, err = run(lab, nil, "", "quiz", "--box", filepath.Join(g, "box"), "--answers", ans)
		check(err == nil && strings.Contains(out, "Context received: 3/3"), "quiz graded by tote", out)

		step("guest: send-home")
		write(filepath.Join(g, "homes", "claude", "projects", encode(lab), "memory", "zephyr.md"), "Project Zephyr ships on Friday. Pilot city: Lisbon.\nLab: moved to Monday.\n")
		home := filepath.Join(g, "home-out")
		out, err = run(lab, nil, "", "send-home", "--label", "lab", "-o", home)
		check(err == nil && strings.Contains(out, "1 changed"), "send-home collects the change", out)
		_, err = os.Stat(filepath.Join(home, "tools", "claude", "projects", encode(sender), "memory", "zephyr.md"))
		check(err == nil, "renamed back to the sender's project name")
		_, err = os.Stat(filepath.Join(home, "tools", "claude", "CLAUDE.md"))
		check(os.IsNotExist(err), "tote's own note doesn't go home")
	}

	step("guest: leave")
	out, err = run(lab, nil, "", "leave")
	check(err == nil, "leave", out)
	left := listAll(lab)
	check(len(left) == 0, "blank computer is blank again", left)

	fmt.Printf("\n%s/%s: %d failure(s)\n", runtime.GOOS, runtime.GOARCH, failed)
	if failed > 0 {
		os.Exit(1)
	}
}

func listAll(dir string) []string { return list(dir, func(string) bool { return true }) }

func listOutside(dir string) []string {
	return list(dir, func(p string) bool { return !strings.HasPrefix(p, ".tote-guest") })
}

func list(dir string, keep func(string) bool) []string {
	var out []string
	filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err == nil && p != dir {
			rel, _ := filepath.Rel(dir, p)
			if rel = filepath.ToSlash(rel); keep(rel) {
				out = append(out, rel)
			}
		}
		return nil
	})
	if len(out) > 15 {
		out = append(out[:15], fmt.Sprintf("…%d more", len(out)-15))
	}
	return out
}

func indent(s string) string {
	return "    " + strings.ReplaceAll(strings.TrimSpace(s), "\n", "\n    ")
}
