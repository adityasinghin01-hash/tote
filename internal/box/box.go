// Package box packs a folder into a box payload and opens it again (SPEC §1–3).
// It deals in plain bytes; locking is done by package lock around it.
package box

import (
	"archive/tar"
	"compress/gzip"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/adityasinghin01-hash/tote/internal/quiz"
)

const (
	Format       = "tote-box/1"
	ManifestName = "manifest.json"
)

// Version is set by the CLI so manifests record which tote made them.
var Version = "dev"

type Source struct {
	OS        string `json:"os"`
	Home      string `json:"home"`
	HostLabel string `json:"host_label"`
}

type File struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type Manifest struct {
	Format      string    `json:"format"`
	ID          string    `json:"id,omitempty"` // random; lets a box sent back home find what it came from
	Created     time.Time `json:"created"`
	ToteVersion string    `json:"tote_version"`
	Source      Source    `json:"source"`
	Tools       []string  `json:"tools"`
	Files       []File    `json:"files"`
}

// TotalSize is the sum of all file sizes in the manifest.
func (m Manifest) TotalSize() int64 {
	var n int64
	for _, f := range m.Files {
		n += f.Size
	}
	return n
}

// Options control what Pack does with shortcuts (symlinks).
type Options struct {
	// FollowLinks packs the real file or folder a shortcut points to, as
	// ordinary files. Without it, shortcuts are skipped. Shortcuts that loop
	// back onto a folder already being packed are always skipped.
	FollowLinks bool
}

// Report says what Pack did beyond the plain files.
type Report struct {
	Skipped  []string // not packed: special files, broken or looping shortcuts, unsafe names
	Followed []string // shortcuts whose target was packed in their place
	NotYours []string // owned by another user on this computer: never packed
	// QuizLocked: a quiz.json was turned into quiz.lock (answers fingerprinted).
	QuizLocked bool
}

// Pack writes srcDir as a gzip'd tar payload to w, with manifest.json last.
func Pack(srcDir string, w io.Writer, src Source, tools []string, opt Options) (m Manifest, rep Report, err error) {
	if src.OS == "" {
		src.OS = runtime.GOOS
	}
	idb := make([]byte, 8)
	if _, err := rand.Read(idb); err != nil {
		return m, rep, err
	}
	m = Manifest{Format: Format, ID: hex.EncodeToString(idb), Created: time.Now().UTC().Truncate(time.Second),
		ToteVersion: Version, Source: src, Tools: tools, Files: []File{}}
	if m.Tools == nil {
		m.Tools = []string{}
	}
	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)

	root, err := filepath.EvalSymlinks(srcDir)
	if err != nil {
		return m, rep, err
	}
	if st, err := os.Stat(root); err != nil {
		return m, rep, err
	} else if !OwnedByMe(st) {
		return m, rep, fmt.Errorf("%s belongs to another user on this computer — tote only sends your own files", srcDir)
	}
	pk := &packer{tw: tw, m: &m, rep: &rep, opt: opt, open: map[string]bool{}}
	if err = pk.dir(root, ""); err != nil {
		return m, rep, err
	}

	mj, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return m, rep, err
	}
	if err = tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: ManifestName, Mode: 0o644,
		Size: int64(len(mj)), ModTime: m.Created, Format: tar.FormatPAX}); err != nil {
		return m, rep, err
	}
	if _, err = tw.Write(mj); err != nil {
		return m, rep, err
	}
	if err = tw.Close(); err != nil {
		return m, rep, err
	}
	return m, rep, gz.Close()
}

type packer struct {
	tw   *tar.Writer
	m    *Manifest
	rep  *Report
	opt  Options
	open map[string]bool // real paths of folders currently being walked (loop guard)
}

func (pk *packer) lockQuiz(p string) error {
	plain, err := os.ReadFile(p)
	if err != nil {
		return err
	}
	locked, err := quiz.Lock(plain)
	if err != nil {
		return err
	}
	if err := pk.tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: quiz.LockedName, Mode: 0o644,
		Size: int64(len(locked)), ModTime: time.Now(), Format: tar.FormatPAX}); err != nil {
		return err
	}
	if _, err := pk.tw.Write(locked); err != nil {
		return err
	}
	h := sha256.Sum256(locked)
	pk.m.Files = append(pk.m.Files, File{Path: quiz.LockedName, Size: int64(len(locked)), SHA256: hex.EncodeToString(h[:])})
	pk.rep.QuizLocked = true
	return nil
}

