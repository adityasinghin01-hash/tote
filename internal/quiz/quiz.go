// Package quiz is tote's proof that a handoff worked. The sending AI writes
// questions with short exact answers (quiz.json); tote locks the answers into
// fingerprints (quiz.lock) so the receiving AI can't read them; the receiving
// AI answers through `tote quiz`, and tote — not the AI — grades it.
//
// Threat model: this catches an AI that skimmed or skipped the box, not one
// actively trying to cheat. Fingerprints are salted SHA-256, so an AI that
// set out to guess short answers offline could; the point is a fair score
// for an honest reader, measured outside the AI.
package quiz

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode"
)

const (
	PlainName  = "quiz.json"
	LockedName = "quiz.lock"
	Format     = "tote-quiz/1"

	minQuestions  = 3
	maxQuestions  = 30
	maxAnswerLen  = 8  // words in an accepted answer: keep them specific
	maxReplyWords = 40 // words of a reply that are looked at
)

// Question is what the sending AI writes.
type Question struct {
	Q       string   `json:"q"`
	Answers []string `json:"answers"`          // accepted answers, e.g. ["Lisbon", "Lisbon, Portugal"]
	Source  string   `json:"source,omitempty"` // file in the box where the answer is
}

type Plain struct {
	Questions []Question `json:"questions"`
}

// LockedQ is what travels: the question, never the answer.
type LockedQ struct {
	ID       int      `json:"id"`
	Q        string   `json:"q"`
	Hashes   []string `json:"hashes"`
	MaxWords int      `json:"max_words"`
	Source   string   `json:"source,omitempty"`
}

type Locked struct {
	Format    string    `json:"format"`
	Salt      string    `json:"salt"`
	Questions []LockedQ `json:"questions"`
}

var (
	numComma = regexp.MustCompile(`(\d),(\d)`)
	spaces   = regexp.MustCompile(`\s+`)
)

// Normalize makes "₹1,500." and "1500" or "Lisbon, Portugal" and "lisbon portugal" compare equal.
func Normalize(s string) string {
	s = strings.ToLower(s)
	for numComma.MatchString(s) {
		s = numComma.ReplaceAllString(s, "$1$2")
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r) || r == '/' || r == '.' || r == '-' || r == '_' || r == '~' || r == '@':
			b.WriteRune(r)
		default:
			b.WriteRune(' ') // punctuation, currency, quotes, backticks → space
		}
	}
	words := strings.Fields(b.String())
	for i, w := range words { // "done." → "done", but keep "~/dev/x" and "2.1.144"
		words[i] = strings.TrimRight(w, ".-")
	}
	return strings.TrimSpace(spaces.ReplaceAllString(strings.Join(words, " "), " "))
}

func fingerprint(salt, norm string) string {
	h := sha256.Sum256([]byte(salt + "\x00" + norm))
	return hex.EncodeToString(h[:])
}

// Lock checks quiz.json and turns it into quiz.lock.
func Lock(plainJSON []byte) ([]byte, error) {
	var p Plain
	dec := json.NewDecoder(strings.NewReader(string(plainJSON)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return nil, fmt.Errorf("quiz.json isn't valid: %w", err)
	}
	if n := len(p.Questions); n < minQuestions || n > maxQuestions {
		return nil, fmt.Errorf("quiz.json needs %d–%d questions, has %d", minQuestions, maxQuestions, n)
	}
	saltB := make([]byte, 16)
	rand.Read(saltB)
	l := Locked{Format: Format, Salt: hex.EncodeToString(saltB)}
	for i, q := range p.Questions {
		if strings.TrimSpace(q.Q) == "" || len(q.Answers) == 0 {
			return nil, fmt.Errorf("quiz question %d has no text or no answer", i+1)
		}
		lq := LockedQ{ID: i + 1, Q: strings.TrimSpace(q.Q), Source: q.Source}
		for _, a := range q.Answers {
			n := Normalize(a)
			if n == "" {
				return nil, fmt.Errorf("quiz question %d has an empty answer", i+1)
			}
			w := len(strings.Fields(n))
			if w > maxAnswerLen {
				return nil, fmt.Errorf("quiz question %d: answer %q is too long — use a name, number or path (≤%d words)", i+1, a, maxAnswerLen)
			}
			if strings.Contains(strings.ToLower(lq.Q), n) && len(n) > 3 {
				return nil, fmt.Errorf("quiz question %d gives away its own answer", i+1)
			}
			lq.Hashes = append(lq.Hashes, fingerprint(l.Salt, n))
			lq.MaxWords = max(lq.MaxWords, w)
		}
		l.Questions = append(l.Questions, lq)
	}
	return json.MarshalIndent(l, "", "  ")
}

// Parse reads quiz.lock.
func Parse(b []byte) (Locked, error) {
	var l Locked
	if err := json.Unmarshal(b, &l); err != nil || l.Format != Format || len(l.Questions) == 0 {
		return l, errors.New("quiz.lock is missing or damaged")
	}
	return l, nil
}

// Mark is one graded answer.
type Mark struct {
	ID     int
	Q      string
	Reply  string
	Right  bool
	Source string
}

type Result struct {
	Marks []Mark
	Right int
	Total int
}

func (r Result) Percent() int {
	if r.Total == 0 {
		return 0
	}
	return (r.Right*100 + r.Total/2) / r.Total
}

// Grade marks replies (question ID → reply). A reply is right if any run of
// up to MaxWords consecutive words in it matches an accepted answer, so
// "the pilot is Lisbon, Portugal" counts for "Lisbon". Only the first
// maxReplyWords words are looked at, so stuffing a reply with guesses fails.
func Grade(l Locked, replies map[int]string) Result {
	res := Result{Total: len(l.Questions)}
	for _, q := range l.Questions {
		reply := replies[q.ID]
		m := Mark{ID: q.ID, Q: q.Q, Reply: reply, Source: q.Source}
		m.Right = matches(l.Salt, q, reply)
		if m.Right {
			res.Right++
		}
		res.Marks = append(res.Marks, m)
	}
	return res
}

func matches(salt string, q LockedQ, reply string) bool {
	words := strings.Fields(Normalize(reply))
	if len(words) > maxReplyWords {
		words = words[:maxReplyWords]
	}
	want := map[string]bool{}
	for _, h := range q.Hashes {
		want[h] = true
	}
	for n := 1; n <= q.MaxWords; n++ {
		for i := 0; i+n <= len(words); i++ {
			if want[fingerprint(salt, strings.Join(words[i:i+n], " "))] {
				return true
			}
		}
	}
	return false
}

var replyLine = regexp.MustCompile(`^\s*(?:Q|q)?(\d{1,2})\s*[.):\-]\s*(.*)$`)

// ParseReplies reads "1. answer" / "2) answer" / "Q3: answer" lines.
func ParseReplies(text string) map[int]string {
	out := map[int]string{}
	for _, line := range strings.Split(text, "\n") {
		if m := replyLine.FindStringSubmatch(line); m != nil {
			var id int
			fmt.Sscan(m[1], &id)
			if _, dup := out[id]; !dup {
				out[id] = strings.TrimSpace(m[2])
			}
		}
	}
	return out
}
