package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/adityasinghin01-hash/tote/internal/quiz"
)

const maxAttempts = 2

type attempt struct {
	When  time.Time `json:"when"`
	Right int       `json:"right"`
	Total int       `json:"total"`
}

// findBox: --box, else the newest guest folder's box, else the current folder.
func findBox(flagBox string) (string, error) {
	if flagBox != "" {
		return flagBox, nil
	}
	if st, err := latestGuest(); err == nil {
		return st.Box, nil
	}
	wd, _ := os.Getwd()
	if _, err := os.Stat(filepath.Join(wd, quiz.LockedName)); err == nil {
		return wd, nil
	}
	return "", errors.New("no box found — run inside the box folder or pass --box <folder>")
}

func cmdQuiz(args []string) error {
	fs := flag.NewFlagSet("quiz", flag.ContinueOnError)
	boxFlag := fs.String("box", "", "the opened box folder")
	answers := fs.String("answers", "", "file with your answers, one per line as `N. answer` (- for stdin)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	boxDir, err := findBox(*boxFlag)
	if err != nil {
		return err
	}
	b, err := os.ReadFile(filepath.Join(boxDir, quiz.LockedName))
	if err != nil {
		return errors.New("this box has no proof quiz (the sender didn't write quiz.json)")
	}
	l, err := quiz.Parse(b)
	if err != nil {
		return err
	}
	attemptsPath := filepath.Join(boxDir, ".quiz-attempts.json")
	var past []attempt
	if ab, err := os.ReadFile(attemptsPath); err == nil {
		json.Unmarshal(ab, &past)
	}

	if *answers == "" {
		fmt.Printf("Proof quiz — %d questions about what you were handed.\n", len(l.Questions))
		fmt.Println("Answer from what you've read in the box; short answers are best (a name, number, path, yes/no).")
		fmt.Println()
		for _, q := range l.Questions {
			fmt.Printf("%d. %s\n", q.ID, q.Q)
		}
		fmt.Printf("\nWrite your answers to a file, one per line like `1. answer`, then run:\n  %s quiz --answers <file>\n", selfName())
		fmt.Printf("You get %d attempts; the first one is your score.\n", maxAttempts)
		return nil
	}

	if len(past) >= maxAttempts {
		return fmt.Errorf("no attempts left — first score was %d/%d", past[0].Right, past[0].Total)
	}
	var text []byte
	if *answers == "-" {
		text, err = io.ReadAll(os.Stdin)
	} else {
		text, err = os.ReadFile(*answers)
	}
	if err != nil {
		return err
	}
	replies := quiz.ParseReplies(string(text))
	if len(replies) == 0 {
		return errors.New("no answers found — write one per line like `1. answer`")
	}
	r := quiz.Grade(l, replies)
	past = append(past, attempt{When: time.Now().UTC(), Right: r.Right, Total: r.Total})
	ab, _ := json.MarshalIndent(past, "", "  ")
	os.WriteFile(attemptsPath, ab, 0o600)

	var out strings.Builder
	first := past[0]
	fmt.Fprintf(&out, "Context received: %d/%d (%d%%)", first.Right, first.Total, (first.Right*100+first.Total/2)/first.Total)
	if len(past) > 1 {
		fmt.Fprintf(&out, " — retry after reading: %d/%d", r.Right, r.Total)
	}
	out.WriteString("\n\n")
	for _, m := range r.Marks {
		mark := "✓"
		if !m.Right {
			mark = "✗"
		}
		fmt.Fprintf(&out, "%s %d. %s\n", mark, m.ID, m.Q)
		if !m.Right {
			reply := m.Reply
			if reply == "" {
				reply = "(no answer)"
			}
			fmt.Fprintf(&out, "     you said: %s\n", reply)
			if m.Source != "" {
				if _, err := os.Stat(filepath.Join(boxDir, filepath.FromSlash(m.Source))); err != nil {
					fmt.Fprintf(&out, "     answer is in %s — which is NOT in this box (the sender left it out)\n", m.Source)
				} else {
					fmt.Fprintf(&out, "     read: %s\n", m.Source)
				}
			}
		}
	}
	if r.Right < r.Total && len(past) < maxAttempts {
		fmt.Fprintf(&out, "\nRead the files above, then you have %d more attempt.\n", maxAttempts-len(past))
	}
	fmt.Print(out.String())
	os.WriteFile(filepath.Join(boxDir, "QUIZ-RESULT.md"), []byte("# Proof quiz\n\n"+out.String()), 0o600)
	return nil
}
