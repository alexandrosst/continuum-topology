package server

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"continuum/internal/accounts"
	"continuum/internal/decisionloop"
	"continuum/internal/sealing"
)

const (
	e2eKeyA = "an-example-key-that-is-long-enough-A"
	e2eKeyB = "an-example-key-that-is-long-enough-B"
)

func e2eSealer(t *testing.T, keys ...string) *sealing.Sealer {
	t.Helper()
	var ks [][]byte
	for _, k := range keys {
		ks = append(ks, []byte(k))
	}
	s, err := sealing.New(ks...)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func filesHoldText(t *testing.T, dbPath, text string) bool {
	t.Helper()
	found := false
	entries, _ := os.ReadDir(filepath.Dir(dbPath))
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join(filepath.Dir(dbPath), e.Name()))
		if err == nil && bytes.Contains(b, []byte(text)) {
			found = true
		}
	}
	return found
}

// TestSealedSecretsStillWorkAndAreInNoFile covers the three secrets from the screen to the disk and back: the decider's secret still signs the
// requests to the decider, the mail password still reaches the relay's configuration, and neither is in any file of the database.
func TestSealedSecretsStillWorkAndAreInNoFile(t *testing.T) {
	a, admin, editor, _, _, _ := rigWithDecider(t, "127.0.0.0/8")
	sl := e2eSealer(t, e2eKeyA)
	if err := a.w.Store.UseSealer(context.Background(), sl); err != nil {
		t.Fatal(err)
	}
	a.base.Sealer = sl

	var gotSig, gotTS string
	var gotBody []byte
	dec := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotSig, gotTS = r.Header.Get("X-Continuum-Signature"), r.Header.Get("X-Continuum-Timestamp")
		gotBody, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte(`{"recommendations":[]}`))
	}))
	defer dec.Close()

	const deciderSecret = "decider-secret-long-enough-for-validation"
	const mailPassword = "mail-relay-password-that-must-not-leak"
	if r := a.do("PUT", "/api/v1/settings", map[string]any{"deciderUrl": dec.URL, "deciderSecret": deciderSecret}, withCookie(admin)); r.Code != 200 {
		t.Fatalf("saving the decider: %d %s", r.Code, r.Body.String())
	}
	if _, err := a.base.Accounts.SaveMailConfig(context.Background(), "root", accounts.MailConfig{Host: "smtp.example.com", Port: "587", Username: "u", Password: mailPassword, From: "ikhnos@example.com"}); err != nil {
		t.Fatalf("saving the mail configuration: %v", err)
	}

	// What is stored is sealed.
	raw, err := a.w.Store.GetSettings(context.Background(), "org-1")
	if err != nil || bytes.Contains(raw, []byte(deciderSecret)) || !strings.Contains(string(raw), "sealed:v1:") {
		t.Fatalf("stored settings: %q, %v", raw, err)
	}
	rawMail, err := a.w.Store.GetMailConfig(context.Background())
	if err != nil || bytes.Contains(rawMail, []byte(mailPassword)) || !strings.Contains(string(rawMail), "sealed:v1:") {
		t.Fatalf("stored mail: %q, %v", rawMail, err)
	}

	// And it still does its work: the request to the decider is signed with the secret that was typed.
	if r := a.do("POST", "/api/v1/decide", map[string]any{"schema": 1}, withCookie(editor)); r.Code != 200 {
		t.Fatalf("decide: %d %s", r.Code, r.Body.String())
	}
	sig, ok := strings.CutPrefix(gotSig, "sha256=")
	if !ok || !decisionloop.VerifyDeciderSignature(deciderSecret, gotBody, gotTS, sig, time.Now(), time.Minute) {
		t.Fatalf("the request was not signed with the secret that was saved (%q)", gotSig)
	}
	if got := a.core.Accounts.Mailer().Password; got != mailPassword {
		t.Fatalf("the mailer holds %q, not the password that was saved", got)
	}

	// Nothing in the files of the database.
	a.w.Store.ScrubPlaintext(context.Background())
	for _, secret := range []string{deciderSecret, mailPassword} {
		if filesHoldText(t, a.w.DBPath, secret) {
			t.Fatalf("%q is readable in a file of the database", secret)
		}
	}
}

