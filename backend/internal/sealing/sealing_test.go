package sealing

import (
	"errors"
	"strings"
	"testing"
)

const (
	keyA = "an-example-key-that-is-long-enough-A"
	keyB = "an-example-key-that-is-long-enough-B"
)

func mustNew(t *testing.T, keys ...string) *Sealer {
	t.Helper()
	var ks [][]byte
	for _, k := range keys {
		ks = append(ks, []byte(k))
	}
	s, err := New(ks...)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestASealedValueOpensWhereItWasSealedAndNowhereElse(t *testing.T) {
	s := mustNew(t, keyA)
	sealed, err := s.Seal("totp", "user-1", "JBSWY3DPEHPK3PXP")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(sealed, "JBSWY3DPEHPK3PXP") || !IsSealed(sealed) {
		t.Fatalf("the secret is readable in %q", sealed)
	}
	if got, err := s.Open("totp", "user-1", sealed); err != nil || got != "JBSWY3DPEHPK3PXP" {
		t.Fatalf("Open = %q, %v", got, err)
	}
	for _, c := range []struct{ purpose, where string }{{"totp", "user-2"}, {"mail", "user-1"}} {
		if _, err := s.Open(c.purpose, c.where, sealed); !errors.Is(err, ErrCorrupt) {
			t.Errorf("opened under %v: %v", c, err)
		}
	}
}

func TestTwoSealsOfOneValueDiffer(t *testing.T) {
	s := mustNew(t, keyA)
	a, _ := s.Seal("mail", "", "hunter2hunter2")
	b, _ := s.Seal("mail", "", "hunter2hunter2")
	if a == b {
		t.Fatal("the same value sealed twice gave the same text: the nonce is not fresh")
	}
}

func TestATamperedValueDoesNotOpen(t *testing.T) {
	s := mustNew(t, keyA)
	sealed, _ := s.Seal("mail", "", "secret-password")
	// Flip the last character of the body.
	last := sealed[len(sealed)-1]
	flip := byte('A')
	if last == 'A' {
		flip = 'B'
	}
	if _, err := s.Open("mail", "", sealed[:len(sealed)-1]+string(flip)); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("a changed value opened: %v", err)
	}
	for _, bad := range []string{"sealed:v1:", "sealed:v1:nocolon", sealed[:len(sealed)/2], "sealed:v1:" + s.KeyID() + ":!!!"} {
		if _, err := s.Open("mail", "", bad); err == nil {
			t.Errorf("%q opened", bad)
		}
	}
}

func TestAnEmptySecretStaysEmptyAndAPlainOneIsReturnedAsItIs(t *testing.T) {
	s := mustNew(t, keyA)
	if got, _ := s.Seal("totp", "u", ""); got != "" {
		t.Fatalf("an empty secret was sealed to %q", got)
	}
	if got, err := s.Open("totp", "u", "JBSWY3DPEHPK3PXP"); err != nil || got != "JBSWY3DPEHPK3PXP" {
		t.Fatalf("a value from before sealing: %q, %v", got, err)
	}
}

func TestASealerThatIsOffPassesPlainValuesAndRefusesSealedOnes(t *testing.T) {
	on := mustNew(t, keyA)
	sealed, _ := on.Seal("mail", "", "secret-password")
	for _, off := range []*Sealer{nil, {}} {
		if got, err := off.Seal("mail", "", "x"); err != nil || got != "x" {
			t.Errorf("off Seal = %q, %v", got, err)
		}
		if _, err := off.Open("mail", "", sealed); !errors.Is(err, ErrNoKey) {
			t.Errorf("off Open of a sealed value: %v", err)
		}
		if off.Enabled() || off.KeyID() != "" {
			t.Error("an off sealer says it is on")
		}
	}
}