// dir packs the contents of the real folder at real under the box name prefix.
func (pk *packer) dir(real, prefix string) error {
	pk.open[real] = true
	defer delete(pk.open, real)
	ents, err := os.ReadDir(real)
	if err != nil {
		return err
	}
	for _, e := range ents { // ReadDir is sorted, so boxes are reproducible
		p := filepath.Join(real, e.Name())
		name := e.Name()
		if prefix != "" {
			name = prefix + "/" + name
		}
		if name == ManifestName {
			return fmt.Errorf("source folder already contains %s", ManifestName)
		}
		if prefix == "" && name == quiz.LockedName {
			if _, err := os.Stat(filepath.Join(real, quiz.PlainName)); err == nil {
				return fmt.Errorf("folder has both %s and %s — keep only %s", quiz.PlainName, quiz.LockedName, quiz.PlainName)
			}
		}
		if prefix == "" && name == "TASK.md" {
			continue // instructions for the sending AI only
		}
		if prefix == "" && name == quiz.PlainName {
			// The proof quiz: answers never travel, only their fingerprints.
			if err := pk.lockQuiz(filepath.Join(real, name)); err != nil {
				return err
			}
			continue
		}
		if _, err := safeName(name); err != nil { // e.g. ':' in a name won't open on Windows
			pk.rep.Skipped = append(pk.rep.Skipped, name)
			continue
		}
		info, err := os.Lstat(p)
		if err != nil {
			return err
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			if !pk.opt.FollowLinks {
				pk.rep.Skipped = append(pk.rep.Skipped, name)
				continue
			}
			target, err := filepath.EvalSymlinks(p)
			if err != nil { // broken shortcut
				pk.rep.Skipped = append(pk.rep.Skipped, name)
				continue
			}
			if info, err = os.Stat(target); err != nil {
				pk.rep.Skipped = append(pk.rep.Skipped, name)
				continue
			}
			if info.IsDir() && pk.open[target] { // would loop forever
				pk.rep.Skipped = append(pk.rep.Skipped, name)
				continue
			}
			pk.rep.Followed = append(pk.rep.Followed, name)
			p = target
		}
		if !OwnedByMe(info) {
			pk.rep.NotYours = append(pk.rep.NotYours, name)
			continue
		}
		switch {
		case info.IsDir():
			if err := pk.tw.WriteHeader(&tar.Header{Typeflag: tar.TypeDir, Name: name + "/",
				Mode: 0o755, ModTime: info.ModTime(), Format: tar.FormatPAX}); err != nil {
				return err
			}
			real := p
			if r, err := filepath.EvalSymlinks(p); err == nil {
				real = r
			}
			if err := pk.dir(real, name); err != nil {
				return err
			}
		case info.Mode().IsRegular():
			if err := addFile(pk.tw, p, name, info, pk.m); err != nil {
				return err
			}
		default:
			pk.rep.Skipped = append(pk.rep.Skipped, name)
		}
	}
	return nil
}

func addFile(tw *tar.Writer, p, name string, info fs.FileInfo, m *Manifest) error {
	f, err := os.Open(p)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: name,
		Mode: int64(info.Mode().Perm()), Size: info.Size(), ModTime: info.ModTime(),
		Format: tar.FormatPAX}); err != nil {
		return err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(tw, h), f)
	if err != nil {
		return err
	}
	if n != info.Size() {
		return fmt.Errorf("%s changed size while packing", name)
	}
	m.Files = append(m.Files, File{Path: name, Size: n, SHA256: hex.EncodeToString(h.Sum(nil))})
	return nil
}

// ErrDestExists is returned when the open target already has something in it.
var ErrDestExists = errors.New("destination already exists and is not empty")

