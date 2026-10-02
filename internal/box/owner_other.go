//go:build !unix

package box

import "io/fs"

// OwnedByMe: on Windows other users' profiles are already unreadable to
// ordinary users, and tote only reads inside the sender's own folders, so
// ownership is enforced by the OS. (Checked against ACLs in guest mode.)
func OwnedByMe(info fs.FileInfo) bool { return true }
