package mailbox

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// DefaultHostedURL is the public mailbox run by the tote project. Empty until
// it is deployed; set at build time with -ldflags.
var DefaultHostedURL = ""

// Hosted drops boxes into a tote relay (relay/ in this repo): free, no
// account, boxes up to 360 MB, gone after 24 hours or 5 downloads.
type Hosted struct {
	URL string `json:"url"`
}

func (h Hosted) Kind() string { return "hosted" }

func (h Hosted) base() (string, error) {
	u := strings.TrimRight(h.URL, "/")
	if u == "" {
		u = strings.TrimRight(DefaultHostedURL, "/")
	}
	if u == "" {
		return "", fmt.Errorf("no hosted mailbox is set up yet — use your own: tote mailbox use folder|s3 …")
	}
	return u, nil
}

func (h Hosted) Drop(ctx context.Context, boxPath string, _ time.Duration) (Ticket, error) {
	base, err := h.base()
	if err != nil {
		return Ticket{}, err
	}
	f, err := os.Open(boxPath)
	if err != nil {
		return Ticket{}, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return Ticket{}, err
	}

	var made struct {
		ID        string `json:"id"`
		PutToken  string `json:"put_token"`
		ReadToken string `json:"read_token"`
		PartSize  int64  `json:"part_size"`
		Parts     int    `json:"parts"`
	}
	body, _ := json.Marshal(map[string]int64{"size": st.Size()})
	if err := call(ctx, http.MethodPost, base+"/v1/boxes", "", bytes.NewReader(body), int64(len(body)), &made); err != nil {
		return Ticket{}, err
	}
	auth := "Bearer " + made.PutToken
	for n := 0; n < made.Parts; n++ {
		off := int64(n) * made.PartSize
		size := min(made.PartSize, st.Size()-off)
		part := io.NewSectionReader(f, off, size)
		if err := call(ctx, http.MethodPut, fmt.Sprintf("%s/v1/boxes/%s/%d", base, made.ID, n), auth, part, size, nil); err != nil {
			return Ticket{}, fmt.Errorf("uploading part %d/%d: %w", n+1, made.Parts, err)
		}
	}
	var sealed struct {
		Get     string `json:"get"`
		Del     string `json:"del"`
		Expires int64  `json:"expires"`
	}
	if err := call(ctx, http.MethodPost, fmt.Sprintf("%s/v1/boxes/%s/seal", base, made.ID), auth, nil, 0, &sealed); err != nil {
		return Ticket{}, err
	}
	return Ticket{V: 1, Get: sealed.Get + made.ReadToken, Del: sealed.Del + made.ReadToken,
		Exp: time.UnixMilli(sealed.Expires).UTC().Truncate(time.Second)}, nil
}

func call(ctx context.Context, method, url, auth string, body io.Reader, size int64, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return err
	}
	req.ContentLength = size
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	if method == http.MethodPost && body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("couldn't reach the mailbox: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		var e struct {
			Error string `json:"error"`
		}
		json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&e)
		if e.Error == "" {
			e.Error = resp.Status
		}
		return fmt.Errorf("mailbox: %s", e.Error)
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}
