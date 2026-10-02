package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/adityasinghin01-hash/tote/internal/homecoming"
)

func cmdSendHome(args []string) error {
	fs := flag.NewFlagSet("send-home", flag.ContinueOnError)
	label := fs.String("label", "", "a name for this computer, shown at home (e.g. lab)")
	out := fs.String("o", "", "folder to collect into (default: inside the guest folder)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	byName, _ := adapters()
	st, err := latestGuest()
	if err != nil {
		return err
	}
	if *out == "" {
		*out = filepath.Join(st.Root, "outbox-"+time.Now().Format("150405"))
	}
	if *label == "" {
		*label = "guest computer"
	}
	rep, err := homecoming.SendHome(st, byName, *out, *label)
	if err != nil {
		return err
	}
	fmt.Printf("Collected what changed here into %s\n\n", rep.Out)
	for t := range byName {
		if rep.Changed[t]+rep.Added[t] > 0 {
			fmt.Printf("  %-10s %d changed, %d new files\n", t, rep.Changed[t], rep.Added[t])
		}
	}
	fmt.Printf("  chats      %d (summaries + the full logs, so they can be resumed at home)\n", rep.Chats)
	fmt.Printf("\nNext: ask your AI → \"Follow %s\"\n", filepath.Join(rep.Out, "TASK.md"))
	fmt.Printf("It writes a short note, then runs:  %s send %s\n", selfName(), rep.Out)
	return nil
}

func cmdMerge(args []string) error {
	fs := flag.NewFlagSet("merge", flag.ContinueOnError)
	pin := fs.String("pin", "", "the 6-digit PIN (asked for if not given)")
	yes := fs.Bool("yes", false, "don't ask before merging")
	src, err := parse(fs, args)
	if err != nil {
		return err
	}
	tmp, err := os.MkdirTemp("", "tote-merge-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	boxDir := filepath.Join(tmp, "box")
	_, t, err := fetchBox(src, *pin, boxDir)
	if err != nil {
		return err
	}
	byName, _ := adapters()
	home, _ := os.UserHomeDir()
	p, err := homecoming.PlanMerge(boxDir, byName, filepath.Join(home, "tote-inbox"))
	if err != nil {
		return err
	}
	from := p.Return.From
	if from == "" {
		from = "the other computer"
	}
	fmt.Printf("Work from %s:\n\n", from)
	fmt.Printf("  %3d new files          → added\n", p.Count("add"))
	fmt.Printf("  %3d changed over there → updated (your copy backed up first)\n", p.Count("update"))
	fmt.Printf("  %3d changed on BOTH    → both kept side by side for your AI to combine\n", p.Count("conflict"))
	fmt.Printf("  %3d new chats          → added (resume them here)\n", p.Count("chat"))
	fmt.Printf("  %3d unchanged\n", p.Count("same"))
	if !p.Known {
		fmt.Println("\n  (This computer didn't send the original box, so every changed file is kept side by side.)")
	}
	if len(p.Unknown) > 0 {
		fmt.Printf("\n  Not merged (tote doesn't know %v here) — saved in the inbox.\n", p.Unknown)
	}
	if !asker(*yes)("\nMerge now? Nothing is deleted.", true) {
		return errors.New("nothing merged")
	}
	if err := homecoming.Apply(p); err != nil {
		return err
	}
	if t != nil {
		_ = removeTicket(t)
	}
	fmt.Printf("\nDone. The note from over there and a report are in:\n  %s\n", p.Inbox)
	fmt.Printf("Ask your AI → \"Read %s and %s, then combine anything it lists.\"\n",
		filepath.Join(p.Inbox, "START-HERE.md"), filepath.Join(p.Inbox, "MERGE-REPORT.md"))
	return nil
}
