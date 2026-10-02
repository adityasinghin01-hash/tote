//go:build unix

package guest

import (
	"os"
	"os/exec"
	"syscall"
)

// detach makes the reaper survive the terminal closing.
func detach(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} }

// removeLater deletes path; on Unix a running program can delete itself.
func removeLater(path string) error { return os.RemoveAll(path) }
