package main

import (
	"errors"
	"os"
	"syscall"
	"testing"
)

func TestTheServerEndsWithAFailureWhenAListenerStopsOnItsOwn(t *testing.T) {
	sig, fatal := make(chan os.Signal, 1), make(chan error, 1)
	want := errors.New("the agent listener stopped")
	fatal <- want
	if err := waitForEnd(sig, fatal); !errors.Is(err, want) {
		t.Fatalf("waitForEnd = %v, want the listener's error", err)
	}
}

func TestTheServerEndsCleanlyOnASignal(t *testing.T) {
	sig, fatal := make(chan os.Signal, 1), make(chan error, 1)
	sig <- syscall.SIGTERM
	if err := waitForEnd(sig, fatal); err != nil {
		t.Fatalf("waitForEnd = %v, want nil", err)
	}
}
