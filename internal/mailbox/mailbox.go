// Package mailbox stores locked boxes somewhere both computers can reach.
//
// The sender drops a box into a mailbox and gets a Ticket. The ticket holds
// plain download/delete links (time-limited where the mailbox supports it),
// so the receiving computer never needs anyone's storage password — only the
// ticket, and the PIN sent separately.
package mailbox

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Ticket is everything the receiver needs except the PIN.
type Ticket struct {
	V   int       `json:"v"`
	Get string    `json:"g"`           // https://… or file:///…
	Del string    `json:"d,omitempty"` // link that deletes the box after a good open
	Key string    `json:"k"`           // lock key (SPEC §4)
	Exp time.Time `json:"-"`           // when the links stop working (zero: never)
	E   int64     `json:"e,omitempty"` // Exp as Unix seconds, for compact encoding
}

const ticketPrefix = "tote1_"

// String encodes the ticket as one pasteable token with no spaces or quotes.
func (t Ticket) String() string {
	if t.Del == t.Get {
		t.Del = "=" // same link deletes; don't spend characters repeating it
	}
	if !t.Exp.IsZero() {
		t.E = t.Exp.Unix()
	}
	b, _ := json.Marshal(t)
	return ticketPrefix + base64.RawURLEncoding.EncodeToString(b)
}

// ParseTicket reverses Ticket.String.
func ParseTicket(s string) (Ticket, error) {
	var t Ticket
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, ticketPrefix) {
		return t, errors.New("that doesn't look like a tote ticket (it should start with tote1_)")
	}
	b, err := base64.RawURLEncoding.DecodeString(s[len(ticketPrefix):])
	if err != nil {
		return t, errors.New("ticket is damaged — copy it again, all of it")
	}
	if err := json.Unmarshal(b, &t); err != nil || t.V != 1 || t.Get == "" || t.Key == "" {
		return t, errors.New("ticket is damaged — copy it again, all of it")
	}
	if t.Del == "=" {
		t.Del = t.Get
	}
	if t.E != 0 {
		t.Exp = time.Unix(t.E, 0).UTC()
	}
	return t, nil
}

// Mailbox is a place to drop a box.
type Mailbox interface {
	// Kind is a short name shown to the user ("folder", "s3", "hosted").
	Kind() string
	// Drop uploads the box file and returns links valid for about ttl.
	// The returned ticket has no Key; the caller adds it.
	Drop(ctx context.Context, boxPath string, ttl time.Duration) (Ticket, error)
}

// HTTPClient is used for every link fetch; tests may replace it.
var HTTPClient = &http.Client{Timeout: 0} // no overall timeout: boxes can be large

// Fetch opens the box a ticket points to as a stream.
func Fetch(ctx context.Context, t Ticket) (io.ReadCloser, int64, error) {
	if !t.Exp.IsZero() && time.Now().After(t.Exp) {
		return nil, 0, fmt.Errorf("this ticket expired at %s — ask for a new one", t.Exp.Local().Format("2 Jan 15:04"))
	}
	u, err := url.Parse(t.Get)
	if err != nil {
		return nil, 0, err
	}
	switch u.Scheme {
	case "file":
		f, err := os.Open(filepath.FromSlash(u.Path))
		if err != nil {
			return nil, 0, fmt.Errorf("box not found at %s (is the shared folder synced here?)", u.Path)
		}
		st, _ := f.Stat()
		return f, st.Size(), nil
	case "https", "http":
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, t.Get, nil)
		if err != nil {
			return nil, 0, err
		}
		resp, err := HTTPClient.Do(req)
		if err != nil {
			return nil, 0, fmt.Errorf("couldn't reach the mailbox: %w", err)
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			switch resp.StatusCode {
			case http.StatusNotFound, http.StatusGone, http.StatusForbidden:
				return nil, 0, errors.New("box is gone — it was already collected, expired, or the link is wrong")
			}
			return nil, 0, fmt.Errorf("mailbox said %s", resp.Status)
		}
		return resp.Body, resp.ContentLength, nil
	}
	return nil, 0, fmt.Errorf("unsupported link type %q", u.Scheme)
}

// Remove deletes the box after it was opened successfully (one-time use).
// Failure is not fatal: the box still expires on its own.
func Remove(ctx context.Context, t Ticket) error {
	if t.Del == "" {
		return nil
	}
	u, err := url.Parse(t.Del)
	if err != nil {
		return err
	}
	switch u.Scheme {
	case "file":
		return os.Remove(filepath.FromSlash(u.Path))
	case "https", "http":
		req, err := http.NewRequestWithContext(ctx, http.MethodDelete, t.Del, nil)
		if err != nil {
			return err
		}
		resp, err := HTTPClient.Do(req)
		if err != nil {
			return err
		}
		resp.Body.Close()
		if resp.StatusCode >= 300 && resp.StatusCode != http.StatusNotFound {
			return fmt.Errorf("delete: %s", resp.Status)
		}
		return nil
	}
	return fmt.Errorf("unsupported link type %q", u.Scheme)
}

// fileURL turns a local path into a file:// link that works on every OS.
func fileURL(p string) string {
	p = filepath.ToSlash(p)
	if !strings.HasPrefix(p, "/") { // Windows: C:/x → /C:/x
		p = "/" + p
	}
	return (&url.URL{Scheme: "file", Path: p}).String()
}
