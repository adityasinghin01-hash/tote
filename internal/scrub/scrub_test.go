package scrub

import (
	"strings"
	"testing"
)

// fake joins pieces at run time so no key-shaped text sits in the source
// (secret scanners such as GitHub push protection rightly reject it).
func fake(a, b string) string { return a + b }

func TestBlanksPlantedSecrets(t *testing.T) {
	planted := map[string]string{
		"anthropic":    "key " + fake("sk-ant-", "api03-AbCdEfGhIjKlMnOpQrStUvWxYz0123456789") + " here",
		"openai":       "OPENAI " + fake("sk-", "proj-abcdefghijklmnopqrstuvwxyz0123456789ABCD"),
		"github":       "token " + fake("gh", "p_abcdefghijklmnopqrstuvwxyz0123456789AB"),
		"aws-key-id":   "id AKIAIOSFODNN7EXAMPLE",
		"google":       fake("AI", "zaSyA-abcdefghijklmnopqrstuvwxyz012345"),
		"slack":        fake("xo", "xb-1234567890-abcdefghij"),
		"stripe":       fake("sk_", "live_abcdefghijklmnopqrstuvwx"),
		"jwt":          fake("eyJhbGciOiJIUzI1NiJ9", ".eyJzdWIiOiIxMjM0NTY3ODkwIn0.abcdefghijklmnop"),
		"url-password": "postgres://admin:hunter2secret@db.example.com/x",
		"named-secret": `DB_PASSWORD="c0rrect-h0rse-battery"`,
		"private-key":  "-----BEGIN OPENSSH PRIVATE KEY-----\nAAAAB3NzaC1yc2E\n-----END OPENSSH PRIVATE KEY-----",
	}
	for kind, in := range planted {
		out, counts := Text([]byte(in), Full)
		if counts[kind] == 0 {
			t.Errorf("%s not caught: %q → %q (%v)", kind, in, out, counts)
		}
		for _, secret := range []string{"AbCdEfGhIj", "abcdefghijklmnop", "IOSFODNN7", "hunter2", "c0rrect-h0rse", "AAAAB3Nz"} {
			if strings.Contains(in, secret) && strings.Contains(string(out), secret) {
				t.Errorf("%s: %q still in output %q", kind, secret, out)
			}
		}
	}
}

func TestKeepsContext(t *testing.T) {
	out, _ := Text([]byte(`DB_PASSWORD="c0rrect-h0rse-battery"`), Full)
	if !strings.HasPrefix(string(out), `DB_PASSWORD="[REDACTED`) {
		t.Fatalf("lost the variable name: %q", out)
	}
	out, _ = Text([]byte("postgres://admin:hunter2secret@db.example.com/x"), Full)
	if string(out) != "postgres://admin:[REDACTED:url-password]@db.example.com/x" {
		t.Fatalf("url: %q", out)
	}
}

func TestLeavesOrdinaryTextAlone(t *testing.T) {
	for _, s := range []string{
		"The token budget is about 8K per session.",
		"password reset flow sends an email",
		"see https://github.com/schollz/croc",
		"sk-learn is a library",
		"https://electrek.co/2025/12/30/elon-musk-tesla-something-very-long-slug-here-2026",
		"password: req.body.password",
		"JWT_SECRET:    process.env.JWT_SECRET",
		"author: Aditya Singh",
		`"tokens": tokensUsedSoFar`,
	} {
		if out, c := Text([]byte(s), Full); string(out) != s {
			t.Errorf("changed ordinary text %q → %q (%v)", s, out, c)
		}
	}
}

func TestIdempotent(t *testing.T) {
	once, _ := Text([]byte(fake("sk-ant-", "api03-AbCdEfGhIjKlMnOpQrStUvWxYz0123456789")), Full)
	twice, c := Text(once, Full)
	if string(once) != string(twice) || len(c) != 0 {
		t.Fatalf("second pass changed things: %q %v", twice, c)
	}
}

func TestKeysOnlyModeLeavesCodeWorking(t *testing.T) {
	code := `const db = "postgres://postgres:postgres123@localhost/app"; const pw = { password: "hunter2hunter2x9" }`
	out, c := Text([]byte(code), KeysOnly)
	if string(out) != code || len(c) != 0 {
		t.Fatalf("KeysOnly changed code: %q %v", out, c)
	}
	out, _ = Text([]byte("key = "+fake("sk-ant-", "api03-AbCdEfGhIjKlMnOpQrStUvWxYz0123456789")), KeysOnly)
	if strings.Contains(string(out), "AbCdEf") {
		t.Fatal("KeysOnly missed a real key")
	}
}
