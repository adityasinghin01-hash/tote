package core

import (
	"bytes"
	"crypto/rand"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/adityasinghin01-hash/tote/internal/box"
	"github.com/adityasinghin01-hash/tote/internal/lock"
)

func init() { lock.WorkFactor = 10 } // keep scrypt cheap in tests

func makeTree(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	big := make([]byte, 3<<20)
	rand.Read(big)
	files := map[string][]byte{
		"START-HERE.md":                 []byte("# hello\n"),
		"tools/claude/memory/MEMORY.md": []byte("- [x](x.md)\n"),
		"tools/claude/memory/नोट्स.md":  []byte("unicode name\n"),
		"raw/claude/blob.bin":           big,
		"empty.txt":                     {},
		"deep/a/b/c/d/e.txt":            []byte("deep"),
	}
	for p, b := range files {
		full := filepath.Join(dir, filepath.FromSlash(p))
		os.MkdirAll(filepath.Dir(full), 0o755)
		if err := os.WriteFile(full, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	os.MkdirAll(filepath.Join(dir, "digests"), 0o755) // empty dir must survive
	return dir
}

// snapshot maps every path under root to its bytes ("<dir>" for folders).
func snapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			t.Fatal(err)
		}
		rel, _ := filepath.Rel(root, p)
		if d.IsDir() {
			out[rel] = "<dir>"
			return nil
		}
		b, _ := os.ReadFile(p)
		out[rel] = string(b)
		return nil
	})
	return out
}

func TestRoundTripIsIdentical(t *testing.T) {
	src := makeTree(t)
	work := t.TempDir()
	p, err := PackFile(src, filepath.Join(work, "b.tote"), box.Source{HostLabel: "mac"}, []string{"claude"}, box.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !lock.ValidKey(p.Key) || !lock.ValidPIN(p.PIN) {
		t.Fatalf("bad secret %q %q", p.Key, p.PIN)
	}
	dest := filepath.Join(work, "out")
	m, err := OpenFile(p.Path, p.Key, p.PIN, dest)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Files) != 6 || m.Source.HostLabel != "mac" || m.Tools[0] != "claude" {
		t.Fatalf("manifest wrong: %+v", m)
	}
	a, b := snapshot(t, src), snapshot(t, dest)
	if len(a) != len(b) {
		t.Fatalf("entry count %d vs %d", len(a), len(b))
	}
	for k, v := range a {
		if b[k] != v {
			t.Fatalf("%s differs after round trip", k)
		}
	}
	if _, err := os.Stat(p.Path + ".part"); !os.IsNotExist(err) {
		t.Fatal("temp .part file left behind")
	}
}

func TestWrongPINFailsAndLeavesNothing(t *testing.T) {
	src := makeTree(t)
	work := t.TempDir()
	p, _ := PackFile(src, filepath.Join(work, "b.tote"), box.Source{}, nil, box.Options{})
	pin := "000000"
	if p.PIN == pin {
		pin = "111111"
	}
	_, err := OpenFile(p.Path, p.Key, pin, filepath.Join(work, "out"))
	if !errors.Is(err, lock.ErrWrongCode) {
		t.Fatalf("want ErrWrongCode, got %v", err)
	}
	assertOnly(t, work, "b.tote")
}

func TestTamperedBoxIsRejected(t *testing.T) {
	src := makeTree(t)
	work := t.TempDir()
	p, _ := PackFile(src, filepath.Join(work, "b.tote"), box.Source{}, nil, box.Options{})
	b, _ := os.ReadFile(p.Path)
	b[len(b)/2] ^= 0xFF
	os.WriteFile(p.Path, b, 0o600)
	if _, err := OpenFile(p.Path, p.Key, p.PIN, filepath.Join(work, "out")); err == nil {
		t.Fatal("tampered box opened")
	}
	assertOnly(t, work, "b.tote")
}

