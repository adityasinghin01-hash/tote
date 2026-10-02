// tote moves an AI's memory, skills and chats to any computer.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/adityasinghin01-hash/tote/internal/box"
	"github.com/adityasinghin01-hash/tote/internal/core"
	"github.com/adityasinghin01-hash/tote/internal/lock"
)

var version = "0.1.0-dev"

// installBase is where install.sh / install.ps1 and the release binaries
// live (set at release time; TOTE_INSTALL_BASE overrides for testing).
var installBase = ""

func installURL() string {
	if v := os.Getenv("TOTE_INSTALL_BASE"); v != "" {
		return strings.TrimRight(v, "/")
	}
	return strings.TrimRight(installBase, "/")
}

const usage = `tote — take your AI to any computer

Usage:
  tote prepare [--tool claude] [--days 7] [--raw]    gather your AI's files + chat digests for sending
  tote send <folder> [--label name]                 lock a folder and post it to your mailbox
  tote get <ticket> [--pin PIN] [-d dir]            collect and unlock a box
  tote mailbox [use hosted|folder|s3 …]             see or change where boxes are posted

  tote guest <ticket|box folder> [--yes]             use your AI on someone else's computer, privately
  tote run [tool]                                   start your AI from the guest folder
  tote leave [--all]                                log out and wipe the guest folder
  tote status                                       show guest folders here
  tote quiz [--answers file]                        the receiving AI proves it read the box
  tote send-home [--label lab]                      (on the guest computer) collect what changed, to send back
  tote merge <ticket> [--yes]                       (at home) bring that work in without losing home edits

  tote pack <folder> [-o box.tote]                  lock a folder into a local box file
  tote open <box.tote> --key KEY --pin PIN [-d dir] unlock a local box file
  tote version
`

func main() {
	box.Version = version
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "pack":
		err = cmdPack(os.Args[2:])
	case "open":
		err = cmdOpen(os.Args[2:])
	case "prepare":
		err = cmdPrepare(os.Args[2:])
	case "send":
		err = cmdSend(os.Args[2:])
	case "get":
		err = cmdGet(os.Args[2:])
	case "guest":
		err = cmdGuest(os.Args[2:])
	case "run":
		err = cmdRun(os.Args[2:])
	case "leave":
		err = cmdLeave(os.Args[2:])
	case "send-home":
		err = cmdSendHome(os.Args[2:])
	case "merge":
		err = cmdMerge(os.Args[2:])
	case "quiz":
		err = cmdQuiz(os.Args[2:])
	case "status":
		err = cmdStatus()
	case "__reap":
		err = cmdReap(os.Args[2:])
	case "mailbox":
		err = cmdMailbox(os.Args[2:])
	case "version", "--version", "-v":
		fmt.Println("tote", version)
	case "help", "--help", "-h":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "tote: unknown command %q\n\n%s", os.Args[1], usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "tote:", err)
		os.Exit(1)
	}
}

// parse lets flags appear before or after the positional argument.
func parse(fs *flag.FlagSet, args []string) (string, error) {
	var pos []string
	for len(args) > 0 {
		if err := fs.Parse(args); err != nil {
			return "", err
		}
		args = fs.Args()
		if len(args) > 0 {
			pos = append(pos, args[0])
			args = args[1:]
		}
	}
	if len(pos) != 1 {
		return "", errors.New("expected exactly one path")
	}
	return pos[0], nil
}

func cmdPack(args []string) error {
	fs := flag.NewFlagSet("pack", flag.ContinueOnError)
	out := fs.String("o", "", "where to save the box (default: <folder>.tote)")
	label := fs.String("label", "", "a name for this computer, shown on the other side")
	noLinks := fs.Bool("no-follow", false, "skip shortcuts instead of packing what they point to")
	src, err := parse(fs, args)
	if err != nil {
		return err
	}
	src = filepath.Clean(src)
	if *out == "" {
		*out = src + ".tote"
	}
	home, _ := os.UserHomeDir()
	p, err := core.PackFile(src, *out, box.Source{Home: home, HostLabel: *label}, nil, box.Options{FollowLinks: !*noLinks})
	if err != nil {
		return err
	}
	fmt.Printf("Packed %d files (%s) into %s (%s)\n\n",
		len(p.Manifest.Files), human(p.Manifest.TotalSize()), p.Path, human(p.Bytes))
	fmt.Printf("  key: %s\n  PIN: %s   ← send this a different way\n", p.Key, p.PIN)
	if n := len(p.Report.Followed); n > 0 {
		fmt.Printf("\nFollowed %d shortcuts and packed the real files: %s\n", n, strings.Join(first(p.Report.Followed, 5), ", "))
	}
	if n := len(p.Report.NotYours); n > 0 {
		fmt.Printf("\nLeft out %d items owned by other users on this computer: %s\n", n, strings.Join(first(p.Report.NotYours, 5), ", "))
	}
	if n := len(p.Report.Skipped); n > 0 {
		fmt.Printf("\nSkipped %d (broken/looping shortcuts, special files, unsafe names): %s\n", n, strings.Join(first(p.Report.Skipped, 5), ", "))
	}
	return nil
}

func cmdOpen(args []string) error {
	fs := flag.NewFlagSet("open", flag.ContinueOnError)
	key := fs.String("key", "", "the key from tote pack")
	pin := fs.String("pin", "", "the 6-digit PIN")
	dest := fs.String("d", "", "folder to unpack into (default: box name without .tote)")
	src, err := parse(fs, args)
	if err != nil {
		return err
	}
	if *dest == "" {
		*dest = strings.TrimSuffix(src, ".tote")
		if *dest == src {
			*dest = src + ".opened"
		}
	}
	m, err := core.OpenFile(src, strings.TrimSpace(*key), strings.TrimSpace(*pin), *dest)
	if errors.Is(err, lock.ErrWrongCode) {
		return errors.New("that key or PIN doesn't open this box")
	}
	if err != nil {
		return err
	}
	fmt.Printf("Opened %d files (%s) into %s — every file checked.\n", len(m.Files), human(m.TotalSize()), *dest)
	return nil
}

func human(n int64) string {
	const u = 1024
	if n < u {
		return fmt.Sprintf("%d B", n)
	}
	d, e := int64(u), 0
	for m := n / u; m >= u; m /= u {
		d *= u
		e++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(d), "KMGTPE"[e])
}

func first(s []string, n int) []string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
