package box

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type entry struct {
	name string
	typ  byte
	body string
	link string
}

// craft builds a raw payload by hand so we can feed Open hostile input.
func craft(t *testing.T, entries []entry, m *Manifest) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		h := &tar.Header{Name: e.name, Typeflag: e.typ, Mode: 0o644, Size: int64(len(e.body)), Linkname: e.link}
		if e.typ != tar.TypeReg {
			h.Size = 0
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		tw.Write([]byte(e.body))
	}
	if m != nil {
		mj, _ := json.Marshal(m)
		tw.WriteHeader(&tar.Header{Name: ManifestName, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(mj))})
		tw.Write(mj)
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func goodManifest() *Manifest {
	return &Manifest{Format: Format, Files: []File{}}
}

func openExpectErr(t *testing.T, payload []byte, want string) {
	t.Helper()
	parent := t.TempDir()
	_, err := Open(bytes.NewReader(payload), filepath.Join(parent, "out"))
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("want error containing %q, got %v", want, err)
	}
	ents, _ := os.ReadDir(parent)
	if len(ents) != 0 {
		t.Fatalf("left behind: %v", ents)
	}
	// nothing may escape to the grandparent either
	if _, err := os.Stat(filepath.Join(filepath.Dir(parent), "evil")); err == nil {
		t.Fatal("file escaped the target folder")
	}
}

func TestRejectsPathTraversal(t *testing.T) {
	for _, n := range []string{"../evil", "a/../../evil", "/abs/evil", `..\evil`, "C:evil", ".."} {
		openExpectErr(t, craft(t, []entry{{name: n, typ: tar.TypeReg, body: "x"}}, goodManifest()), "unsafe path")
	}
}

func TestRejectsLinks(t *testing.T) {
	openExpectErr(t, craft(t, []entry{{name: "l", typ: tar.TypeSymlink, link: "/etc/passwd"}}, goodManifest()), "link or special")
	openExpectErr(t, craft(t, []entry{{name: "h", typ: tar.TypeLink, link: "x"}}, goodManifest()), "link or special")
}

func TestRejectsMissingManifest(t *testing.T) {
	openExpectErr(t, craft(t, []entry{{name: "a.txt", typ: tar.TypeReg, body: "x"}}, nil), "no manifest")
}

func TestRejectsEntriesAfterManifest(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	mj, _ := json.Marshal(goodManifest())
	tw.WriteHeader(&tar.Header{Name: ManifestName, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(mj))})
	tw.Write(mj)
	tw.WriteHeader(&tar.Header{Name: "late.txt", Typeflag: tar.TypeReg, Mode: 0o644, Size: 1})
	tw.Write([]byte("x"))
	tw.Close()
	gz.Close()
	openExpectErr(t, buf.Bytes(), "after manifest")
}

func TestRejectsUnlistedAndChangedFiles(t *testing.T) {
	openExpectErr(t, craft(t, []entry{{name: "extra.txt", typ: tar.TypeReg, body: "x"}}, goodManifest()), "unexpected extra.txt")

	m := goodManifest()
	m.Files = []File{{Path: "a.txt", Size: 1, SHA256: strings.Repeat("0", 64)}}
	openExpectErr(t, craft(t, []entry{{name: "a.txt", typ: tar.TypeReg, body: "x"}}, m), "changed a.txt")

	m = goodManifest()
	m.Files = []File{{Path: "gone.txt", Size: 1, SHA256: "x"}}
	openExpectErr(t, craft(t, nil, m), "missing gone.txt")
}

func TestRejectsUnknownFormat(t *testing.T) {
	m := goodManifest()
	m.Format = "tote-box/99"
	openExpectErr(t, craft(t, nil, m), "unsupported box format")
}

func TestRejectsGarbage(t *testing.T) {
	openExpectErr(t, []byte("not a box at all"), "not a tote box")
}
