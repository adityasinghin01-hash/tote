package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/adityasinghin01-hash/tote/internal/adapter"
	"github.com/adityasinghin01-hash/tote/internal/box"
	"github.com/adityasinghin01-hash/tote/internal/config"
	"github.com/adityasinghin01-hash/tote/internal/core"
	"github.com/adityasinghin01-hash/tote/internal/lock"
	"github.com/adityasinghin01-hash/tote/internal/mailbox"
	"github.com/adityasinghin01-hash/tote/internal/sent"
)

func cmdSend(args []string) error {
	fs := flag.NewFlagSet("send", flag.ContinueOnError)
	label := fs.String("label", "", "a name for this computer, shown on the other side")
	ttl := fs.Duration("ttl", 24*time.Hour, "how long the ticket works (own bucket: up to 168h)")
	noLinks := fs.Bool("no-follow", false, "skip shortcuts instead of packing what they point to")
	src, err := parse(fs, args)
	if err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(src, "TASK.md")); err == nil {
		for _, need := range []string{"START-HERE.md", "DONE.md"} {
			if _, err := os.Stat(filepath.Join(src, need)); err != nil {
				return fmt.Errorf("%s is missing — ask your AI to follow TASK.md first", need)
			}
		}
		// The AI may have copied files in after prepare: check for secrets again.
		red := map[string]int{}
		if err := adapter.ScrubFolder(src, red); err != nil {
			return err
		}
		if n := sum(red); n > 0 {
			fmt.Printf("Blanked %d more secret(s) the AI added.\n", n)
		}
		if learned, err := learnAdapters(src); err != nil {
			return err
		} else if len(learned) > 0 {
			fmt.Printf("Learned how %s works — next time tote packs it automatically.\n", strings.Join(learned, ", "))
		}
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	mb, err := cfg.MailboxImpl()
	if err != nil {
		return err
	}

	tmpDir, err := os.MkdirTemp("", "tote-send-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)
	home, _ := os.UserHomeDir()
	p, err := core.PackFile(filepath.Clean(src), filepath.Join(tmpDir, "box.tote"),
		box.Source{Home: home, HostLabel: *label}, toolsInBox(src), box.Options{FollowLinks: !*noLinks})
	if err != nil {
		return err
	}
	if n := len(p.Report.NotYours); n > 0 {
		fmt.Printf("Left out %d items owned by other users on this computer.\n", n)
	}
	if p.Report.QuizLocked {
		fmt.Println("Proof quiz locked in (answers travel only as fingerprints).")
	} else if _, err := os.Stat(filepath.Join(src, "TASK.md")); err == nil {
		fmt.Println("Note: no quiz.json — the other side won't get a proof score.")
	}
	fmt.Printf("Packed %d files (%s → %s locked). Uploading to %s mailbox…\n",
		len(p.Manifest.Files), human(p.Manifest.TotalSize()), human(p.Bytes), mb.Kind())
	t, err := mb.Drop(context.Background(), p.Path, *ttl)
	if err != nil {
		return err
	}
	t.Key = p.Key
	if err := sent.Save(p.Manifest); err != nil {
		fmt.Printf("(Couldn't record what was sent: %v — merging work back will keep both copies of changed files.)\n", err)
	}

	if base := installURL(); base != "" {
		fmt.Print("\nOn the other computer, paste ONE of these (nothing needs to be installed there):\n\n")
		fmt.Printf("Mac / Linux (Terminal):\n  curl -fsSL %s/install.sh | sh -s -- %s\n\n", base, t)
		fmt.Printf("Windows (PowerShell):\n  & ([scriptblock]::Create((irm %s/install.ps1))) %s\n\n", base, t)
		fmt.Printf("If tote is already installed there:\n  tote guest %s\n\n", t)
	} else {
		fmt.Print("\nOn the other computer run:\n\n")
		fmt.Printf("  tote guest %s\n\n", t)
	}
	fmt.Printf("PIN: %s   ← send this a DIFFERENT way (say it, or a separate message)\n", p.PIN)
	if !t.Exp.IsZero() {
		if t.Del != "" {
			fmt.Printf("Works until %s. One good download and it's deleted.\n", t.Exp.Local().Format("2 Jan 15:04"))
		} else {
			fmt.Printf("Works until %s. Deleted within an hour after it's collected, or when it expires.\n", t.Exp.Local().Format("2 Jan 15:04"))
		}
	}
	return nil
}

func sum(m map[string]int) (n int) {
	for _, v := range m {
		n += v
	}
	return
}

// learnAdapters validates tool descriptions an AI wrote into adapters/ and
// saves them in this computer's tote settings for next time.
func learnAdapters(src string) ([]string, error) {
	files, _ := filepath.Glob(filepath.Join(src, "adapters", "*.json"))
	cdir, err := config.Dir()
	if err != nil {
		return nil, err
	}
	var names []string
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		a, err := adapter.Parse(b)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", f, err)
		}
		if strings.TrimSuffix(filepath.Base(f), ".json") != a.Name {
			return nil, fmt.Errorf("%s describes %q — the file name must match", f, a.Name)
		}
		if builtin, err := adapter.Load(a.Name, ""); err == nil && !builtin.Generic {
			continue // tote's own description wins
		}
		dst := filepath.Join(cdir, "adapters", a.Name+".json")
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			return nil, err
		}
		if err := os.WriteFile(dst, b, 0o600); err != nil {
			return nil, err
		}
		names = append(names, a.Display)
	}
	return names, nil
}

