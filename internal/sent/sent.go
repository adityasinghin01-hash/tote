// Package sent remembers, on the sending computer, exactly what each box
// carried — so when work comes back (tote merge) tote can tell "changed only
// over there" (safe to update) from "changed on both sides" (keep both).
package sent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/adityasinghin01-hash/tote/internal/box"
	"github.com/adityasinghin01-hash/tote/internal/config"
)

type Record struct {
	ID      string            `json:"id"`
	Created time.Time         `json:"created"`
	Tools   []string          `json:"tools"`
	Files   map[string]string `json:"files"` // "tools/<tool>/<path>" → sha256 as sent
}

func path(id string) (string, error) {
	d, err := config.Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "sent", id+".json"), nil
}

// Save records what a box carried. Only tool files matter for merging.
func Save(m box.Manifest) error {
	if m.ID == "" {
		return nil
	}
	r := Record{ID: m.ID, Created: m.Created, Tools: m.Tools, Files: map[string]string{}}
	for _, f := range m.Files {
		if strings.HasPrefix(f.Path, "tools/") {
			r.Files[f.Path] = f.SHA256
		}
	}
	p, err := path(m.ID)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	b, _ := json.Marshal(r)
	return os.WriteFile(p, b, 0o600)
}

// Load returns the record for a box ID, or ok=false if this computer didn't send it.
func Load(id string) (Record, bool) {
	var r Record
	if id == "" {
		return r, false
	}
	p, err := path(id)
	if err != nil {
		return r, false
	}
	b, err := os.ReadFile(p)
	if err != nil || json.Unmarshal(b, &r) != nil {
		return r, false
	}
	return r, true
}
