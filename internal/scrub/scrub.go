// Package scrub blanks secrets (API keys, tokens, private keys, passwords)
// out of text before it is packed. Chat logs are full of command output, and
// command output is where keys leak.
package scrub

import (
	"bytes"
	"regexp"
	"sort"
	"unicode/utf8"
)

type rule struct {
	kind string
	re   *regexp.Regexp
	keep int // capture group to keep (e.g. the "password=" part), 0 = none
}

var rules = []rule{
	{"private-key", regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*?-----END [A-Z ]*PRIVATE KEY-----`), 0},
	{"anthropic", regexp.MustCompile(`\bsk-ant-[A-Za-z0-9_\-]{20,}`), 0},
	{"openai", regexp.MustCompile(`\bsk-(?:proj-|svcacct-|admin-)?[A-Za-z0-9_\-]{32,}`), 0},
	{"github", regexp.MustCompile(`\b(?:gh[pousr]_[A-Za-z0-9]{36,}|github_pat_[A-Za-z0-9_]{40,})`), 0},
	{"aws-key-id", regexp.MustCompile(`\b(?:AKIA|ASIA)[A-Z0-9]{16}\b`), 0},
	{"google", regexp.MustCompile(`\bAIza[0-9A-Za-z_\-]{35}`), 0},
	{"slack", regexp.MustCompile(`\bxox[abposr]-[A-Za-z0-9\-]{10,}`), 0},
	{"stripe", regexp.MustCompile(`\b[rs]k_(?:live|test)_[A-Za-z0-9]{20,}`), 0},
	{"jwt", regexp.MustCompile(`\beyJ[A-Za-z0-9_\-]{10,}\.eyJ[A-Za-z0-9_\-]{10,}\.[A-Za-z0-9_\-]{10,}`), 0},
	{"url-password", regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.\-]*://[^\s:/@]+:)[^\s@/]{3,}(@)`), 1},
	// KEY=value / "key": "value" where the name says secret
	{"named-secret", regexp.MustCompile(`(?i)((?:api[_-]?key|secret|token|passw(?:or)?d|pwd|access[_-]?key|credential|private[_-]?key)[A-Za-z0-9_\-]*["']?\s*[:=]\s*["']?)([^\s"',;]{12,})`), 1},
}

// Mode chooses how aggressive scrubbing is.
type Mode int

const (
	// KeysOnly blanks only values in known key formats. Safe for code and
	// skills, where "password: req.body.password" must survive.
	KeysOnly Mode = iota
	// Full also blanks secret-looking values after names like password= and
	// passwords inside URLs. For chats and notes, where leaks actually happen.
	Full
)

// heuristic rules only run in Full mode.
var heuristic = map[string]bool{"named-secret": true, "url-password": true}

// looksRandom: a real secret mixes letters and digits and isn't code like
// req.body.password or process.env.X.
func looksRandom(v []byte) bool {
	var letters, digits bool
	for _, c := range v {
		switch {
		case c >= '0' && c <= '9':
			digits = true
		case c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z':
			letters = true
		case c == '.' || c == '(' || c == ')' || c == '[' || c == '{' || c == '$' || c == '<':
			return false
		}
	}
	return letters && digits
}

// Text returns s with secrets replaced by [REDACTED:kind] and a count per kind.
func Text(s []byte, mode Mode) ([]byte, map[string]int) {
	counts := map[string]int{}
	for _, r := range rules {
		if heuristic[r.kind] && mode != Full {
			continue
		}
		s = r.re.ReplaceAllFunc(s, func(m []byte) []byte {
			if bytes.HasPrefix(m, []byte("[REDACTED")) {
				return m
			}
			sub := r.re.FindSubmatch(m)
			if r.kind == "named-secret" && (bytes.HasPrefix(sub[2], []byte("[REDACTED")) || !looksRandom(sub[2])) {
				return m
			}
			counts[r.kind]++
			tag := []byte("[REDACTED:" + r.kind + "]")
			if r.keep == 0 {
				return tag
			}
			out := append([]byte{}, sub[r.keep]...)
			out = append(out, tag...)
			if r.kind == "url-password" {
				out = append(out, sub[2]...)
			}
			return out
		})
	}
	for k, v := range counts {
		if v == 0 {
			delete(counts, k)
		}
	}
	return s, counts
}

// IsText guesses whether b is text worth scrubbing (valid UTF-8, no NULs).
func IsText(b []byte) bool {
	if bytes.IndexByte(b, 0) >= 0 {
		return false
	}
	sample := b
	if len(sample) > 8192 {
		sample = sample[:8192]
		for len(sample) > 0 && !utf8.Valid(sample) { // don't cut a rune in half
			sample = sample[:len(sample)-1]
		}
	}
	return utf8.Valid(sample)
}

// Kinds returns the kinds in counts, sorted, for stable reports.
func Kinds(counts map[string]int) []string {
	var k []string
	for x := range counts {
		k = append(k, x)
	}
	sort.Strings(k)
	return k
}
