package quiz

import (
	"strings"
	"testing"
)

const sample = `{"questions":[
 {"q":"Which city is the pilot in?","answers":["Lisbon"],"source":"memory/pilot.md"},
 {"q":"Price per factory per month?","answers":["1500","₹1,500"],"source":"memory/pilot.md"},
 {"q":"Which Go binary must be used?","answers":["~/sdk/go/bin/go"],"source":"START-HERE.md"},
 {"q":"Is the repo public?","answers":["no"],"source":"DONE.md"}
]}`

func lock(t *testing.T, s string) Locked {
	t.Helper()
	b, err := Lock([]byte(s))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "Lisbon") || strings.Contains(string(b), "1500") || strings.Contains(string(b), "sdk/go") {
		t.Fatal("an answer leaked into quiz.lock")
	}
	l, err := Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func TestGradeFairly(t *testing.T) {
	l := lock(t, sample)
	r := Grade(l, map[int]string{
		1: "The pilot is in Lisbon, Portugal (backup Porto).",
		2: "₹1,500 per factory per month",
		3: "`~/sdk/go/bin/go` because /usr/local is Intel",
		4: "No — local commits only.",
	})
	if r.Right != 4 || r.Percent() != 100 {
		for _, m := range r.Marks {
			t.Logf("%d %v %q", m.ID, m.Right, m.Reply)
		}
		t.Fatalf("score %d/4", r.Right)
	}
	r = Grade(l, map[int]string{1: "Porto", 2: "15000", 3: "/usr/local/bin/go", 4: "yes"})
	if r.Right != 0 {
		t.Fatalf("wrong answers scored %d", r.Right)
	}
	if r.Marks[0].Source != "memory/pilot.md" {
		t.Fatal("source lost")
	}
}

func TestStuffingDoesntPay(t *testing.T) {
	l := lock(t, sample)
	guesses := strings.Repeat("maybe something else entirely ", 10) + "Lisbon"
	if Grade(l, map[int]string{1: guesses}).Right != 0 {
		t.Fatal("answer hidden after 40 words was accepted")
	}
}

func TestLockRejectsBadQuizzes(t *testing.T) {
	bad := []string{
		`{"questions":[]}`,
		`{"questions":[{"q":"a","answers":["x"]},{"q":"b","answers":["y"]}]}`, // too few
		`{"questions":[{"q":"a","answers":[]},{"q":"b","answers":["y"]},{"q":"c","answers":["z"]}]}`,
		`{"questions":[{"q":"a","answers":["one two three four five six seven eight nine"]},{"q":"b","answers":["y"]},{"q":"c","answers":["z"]}]}`,
		`{"questions":[{"q":"Is the site Lisbon?","answers":["Lisbon"]},{"q":"b","answers":["y"]},{"q":"c","answers":["z"]}]}`, // gives it away
		`{"questions":[{"q":"a","answers":["x"],"extra":1},{"q":"b","answers":["y"]},{"q":"c","answers":["z"]}]}`,
		`not json`,
	}
	for _, s := range bad {
		if _, err := Lock([]byte(s)); err == nil {
			t.Errorf("accepted bad quiz: %s", s)
		}
	}
}

func TestNormalize(t *testing.T) {
	for in, want := range map[string]string{
		"₹1,500.":           "1500",
		"Lisbon, Portugal":  "lisbon portugal",
		"`~/sdk/go/bin/go`": "~/sdk/go/bin/go",
		"2.1.144":           "2.1.144",
		`"tools/deploy.sh"`: "tools/deploy.sh",
		"  Done.  ":         "done",
	} {
		if got := Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseReplies(t *testing.T) {
	r := ParseReplies("1. Lisbon\n2) 1500\nQ3: ~/sdk/go/bin/go\nnoise\n4 - no\n1. second try ignored")
	if r[1] != "Lisbon" || r[2] != "1500" || r[3] != "~/sdk/go/bin/go" || r[4] != "no" || len(r) != 4 {
		t.Fatalf("%v", r)
	}
}
