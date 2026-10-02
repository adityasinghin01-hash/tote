package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/adityasinghin01-hash/tote/internal/adapter"
	"github.com/adityasinghin01-hash/tote/internal/config"
	"github.com/adityasinghin01-hash/tote/internal/scrub"
)

func cmdPrepare(args []string) error {
	fs := flag.NewFlagSet("prepare", flag.ContinueOnError)
	tool := fs.String("tool", "claude", "which AIs to pack, comma-separated (any name works; unknown ones are packed by the AI itself)")
	days := fs.Int("days", 7, "summarise chats active in the last N days (0 = none)")
	raw := fs.Bool("raw", false, "also include the raw chat logs (large) for native resume")
	label := fs.String("label", "", "a name for this computer, shown on the other side")
	out := fs.String("o", "", "folder to prepare the box in (default: ~/tote-outbox/<tool>)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cdir, _ := config.Dir()
	var as []adapter.Adapter
	var shown, unknown []string
	for _, name := range strings.Split(*tool, ",") {
		name = strings.ToLower(strings.TrimSpace(name))
		if name == "" {
			continue
		}
		a, err := adapter.Load(name, filepath.Join(cdir, "adapters"))
		if err != nil {
			if !adapter.ValidName(name) {
				return fmt.Errorf("%q isn't a usable tool name (letters, digits, - and _)", name)
			}
			a = adapter.Generic(name)
			unknown = append(unknown, name)
		}
		as = append(as, a)
		shown = append(shown, a.Display)
	}
	if len(as) == 0 {
		return fmt.Errorf("say which AI to pack: --tool claude (or codex, opencode, kimi, any-name…)")
	}
	if *out == "" {
		h, _ := os.UserHomeDir()
		*out = filepath.Join(h, "tote-outbox", time.Now().Format("2006-01-02-1504"))
	}
	rep, err := adapter.PrepareMany(as, adapter.PrepareOptions{Out: *out, Days: *days, Raw: *raw, HostLabel: *label})
	if err != nil {
		return err
	}
	fmt.Printf("Prepared %s in %s (%s)\n\n", strings.Join(shown, " + "), rep.Out, human(rep.Bytes))
	for what, n := range rep.Carried {
		fmt.Printf("  %-32s %d files\n", what, n)
	}
	fmt.Printf("  %-32s %d chats\n", fmt.Sprintf("chat digests (last %d days)", *days), len(rep.Chats))
	if len(rep.Redacted) > 0 {
		var parts []string
		for _, k := range scrub.Kinds(rep.Redacted) {
			parts = append(parts, fmt.Sprintf("%d %s", rep.Redacted[k], k))
		}
		fmt.Printf("\nBlanked secrets: %s\n", strings.Join(parts, ", "))
	}
	if len(rep.NotYours) > 0 {
		fmt.Printf("Left out %d items owned by other users.\n", len(rep.NotYours))
	}
	if len(rep.Excluded) > 0 {
		fmt.Printf("Never packed: %s\n", strings.Join(first(rep.Excluded, 5), ", "))
	}
	fmt.Printf("\nThe other AI's first read: INDEX.md + memory index ≈ %d tokens, plus START-HERE.md and DONE.md.\n", rep.FirstRead/4)
	if len(unknown) > 0 {
		fmt.Printf("\ntote doesn't know %s yet — TASK.md asks your AI to pack it and describe it, so next time it's automatic.\n", strings.Join(unknown, ", "))
	}
	fmt.Printf("\nNext: ask your AI → \"Follow %s\"\n", filepath.Join(rep.Out, "TASK.md"))
	return nil
}
