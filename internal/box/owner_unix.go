//go:build unix

package box

import (
	"io/fs"
	"os"
	"syscall"
)

// OwnedByMe reports whether the current user owns the file. tote only packs
// the sender's own files, never other people's on a shared computer.
func OwnedByMe(info fs.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return false
	}
	return int(st.Uid) == os.Getuid()
}
