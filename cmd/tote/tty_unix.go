//go:build unix

package main

import (
	"os"
	"os/exec"
	"strings"
	"syscall"

	"golang.org/x/term"
)

// realTTY: when the one-line installer hands us the keyboard as the /dev/tty
// alias, macOS's kqueue can't watch it and programs like Claude Code (Bun)
// crash with "EINVAL ... kqueue". Reopen the terminal's real device instead.
func realTTY() *os.File {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return nil
	}
	var in, alias syscall.Stat_t
	if syscall.Fstat(int(os.Stdin.Fd()), &in) != nil || syscall.Stat("/dev/tty", &alias) != nil || in.Rdev != alias.Rdev {
		return nil // stdin is already a real device
	}
	for _, f := range []*os.File{os.Stderr, os.Stdout} {
		cmd := exec.Command("tty")
		cmd.Stdin = f
		out, err := cmd.Output()
		name := strings.TrimSpace(string(out))
		if err != nil || !strings.HasPrefix(name, "/dev/") || name == "/dev/tty" {
			continue
		}
		if t, err := os.OpenFile(name, os.O_RDWR, 0); err == nil {
			return t
		}
	}
	return nil
}
