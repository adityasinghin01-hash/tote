package mailbox

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// GitHub drops boxes as release files in the sender's own public repo
// (default <you>/tote-mailbox). Release files can be up to 2 GB — a normal
// commit can't hold more than 100 MB. The receiver downloads with a plain
// link: no GitHub account, nothing installed. The repo is public, but every
// box is locked (key in the ticket + PIN). Expired boxes are swept by an
// hourly GitHub Actions job in the repo, and by every `tote send`.
type GitHub struct {
	Repo  string `json:"repo"`            // owner/name
	Token string `json:"token,omitempty"` // saved by `mailbox use github --token-stdin`; settings file is owner-only
}

// GitHubAPI is the API base; tests point it at a fake.
var GitHubAPI = "https://api.github.com"

func (g GitHub) Kind() string { return "github" }

// token: GITHUB_TOKEN / GH_TOKEN, then the saved token, then the GitHub CLI.
func (g GitHub) token() (string, error) {
	for _, k := range []string{"GITHUB_TOKEN", "GH_TOKEN"} {
		if v := os.Getenv(k); v != "" {
			return v, nil
		}
	}
	if g.Token != "" {
		return g.Token, nil
	}
	return githubToken()
}

func githubToken() (string, error) {
	for _, k := range []string{"GITHUB_TOKEN", "GH_TOKEN"} {
		if v := os.Getenv(k); v != "" {
			return v, nil
		}
	}
	out, err := exec.Command("gh", "auth", "token").Output()
	if err != nil || len(bytes.TrimSpace(out)) == 0 {
		return "", errors.New("can't get your GitHub login — run:  gh auth token | tote mailbox use github --token-stdin")
	}
	return strings.TrimSpace(string(out)), nil
}

type ghClient struct {
	token string
}

func (c ghClient) do(ctx context.Context, method, url string, body io.Reader, size int64, ctype string, out any) (int, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return 0, err
	}
	if size >= 0 {
		req.ContentLength = size
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	resp, err := HTTPClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("couldn't reach GitHub: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		var e struct {
			Message string `json:"message"`
		}
		json.NewDecoder(io.LimitReader(resp.Body, 8192)).Decode(&e)
		return resp.StatusCode, fmt.Errorf("GitHub said %s: %s", resp.Status, e.Message)
	}
	if out != nil {
		return resp.StatusCode, json.NewDecoder(resp.Body).Decode(out)
	}
	return resp.StatusCode, nil
}

func (c ghClient) json(ctx context.Context, method, url string, in, out any) (int, error) {
	var body io.Reader
	size := int64(-1)
	if in != nil {
		b, _ := json.Marshal(in)
		body, size = bytes.NewReader(b), int64(len(b))
	}
	return c.do(ctx, method, url, body, size, "application/json", out)
}

// SweepWorkflow deletes expired boxes every hour, even if nobody runs tote.
const SweepWorkflow = `name: sweep expired tote boxes
on:
  schedule: [{cron: "17 * * * *"}]
  workflow_dispatch:
permissions:
  contents: write
jobs:
  sweep:
    runs-on: ubuntu-latest
    steps:
      - env:
          GH_TOKEN: ${{ github.token }}
          REPO: ${{ github.repository }}
        run: |
          now=$(date -u +%s)
          gh api "repos/$REPO/releases" --paginate \
            --jq '.[] | select(.tag_name | startswith("box-")) | [.tag_name, ((.body // "") | capture("Expires: (?<e>[0-9TZ:+-]+)").e // .created_at)] | @tsv' |
          while IFS=$'\t' read -r tag exp; do
            if [ "$(date -u -d "$exp" +%s)" -lt "$now" ]; then
              gh release delete "$tag" --repo "$REPO" --cleanup-tag --yes
            fi
          done
`

const mailboxReadme = `# tote mailbox

Locked boxes from [tote](https://github.com/adityasinghin01-hash/tote) wait here
for collection. Each one is encrypted on the sender's computer; it can't be
opened without the key in its ticket **and** a PIN sent separately.

Boxes are deleted when they expire (hourly sweep) or after being collected.
`

