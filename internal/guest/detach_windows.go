//go:build windows

package guest

import (
	"os"
	"os/exec"
	"syscall"
)

const detachedProcess = 0x00000008

func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: detachedProcess | syscall.CREATE_NEW_PROCESS_GROUP, HideWindow: true}
}

// removeLater deletes path after this process exits: Windows can't delete a
// running .exe, so a detached cmd waits two seconds and removes the folder.
func removeLater(path string) error {
	if err := os.RemoveAll(path); err == nil {
		return nil
	}
	cmd := exec.Command("cmd", "/c", "ping -n 3 127.0.0.1 >nul & rmdir /s /q \""+path+"\"")
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}
