//go:build unix

package guest

import "golang.org/x/sys/unix"

func freeBytes(dir string) uint64 {
	var st unix.Statfs_t
	if unix.Statfs(dir, &st) != nil {
		return 0
	}
	return uint64(st.Bavail) * uint64(st.Bsize)
}