func TestWontOverwriteExistingFolder(t *testing.T) {
	src := makeTree(t)
	work := t.TempDir()
	p, _ := PackFile(src, filepath.Join(work, "b.tote"), box.Source{}, nil, box.Options{})
	dest := filepath.Join(work, "out")
	os.MkdirAll(dest, 0o755)
	os.WriteFile(filepath.Join(dest, "theirs.txt"), []byte("keep me"), 0o644)
	if _, err := OpenFile(p.Path, p.Key, p.PIN, dest); !errors.Is(err, box.ErrDestExists) {
		t.Fatalf("want ErrDestExists, got %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(dest, "theirs.txt")); string(b) != "keep me" {
		t.Fatal("existing file was touched")
	}
}

func TestBoxInsideSourceRefused(t *testing.T) {
	src := makeTree(t)
	if _, err := PackFile(src, filepath.Join(src, "self.tote"), box.Source{}, nil, box.Options{}); err == nil {
		t.Fatal("packing into own folder should fail")
	}
}

func TestSymlinkSkipped(t *testing.T) {
	src := makeTree(t)
	if err := os.Symlink("/etc/passwd", filepath.Join(src, "link")); err != nil {
		t.Skip("symlinks unsupported here")
	}
	p, err := PackFile(src, filepath.Join(t.TempDir(), "b.tote"), box.Source{}, nil, box.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Report.Skipped) != 1 || p.Report.Skipped[0] != "link" {
		t.Fatalf("skipped = %v", p.Report.Skipped)
	}
}

func assertOnly(t *testing.T, dir string, names ...string) {
	t.Helper()
	ents, _ := os.ReadDir(dir)
	var got []string
	for _, e := range ents {
		got = append(got, e.Name())
	}
	if !bytes.Equal([]byte(join(got)), []byte(join(names))) {
		t.Fatalf("folder has %v, want only %v", got, names)
	}
}

func join(s []string) string {
	out := ""
	for _, x := range s {
		out += x + "|"
	}
	return out
}

func TestFollowsShortcutsAndGuardsLoops(t *testing.T) {
	src := makeTree(t)
	outside := t.TempDir() // like ~/.agents/skills: real files live elsewhere
	os.MkdirAll(filepath.Join(outside, "skill-a"), 0o755)
	os.WriteFile(filepath.Join(outside, "skill-a", "SKILL.md"), []byte("real skill"), 0o644)
	os.Symlink(filepath.Join(outside, "skill-a"), filepath.Join(src, "skill-a")) // followed
	os.Symlink(src, filepath.Join(src, "deep", "loop"))                          // loops to root: skipped
	os.Symlink(filepath.Join(outside, "nope"), filepath.Join(src, "broken"))     // broken: skipped

	work := t.TempDir()
	p, err := PackFile(src, filepath.Join(work, "b.tote"), box.Source{}, nil, box.Options{FollowLinks: true})
	if err != nil {
		t.Fatal(err)
	}
	if join(p.Report.Followed) != "skill-a|" {
		t.Fatalf("followed = %v", p.Report.Followed)
	}
	if join(p.Report.Skipped) != "broken|deep/loop|" {
		t.Fatalf("skipped = %v", p.Report.Skipped)
	}
	dest := filepath.Join(work, "out")
	if _, err := OpenFile(p.Path, p.Key, p.PIN, dest); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Lstat(filepath.Join(dest, "skill-a", "SKILL.md"))
	if err != nil || !fi.Mode().IsRegular() {
		t.Fatalf("followed skill should arrive as a real file: %v", err)
	}
}

func TestOtherUsersFilesNeverPacked(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows enforces this with profile permissions, not file owners")
	}
	if os.Getuid() == 0 {
		t.Skip("running as root")
	}
	src := makeTree(t)
	// /etc/hosts is owned by root on macOS and Linux: a shortcut to it must not leak it.
	if err := os.Symlink("/etc/hosts", filepath.Join(src, "sneaky")); err != nil {
		t.Skip("symlinks unsupported")
	}
	p, err := PackFile(src, filepath.Join(t.TempDir(), "b.tote"), box.Source{}, nil, box.Options{FollowLinks: true})
	if err != nil {
		t.Fatal(err)
	}
	if join(p.Report.NotYours) != "sneaky|" {
		t.Fatalf("not-yours = %v", p.Report.NotYours)
	}
	for _, f := range p.Manifest.Files {
		if f.Path == "sneaky" {
			t.Fatal("another user's file was packed")
		}
	}
	if _, err := PackFile("/etc", filepath.Join(t.TempDir(), "c.tote"), box.Source{}, nil, box.Options{}); err == nil {
		t.Fatal("packing a folder owned by someone else should fail")
	}
}

func TestQuizAnswersNeverTravel(t *testing.T) {
	src := makeTree(t)
	os.WriteFile(filepath.Join(src, "TASK.md"), []byte("sender only"), 0o600)
	os.WriteFile(filepath.Join(src, "quiz.json"), []byte(`{"questions":[
		{"q":"Pilot city?","answers":["Lisbon"]},{"q":"Price?","answers":["1500"]},{"q":"Public?","answers":["no"]}]}`), 0o600)
	work := t.TempDir()
	p, err := PackFile(src, filepath.Join(work, "b.tote"), box.Source{}, nil, box.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !p.Report.QuizLocked {
		t.Fatal("quiz not locked")
	}
	dest := filepath.Join(work, "out")
	if _, err := OpenFile(p.Path, p.Key, p.PIN, dest); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dest, "TASK.md")); !os.IsNotExist(err) {
		t.Fatal("sender-only TASK.md travelled")
	}
	if _, err := os.Stat(filepath.Join(dest, "quiz.json")); !os.IsNotExist(err) {
		t.Fatal("quiz.json (with answers) travelled")
	}
	lockB, err := os.ReadFile(filepath.Join(dest, "quiz.lock"))
	if err != nil || bytes.Contains(lockB, []byte("Lisbon")) || !bytes.Contains(lockB, []byte("Pilot city?")) {
		t.Fatalf("quiz.lock wrong: %v %s", err, lockB)
	}

	bad := makeTree(t)
	os.WriteFile(filepath.Join(bad, "quiz.json"), []byte(`{"questions":[]}`), 0o600)
	if _, err := PackFile(bad, filepath.Join(work, "c.tote"), box.Source{}, nil, box.Options{}); err == nil {
		t.Fatal("a broken quiz.json should stop the send")
	}
}