// Open reads a payload from r into destDir. It extracts into a temporary
// folder next to destDir, checks every file against the manifest, and only
// then moves it into place. On any failure nothing is left behind.
func Open(r io.Reader, destDir string) (Manifest, error) {
	var m Manifest
	if entries, err := os.ReadDir(destDir); err == nil && len(entries) > 0 {
		return m, ErrDestExists
	}
	parent := filepath.Dir(destDir)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return m, err
	}
	tmp, err := os.MkdirTemp(parent, ".tote-open-")
	if err != nil {
		return m, err
	}
	ok := false
	defer func() {
		if !ok {
			os.RemoveAll(tmp)
		}
	}()

	got, m, err := extract(r, tmp)
	if err != nil {
		return m, err
	}
	if err := verify(got, m); err != nil {
		return m, err
	}
	os.Remove(destDir) // fine if missing; fails (harmlessly) only if non-empty, checked above
	if err := os.Rename(tmp, destDir); err != nil {
		return m, err
	}
	ok = true
	return m, nil
}

func extract(r io.Reader, root string) (map[string]File, Manifest, error) {
	var m Manifest
	gz, err := gzip.NewReader(r)
	if err != nil {
		return nil, m, fmt.Errorf("not a tote box (bad compression): %w", err)
	}
	tr := tar.NewReader(gz)
	got := map[string]File{}
	sawManifest := false
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, m, fmt.Errorf("box is damaged: %w", err)
		}
		if sawManifest {
			return nil, m, fmt.Errorf("box has entries after %s", ManifestName)
		}
		name, err := safeName(hdr.Name)
		if err != nil {
			return nil, m, err
		}
		target := filepath.Join(root, filepath.FromSlash(name))
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return nil, m, err
			}
		case tar.TypeReg:
			if name == ManifestName {
				dec := json.NewDecoder(io.LimitReader(tr, 64<<20))
				if err := dec.Decode(&m); err != nil {
					return nil, m, fmt.Errorf("manifest unreadable: %w", err)
				}
				sawManifest = true
				continue
			}
			if _, dup := got[name]; dup {
				return nil, m, fmt.Errorf("box lists %s twice", name)
			}
			f, err := writeFile(tr, target, hdr)
			if err != nil {
				return nil, m, err
			}
			got[name] = f
		default:
			return nil, m, fmt.Errorf("box contains a link or special file (%s); refusing", name)
		}
	}
	if !sawManifest {
		return nil, m, fmt.Errorf("box has no %s", ManifestName)
	}
	if m.Format != Format {
		return nil, m, fmt.Errorf("unsupported box format %q (this tote reads %s)", m.Format, Format)
	}
	return got, m, nil
}

func writeFile(tr *tar.Reader, target string, hdr *tar.Header) (File, error) {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return File{}, err
	}
	perm := os.FileMode(hdr.Mode).Perm()&0o755 | 0o600
	out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, perm)
	if err != nil {
		return File{}, err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(out, h), tr)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return File{}, err
	}
	os.Chtimes(target, hdr.ModTime, hdr.ModTime)
	return File{Path: filepath.ToSlash(hdr.Name), Size: n, SHA256: hex.EncodeToString(h.Sum(nil))}, nil
}

// safeName rejects any tar name that could land outside the target folder.
func safeName(n string) (string, error) {
	bad := func() (string, error) { return "", fmt.Errorf("unsafe path in box: %q", n) }
	if n == "" || strings.ContainsAny(n, "\\:\x00") || strings.HasPrefix(n, "/") {
		return bad()
	}
	c := path.Clean(n)
	if c == "." || c == ".." || strings.HasPrefix(c, "../") {
		return bad()
	}
	return c, nil
}

func verify(got map[string]File, m Manifest) error {
	var problems []string
	want := map[string]File{}
	for _, f := range m.Files {
		want[f.Path] = f
		g, ok := got[f.Path]
		switch {
		case !ok:
			problems = append(problems, "missing "+f.Path)
		case g.Size != f.Size || g.SHA256 != f.SHA256:
			problems = append(problems, "changed "+f.Path)
		}
	}
	for p := range got {
		if _, ok := want[p]; !ok {
			problems = append(problems, "unexpected "+p)
		}
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		if len(problems) > 5 {
			problems = append(problems[:5], fmt.Sprintf("…and %d more", len(problems)-5))
		}
		return fmt.Errorf("box failed its check: %s", strings.Join(problems, ", "))
	}
	return nil
}