// EnsureRepo creates the mailbox repo (with README + sweep job) if missing.
func (g *GitHub) EnsureRepo(ctx context.Context) (created bool, err error) {
	tok, err := g.token()
	if err != nil {
		return false, err
	}
	c := ghClient{tok}
	if g.Repo == "" {
		var u struct {
			Login string `json:"login"`
		}
		if _, err := c.json(ctx, "GET", GitHubAPI+"/user", nil, &u); err != nil {
			return false, err
		}
		g.Repo = u.Login + "/tote-mailbox"
	}
	code, err := c.json(ctx, "GET", GitHubAPI+"/repos/"+g.Repo, nil, nil)
	if err == nil {
		return false, nil
	}
	if code != http.StatusNotFound {
		return false, err
	}
	owner, name, _ := strings.Cut(g.Repo, "/")
	var me struct {
		Login string `json:"login"`
	}
	c.json(ctx, "GET", GitHubAPI+"/user", nil, &me)
	if !strings.EqualFold(owner, me.Login) {
		return false, fmt.Errorf("repo %s doesn't exist and tote only creates mailboxes in your own account", g.Repo)
	}
	if _, err := c.json(ctx, "POST", GitHubAPI+"/user/repos", map[string]any{
		"name": name, "description": "tote mailbox: locked boxes waiting to be collected",
		"private": false, "auto_init": true, "has_issues": false, "has_wiki": false, "has_projects": false,
	}, nil); err != nil {
		return false, err
	}
	for path, content := range map[string]string{".github/workflows/sweep.yml": SweepWorkflow, "README.md": mailboxReadme} {
		var cur struct {
			SHA string `json:"sha"`
		}
		c.json(ctx, "GET", GitHubAPI+"/repos/"+g.Repo+"/contents/"+path, nil, &cur)
		body := map[string]any{"message": "tote: set up mailbox", "content": base64.StdEncoding.EncodeToString([]byte(content))}
		if cur.SHA != "" {
			body["sha"] = cur.SHA
		}
		if _, err := c.json(ctx, "PUT", GitHubAPI+"/repos/"+g.Repo+"/contents/"+path, body, nil); err != nil {
			return true, fmt.Errorf("mailbox made, but setting up %s failed: %w", path, err)
		}
	}
	return true, nil
}

type ghRelease struct {
	ID        int64     `json:"id"`
	TagName   string    `json:"tag_name"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
	UploadURL string    `json:"upload_url"`
	Assets    []struct {
		Name          string `json:"name"`
		URL           string `json:"browser_download_url"`
		DownloadCount int    `json:"download_count"`
	} `json:"assets"`
}

var expiresRe = regexp.MustCompile(`Expires: (\S+)`)

func (g GitHub) Drop(ctx context.Context, boxPath string, ttl time.Duration) (Ticket, error) {
	if ttl <= 0 || ttl > 7*24*time.Hour {
		ttl = 24 * time.Hour
	}
	if _, err := g.EnsureRepo(ctx); err != nil { // also fills in g.Repo
		return Ticket{}, err
	}
	tok, err := g.token()
	if err != nil {
		return Ticket{}, err
	}
	c := ghClient{tok}
	g.Sweep(ctx) // old and collected boxes go first

	f, err := os.Open(boxPath)
	if err != nil {
		return Ticket{}, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return Ticket{}, err
	}
	if st.Size() >= 2<<30 {
		return Ticket{}, errors.New("box is over 2 GB, GitHub's limit per file — send fewer chats (--days) or use your own S3/R2 bucket")
	}
	idb := make([]byte, 8)
	rand.Read(idb)
	tag := "box-" + hex.EncodeToString(idb)
	exp := time.Now().Add(ttl).UTC().Truncate(time.Second)
	var rel ghRelease
	if _, err := c.json(ctx, "POST", GitHubAPI+"/repos/"+g.Repo+"/releases", map[string]any{
		"tag_name": tag, "name": "tote box", "prerelease": true,
		"body": "Locked tote box — useless without its ticket and PIN.\n\nExpires: " + exp.Format(time.RFC3339),
	}, &rel); err != nil {
		return Ticket{}, err
	}
	upload := strings.SplitN(rel.UploadURL, "{", 2)[0] + "?name=box.tote"
	var asset struct {
		URL string `json:"browser_download_url"`
	}
	if _, err := c.do(ctx, "POST", upload, f, st.Size(), "application/octet-stream", &asset); err != nil {
		c.json(ctx, "DELETE", GitHubAPI+"/repos/"+g.Repo+"/releases/"+fmt.Sprint(rel.ID), nil, nil)
		return Ticket{}, fmt.Errorf("upload failed: %w", err)
	}
	// The receiver has no GitHub login, so it can't delete: the sweeps do.
	return Ticket{V: 1, Get: asset.URL, Exp: exp}, nil
}

// Sweep deletes boxes that expired, or were collected more than an hour ago.
func (g GitHub) Sweep(ctx context.Context) (deleted int) {
	tok, err := g.token()
	if err != nil || g.Repo == "" {
		return 0
	}
	c := ghClient{tok}
	var rels []ghRelease
	if _, err := c.json(ctx, "GET", GitHubAPI+"/repos/"+g.Repo+"/releases?per_page=100", nil, &rels); err != nil {
		return 0
	}
	for _, r := range rels {
		if !strings.HasPrefix(r.TagName, "box-") {
			continue
		}
		expired := false
		if m := expiresRe.FindStringSubmatch(r.Body); m != nil {
			if t, err := time.Parse(time.RFC3339, m[1]); err == nil && time.Now().After(t) {
				expired = true
			}
		}
		collected := len(r.Assets) > 0 && r.Assets[0].DownloadCount > 0 && time.Since(r.CreatedAt) > time.Hour
		if !expired && !collected {
			continue
		}
		if _, err := c.json(ctx, "DELETE", GitHubAPI+"/repos/"+g.Repo+"/releases/"+fmt.Sprint(r.ID), nil, nil); err == nil {
			c.json(ctx, "DELETE", GitHubAPI+"/repos/"+g.Repo+"/git/refs/tags/"+r.TagName, nil, nil)
			deleted++
		}
	}
	return deleted
}
