// Package config stores which mailbox this computer sends to.
package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/adityasinghin01-hash/tote/internal/mailbox"
)

type Config struct {
	Mailbox string          `json:"mailbox"` // "hosted" (default), "github", "folder", "s3"
	Folder  *mailbox.Folder `json:"folder,omitempty"`
	S3      *mailbox.S3     `json:"s3,omitempty"`
	Hosted  *mailbox.Hosted `json:"hosted,omitempty"`
	GitHub  *mailbox.GitHub `json:"github,omitempty"`
}

// Dir is where tote keeps its settings. TOTE_CONFIG_DIR overrides it (guest
// mode uses this to keep everything inside tote's own folder).
func Dir() (string, error) {
	if d := os.Getenv("TOTE_CONFIG_DIR"); d != "" {
		return d, nil
	}
	// The one-line installer put tote inside the guest area: keep its
	// settings there too, never in the machine owner's config folder.
	if exe, err := os.Executable(); err == nil {
		if h, err := os.UserHomeDir(); err == nil {
			guestBin := filepath.Join(h, ".tote-guest", "bin")
			if strings.HasPrefix(exe, guestBin+string(filepath.Separator)) {
				return filepath.Join(h, ".tote-guest", "cfg"), nil
			}
		}
	}
	d, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "tote"), nil
}

func path() (string, error) {
	d, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "config.json"), nil
}

// Load returns the saved config, or the default if none: the hosted mailbox
// when one is built in, otherwise your own GitHub mailbox.
func Load() (Config, error) {
	c := Config{Mailbox: "hosted"}
	if mailbox.DefaultHostedURL == "" {
		c.Mailbox = "github"
	}
	p, err := path()
	if err != nil {
		return c, err
	}
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	err = json.Unmarshal(b, &c)
	return c, err
}

// Save writes the config readable only by this user (it may hold bucket keys).
func Save(c Config) error {
	p, err := path()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(c, "", "  ")
	tmp := p + ".part"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// MailboxImpl returns the configured mailbox.
func (c Config) MailboxImpl() (mailbox.Mailbox, error) {
	switch c.Mailbox {
	case "folder":
		if c.Folder == nil {
			return nil, errors.New("folder mailbox has no folder set")
		}
		return *c.Folder, nil
	case "s3":
		if c.S3 == nil {
			return nil, errors.New("s3 mailbox is not set up")
		}
		return *c.S3, nil
	case "github":
		if c.GitHub == nil {
			return mailbox.GitHub{}, nil
		}
		return *c.GitHub, nil
	case "hosted", "":
		if c.Hosted != nil {
			return *c.Hosted, nil
		}
		return mailbox.Hosted{}, nil
	}
	return nil, errors.New("unknown mailbox " + c.Mailbox)
}