// TestSettingsAndMailSavedBeforeSealingAreSealedAtTheNextStart is the upgrade: an existing server, secrets in the clear, then the key.
func TestSettingsAndMailSavedBeforeSealingAreSealedAtTheNextStart(t *testing.T) {
	a, admin, _, _, _, _ := rigWithDecider(t, "127.0.0.0/8")
	dec := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer dec.Close()
	const deciderSecret = "decider-secret-long-enough-for-validation"
	const mailPassword = "mail-relay-password-that-must-not-leak"
	a.do("PUT", "/api/v1/settings", map[string]any{"deciderUrl": dec.URL, "deciderSecret": deciderSecret}, withCookie(admin))
	if _, err := a.base.Accounts.SaveMailConfig(context.Background(), "root", accounts.MailConfig{Host: "smtp.example.com", Port: "587", Username: "u", Password: mailPassword, From: "ikhnos@example.com"}); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if raw, _ := a.w.Store.GetSettings(ctx, "org-1"); !bytes.Contains(raw, []byte(deciderSecret)) {
		t.Fatalf("the starting point is not a clear secret: %q", raw)
	}

	// "Restart" with a key: the documents are read, and written back sealed.
	sl := e2eSealer(t, e2eKeyA)
	if err := a.w.Store.UseSealer(ctx, sl); err != nil {
		t.Fatal(err)
	}
	a.base.Sealer = sl
	c := a.base.ForOrg("org-1")
	c.LoadSettings(ctx)
	a.base.Accounts.LoadMailConfig(ctx)
	if got, _ := c.SettingsAt(); got.DeciderSecret != deciderSecret {
		t.Fatalf("the secret read back is %q", got.DeciderSecret)
	}
	if err := a.w.Store.ScrubPlaintext(ctx); err != nil {
		t.Fatal(err)
	}
	raw, _ := a.w.Store.GetSettings(ctx, "org-1")
	rawMail, _ := a.w.Store.GetMailConfig(ctx)
	if bytes.Contains(raw, []byte(deciderSecret)) || !strings.Contains(string(raw), "sealed:v1:") {
		t.Fatalf("settings after: %q", raw)
	}
	if bytes.Contains(rawMail, []byte(mailPassword)) || !strings.Contains(string(rawMail), "sealed:v1:") {
		t.Fatalf("mail after: %q", rawMail)
	}
	for _, secret := range []string{deciderSecret, mailPassword} {
		if filesHoldText(t, a.w.DBPath, secret) {
			t.Fatalf("%q is still readable in a file after sealing was turned on", secret)
		}
	}
}

// TestADeciderSecretThatCannotBeOpenedSwitchesTheDeciderOff pins the failure that matters: a request to a decider that was set up to check a
// signature must never go out unsigned because the secret could not be read.
func TestADeciderSecretThatCannotBeOpenedSwitchesTheDeciderOff(t *testing.T) {
	a, admin, _, _, _, _ := rigWithDecider(t, "127.0.0.0/8")
	ctx := context.Background()
	sl := e2eSealer(t, e2eKeyA)
	a.w.Store.UseSealer(ctx, sl)
	a.base.Sealer = sl
	dec := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer dec.Close()
	a.do("PUT", "/api/v1/settings", map[string]any{"deciderUrl": dec.URL, "deciderSecret": "decider-secret-long-enough-for-validation"}, withCookie(admin))

	// Another key reads the same row (as if the secret were taken from another place).
	other := a.base.ForOrg("org-1")
	other.Sealer = e2eSealer(t, e2eKeyB)
	other.LoadSettings(ctx)
	got, _ := other.SettingsAt()
	if got.DeciderURL != "" || got.DeciderSecret != "" {
		t.Fatalf("the decider is still on without its secret: %+v", got)
	}
}