func TestAnotherKeyNamesItselfInTheRefusal(t *testing.T) {
	a, b := mustNew(t, keyA), mustNew(t, keyB)
	sealed, _ := a.Seal("mail", "", "secret-password")
	_, err := b.Open("mail", "", sealed)
	var u *UnknownKeyError
	if !errors.As(err, &u) || u.ID != a.KeyID() {
		t.Fatalf("err = %v, want an unknown key %s", err, a.KeyID())
	}
	if a.KeyID() == b.KeyID() || len(a.KeyID()) != 8 {
		t.Fatalf("key ids %q %q", a.KeyID(), b.KeyID())
	}
}

func TestAKeyIsReplacedByPuttingTheNewOneOnTop(t *testing.T) {
	old := mustNew(t, keyA)
	sealed, _ := old.Seal("totp", "u", "JBSWY3DPEHPK3PXP")
	rotated := mustNew(t, keyB, keyA)
	if rotated.Current(sealed) {
		t.Fatal("a value under the old key was called current")
	}
	got, changed, err := rotated.Reseal("totp", "u", sealed)
	if err != nil || !changed {
		t.Fatalf("Reseal = %v, %v", changed, err)
	}
	if SealedUnder(got) != rotated.KeyID() || !rotated.Current(got) {
		t.Fatalf("not under the new key: %q", got)
	}
	// With the old key taken off, the new value still opens and the old one no longer does.
	only := mustNew(t, keyB)
	if v, err := only.Open("totp", "u", got); err != nil || v != "JBSWY3DPEHPK3PXP" {
		t.Fatalf("after the old key is off: %q, %v", v, err)
	}
	if _, err := only.Open("totp", "u", sealed); err == nil {
		t.Fatal("the old value opened without its key")
	}
	if again, changed, _ := rotated.Reseal("totp", "u", got); changed || again != got {
		t.Fatal("a current value was sealed again")
	}
}

func TestAPlainValueIsSealedByReseal(t *testing.T) {
	s := mustNew(t, keyA)
	got, changed, err := s.Reseal("mail", "", "secret-password")
	if err != nil || !changed || !IsSealed(got) {
		t.Fatalf("Reseal = %q, %v, %v", got, changed, err)
	}
	if out, changed, _ := s.Reseal("mail", "", ""); changed || out != "" {
		t.Fatal("an empty value was changed")
	}
}

func TestKeysAreRefusedWhenTheyAreShortRepeatedOrAbsent(t *testing.T) {
	if _, err := New(); err == nil {
		t.Error("no key was accepted")
	}
	if _, err := New([]byte("short")); err == nil || !strings.Contains(err.Error(), "at least 32") {
		t.Errorf("a short key: %v", err)
	}
	if _, err := New([]byte(keyA), []byte(keyA)); err == nil {
		t.Error("the same key twice was accepted")
	}
}

func TestTheKeyFileIsOneKeyPerLine(t *testing.T) {
	ks, err := ParseKeys([]byte("# current\n  " + keyA + "  \r\n\n# the one before\n" + keyB + "\n"))
	if err != nil || len(ks) != 2 || string(ks[0]) != keyA || string(ks[1]) != keyB {
		t.Fatalf("ParseKeys = %q, %v", ks, err)
	}
	if _, err := ParseKeys([]byte("\n# nothing\n \n")); err == nil {
		t.Error("an empty file was accepted")
	}
}

func TestTheKeyIsNotAnywhereInASealedValue(t *testing.T) {
	s := mustNew(t, keyA)
	sealed, _ := s.Seal("mail", "", "x")
	if strings.Contains(sealed, keyA) {
		t.Fatal("the key is in the sealed value")
	}
}

func FuzzOpenNeverPanics(f *testing.F) {
	s, _ := New([]byte(keyA))
	good, _ := s.Seal("mail", "", "secret-password")
	for _, seed := range []string{"", "x", "sealed:v1:", "sealed:v1:a:b", good, good[:20]} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, in string) {
		out, err := s.Open("mail", "", in)
		if err == nil && IsSealed(in) && out != "secret-password" {
			t.Fatalf("%q opened to %q without being the one value sealed", in, out)
		}
	})
}
