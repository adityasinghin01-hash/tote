package mailbox

import (
	"bytes"
	"context"
	"crypto/rand"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/johannesboyne/gofakes3"
	"github.com/johannesboyne/gofakes3/backend/s3mem"
)

func boxFile(t *testing.T, n int) (string, []byte) {
	t.Helper()
	b := make([]byte, n)
	rand.Read(b)
	p := filepath.Join(t.TempDir(), "x.tote")
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return p, b
}

// roundTrip drops, fetches, checks bytes, removes, and checks it's gone.
func roundTrip(t *testing.T, mb Mailbox, size int) Ticket {
	t.Helper()
	ctx := context.Background()
	p, want := boxFile(t, size)
	tk, err := mb.Drop(ctx, p, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	tk.Key = "testkey"
	tk2, err := ParseTicket(tk.String())
	if err != nil || tk2.Get != tk.Get || tk2.Del != tk.Del || tk2.Key != "testkey" || !tk2.Exp.Equal(tk.Exp) {
		t.Fatalf("ticket didn't survive encoding: %v", err)
	}
	if strings.ContainsAny(tk.String(), " \"'\n") {
		t.Fatal("ticket has characters that break pasting")
	}
	rc, n, err := Fetch(ctx, tk2)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(rc)
	rc.Close()
	if !bytes.Equal(got, want) {
		t.Fatalf("%s: got %d bytes, want %d", mb.Kind(), len(got), len(want))
	}
	if n > 0 && n != int64(size) {
		t.Fatalf("reported size %d, want %d", n, size)
	}
	if err := Remove(ctx, tk2); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Fetch(ctx, tk2); err == nil {
		t.Fatal("box still there after Remove")
	}
	return tk2
}

func TestFolder(t *testing.T) {
	tk := roundTrip(t, Folder{Dir: filepath.Join(t.TempDir(), "drive")}, 1<<20)
	if !strings.HasPrefix(tk.Get, "file:///") {
		t.Fatalf("folder link = %s", tk.Get)
	}
}

func TestS3PresignedLinks(t *testing.T) {
	backend := s3mem.New()
	backend.CreateBucket("boxes")
	srv := httptest.NewServer(gofakes3.New(backend).Server())
	defer srv.Close()
	mb := S3{Endpoint: strings.TrimPrefix(srv.URL, "http://"), Bucket: "boxes", Prefix: "tote/",
		AccessKey: "AKIDEXAMPLE", SecretKey: "secret", Insecure: true}
	tk := roundTrip(t, mb, 3<<20)
	if strings.Contains(tk.String(), "secret") {
		t.Fatal("secret key leaked into ticket")
	}
	if !strings.Contains(tk.Get, "X-Amz-Signature=") || tk.Exp.IsZero() {
		t.Fatalf("expected a presigned, expiring link: %s", tk.Get)
	}
}

func TestExpiredTicketRefused(t *testing.T) {
	_, _, err := Fetch(context.Background(), Ticket{V: 1, Get: "https://example.invalid/x", Key: "k", Exp: time.Now().Add(-time.Minute)})
	if err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("got %v", err)
	}
}

func TestBadTickets(t *testing.T) {
	for _, s := range []string{"", "hello", "tote1_!!!", "tote1_e30"} { // e30 = "{}"
		if _, err := ParseTicket(s); err == nil {
			t.Fatalf("%q accepted", s)
		}
	}
}

// TestHostedRelay runs against a real relay: `npx wrangler dev` in relay/,
// then TOTE_RELAY_URL=http://localhost:8787 go test ./internal/mailbox/
func TestHostedRelay(t *testing.T) {
	base := os.Getenv("TOTE_RELAY_URL")
	if base == "" {
		t.Skip("TOTE_RELAY_URL not set")
	}
	mb := Hosted{URL: base}
	roundTrip(t, mb, 1<<20)       // one part
	roundTrip(t, mb, 200<<20+123) // three parts, uneven last part

	p, _ := boxFile(t, 361<<20) // over the cap
	if _, err := mb.Drop(context.Background(), p, 0); err == nil || !strings.Contains(err.Error(), "own bucket") {
		t.Fatalf("oversize box: %v", err)
	}
}
