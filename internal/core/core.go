// Package core joins box (pack/open) and lock (seal/unseal) into whole-file
// operations the CLI and mailboxes use.
package core

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/adityasinghin01-hash/tote/internal/box"
	"github.com/adityasinghin01-hash/tote/internal/lock"
)

// Packed describes a freshly made box file.
type Packed struct {
	Path     string
	Key      string
	PIN      string
	Manifest box.Manifest
	Report   box.Report
	Bytes    int64
}

// PackFile packs srcDir into a locked box at outPath with a new key + PIN.
func PackFile(srcDir, outPath string, src box.Source, tools []string, opt box.Options) (Packed, error) {
	var p Packed
	st, err := os.Stat(srcDir)
	if err != nil {
		return p, err
	}
	if !st.IsDir() {
		return p, fmt.Errorf("%s is not a folder", srcDir)
	}
	if abs, _ := filepath.Abs(outPath); isInside(abs, srcDir) {
		return p, fmt.Errorf("the box file can't be saved inside the folder being packed")
	}
	key, pin, err := lock.NewSecret()
	if err != nil {
		return p, err
	}
	tmp := outPath + ".part"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return p, err
	}
	defer os.Remove(tmp) // no-op after the rename succeeds

	sealed, err := lock.Seal(f, key, pin)
	if err != nil {
		f.Close()
		return p, err
	}
	m, rep, err := box.Pack(srcDir, sealed, src, tools, opt)
	if err == nil {
		err = sealed.Close()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return p, err
	}
	if err := os.Rename(tmp, outPath); err != nil {
		return p, err
	}
	st, _ = os.Stat(outPath)
	return Packed{Path: outPath, Key: key, PIN: pin, Manifest: m, Report: rep, Bytes: st.Size()}, nil
}

// OpenFile unlocks the box at boxPath and unpacks it into destDir.
func OpenFile(boxPath, key, pin, destDir string) (box.Manifest, error) {
	f, err := os.Open(boxPath)
	if err != nil {
		return box.Manifest{}, err
	}
	defer f.Close()
	return OpenReader(f, key, pin, destDir)
}

// OpenReader is OpenFile for an already-open stream (e.g. a download).
func OpenReader(r io.Reader, key, pin, destDir string) (box.Manifest, error) {
	plain, err := lock.Unseal(r, key, pin)
	if err != nil {
		return box.Manifest{}, err
	}
	return box.Open(plain, destDir)
}

func isInside(p, dir string) bool {
	d, err := filepath.Abs(dir)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(d, p)
	if err != nil || filepath.IsAbs(rel) {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
