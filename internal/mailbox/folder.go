package mailbox

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"time"
)

// Folder drops boxes into a folder: a USB stick, a network share, or a
// Google Drive / Dropbox / OneDrive folder that syncs to the other computer.
type Folder struct {
	Dir string `json:"dir"`
}

func (f Folder) Kind() string { return "folder" }

func (f Folder) Drop(ctx context.Context, boxPath string, ttl time.Duration) (Ticket, error) {
	if err := os.MkdirAll(f.Dir, 0o700); err != nil {
		return Ticket{}, err
	}
	id := make([]byte, 6)
	rand.Read(id)
	dst := filepath.Join(f.Dir, "tote-"+hex.EncodeToString(id)+".tote")
	if err := copyFile(boxPath, dst); err != nil {
		return Ticket{}, err
	}
	abs, err := filepath.Abs(dst)
	if err != nil {
		return Ticket{}, err
	}
	u := fileURL(abs)
	return Ticket{V: 1, Get: u, Del: u}, nil // a folder can't enforce expiry
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".part"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err = io.Copy(out, in); err == nil {
		err = out.Sync()
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst)
}
