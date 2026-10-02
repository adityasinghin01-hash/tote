package lock

import (
	"bytes"
	"io"
	"testing"
)

func TestSecretShape(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		k, p, err := NewSecret()
		if err != nil || !ValidKey(k) || !ValidPIN(p) || len(k) != 26 {
			t.Fatalf("bad secret %q %q %v", k, p, err)
		}
		if seen[k] {
			t.Fatal("key repeated")
		}
		seen[k] = true
	}
	for _, bad := range []string{"", "12345", "1234567", "12a456"} {
		if ValidPIN(bad) {
			t.Fatalf("%q accepted as PIN", bad)
		}
	}
	if ValidKey("short") || ValidKey("!!!!!!!!!!!!!!!!!!!!!!!!!!") {
		t.Fatal("bad key accepted")
	}
}

func TestSealUnseal(t *testing.T) {
	WorkFactor = 10
	k, p, _ := NewSecret()
	var buf bytes.Buffer
	w, err := Seal(&buf, k, p)
	if err != nil {
		t.Fatal(err)
	}
	w.Write([]byte("secret memory"))
	w.Close()
	if bytes.Contains(buf.Bytes(), []byte("secret memory")) {
		t.Fatal("plaintext visible in sealed output")
	}
	r, err := Unseal(bytes.NewReader(buf.Bytes()), k, p)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(r)
	if string(got) != "secret memory" {
		t.Fatalf("got %q", got)
	}
	k2, _, _ := NewSecret()
	if _, err := Unseal(bytes.NewReader(buf.Bytes()), k2, p); err != ErrWrongCode {
		t.Fatalf("wrong key: %v", err)
	}
}