func cmdGet(args []string) error {
	fs := flag.NewFlagSet("get", flag.ContinueOnError)
	pin := fs.String("pin", "", "the 6-digit PIN (asked for if not given)")
	dest := fs.String("d", "tote-box", "folder to unpack into")
	keep := fs.Bool("keep", false, "don't delete the box from the mailbox after opening")
	raw, err := parse(fs, args)
	if err != nil {
		return err
	}
	t, err := mailbox.ParseTicket(raw)
	if err != nil {
		return err
	}
	if *pin == "" {
		fmt.Print("PIN: ")
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		*pin = strings.TrimSpace(line)
	}
	if !lock.ValidPIN(*pin) {
		return errors.New("the PIN is 6 digits")
	}
	ctx := context.Background()
	rc, size, err := mailbox.Fetch(ctx, t)
	if err != nil {
		return err
	}
	if size > 0 {
		fmt.Printf("Downloading %s…\n", human(size))
	}
	m, err := core.OpenReader(rc, t.Key, *pin, *dest)
	rc.Close()
	if errors.Is(err, lock.ErrWrongCode) {
		return errors.New("wrong PIN (or the ticket doesn't belong to this PIN)")
	}
	if err != nil {
		return err
	}
	fmt.Printf("Opened %d files (%s) into %s — every file checked.\n", len(m.Files), human(m.TotalSize()), *dest)
	if m.Source.HostLabel != "" {
		fmt.Printf("Sent from: %s\n", m.Source.HostLabel)
	}
	if !*keep {
		if err := mailbox.Remove(ctx, t); err != nil {
			fmt.Printf("(Couldn't delete it from the mailbox: %v — it will expire on its own.)\n", err)
		}
	}
	return nil
}

func cmdMailbox(args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if len(args) == 0 || args[0] == "show" {
		return showMailbox(cfg)
	}
	if args[0] != "use" || len(args) < 2 {
		return errors.New("usage: tote mailbox [show] | use github [owner/repo] | use hosted [url] | use folder <dir> | use s3 --endpoint … --bucket … --access-key …")
	}
	switch args[1] {
	case "github":
		g := mailbox.GitHub{}
		for _, a := range args[2:] {
			switch {
			case a == "--token-stdin":
				b, _ := io.ReadAll(io.LimitReader(os.Stdin, 4096))
				g.Token = strings.TrimSpace(string(b))
				if g.Token == "" {
					return errors.New("no token on stdin — try:  gh auth token | tote mailbox use github --token-stdin")
				}
			case !strings.HasPrefix(a, "-"):
				g.Repo = a
			}
		}
		created, err := g.EnsureRepo(context.Background())
		if err != nil {
			return err
		}
		if created {
			fmt.Printf("Made your mailbox: https://github.com/%s (public — boxes in it are locked)\n", g.Repo)
		}
		cfg.Mailbox, cfg.GitHub = "github", &g
	case "hosted":
		cfg.Mailbox, cfg.Hosted = "hosted", nil
		if len(args) > 2 {
			cfg.Hosted = &mailbox.Hosted{URL: args[2]}
		}
	case "folder":
		if len(args) != 3 {
			return errors.New("usage: tote mailbox use folder <dir>")
		}
		dir, err := filepath.Abs(args[2])
		if err != nil {
			return err
		}
		cfg.Mailbox, cfg.Folder = "folder", &mailbox.Folder{Dir: dir}
	case "s3":
		fs := flag.NewFlagSet("s3", flag.ContinueOnError)
		s := mailbox.S3{}
		fs.StringVar(&s.Endpoint, "endpoint", "", "e.g. <account-id>.r2.cloudflarestorage.com")
		fs.StringVar(&s.Bucket, "bucket", "", "bucket name")
		fs.StringVar(&s.Prefix, "prefix", "tote/", "folder inside the bucket")
		fs.StringVar(&s.Region, "region", "", "region (R2: leave empty)")
		fs.StringVar(&s.AccessKey, "access-key", "", "access key ID")
		if err := fs.Parse(args[2:]); err != nil {
			return err
		}
		s.SecretKey = os.Getenv("TOTE_S3_SECRET")
		if s.SecretKey == "" {
			fmt.Print("Secret access key: ")
			line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
			s.SecretKey = strings.TrimSpace(line)
		}
		if s.Endpoint == "" || s.Bucket == "" || s.AccessKey == "" || s.SecretKey == "" {
			return errors.New("s3 needs --endpoint, --bucket, --access-key and the secret key")
		}
		cfg.Mailbox, cfg.S3 = "s3", &s
	default:
		return fmt.Errorf("unknown mailbox %q (github, hosted, folder, s3)", args[1])
	}
	if err := config.Save(cfg); err != nil {
		return err
	}
	return showMailbox(cfg)
}

func showMailbox(cfg config.Config) error {
	switch cfg.Mailbox {
	case "folder":
		fmt.Printf("Mailbox: folder %s\n", cfg.Folder.Dir)
	case "github":
		fmt.Printf("Mailbox: your GitHub repo %s — boxes are locked release files (≤2 GB), swept when expired or collected\n", cfg.GitHub.Repo)
	case "s3":
		fmt.Printf("Mailbox: your bucket %s on %s (receivers get 24h links, never your keys)\n", cfg.S3.Bucket, cfg.S3.Endpoint)
	default:
		u := mailbox.DefaultHostedURL
		if cfg.Hosted != nil {
			u = cfg.Hosted.URL
		}
		if u == "" {
			u = "(not deployed yet)"
		}
		fmt.Printf("Mailbox: hosted %s — free, up to 360 MB, deleted after 24h\n", u)
	}
	return nil
}
