package accounts

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"continuum/internal/kernel"
)

func TestPasswordHashWorkIsBoundedInNumberAndQueue(t *testing.T) {
	c := NewService(kernel.NewCore(nil, nil, "", nil))
	c.authSt().hashSem = make(chan struct{}, 2)
	c.authSt().maxQueue = 2
	hash, _ := HashPassword("correct horse battery")
	// occupy both slots
	c.authSt().hashSem <- struct{}{}
	c.authSt().hashSem <- struct{}{}

	results := make(chan error, 4)
	var started sync.WaitGroup
	for i := 0; i < 2; i++ { // two may wait
		started.Add(1)
		go func() {
			started.Done()
			_, err := c.verifyPassword(context.Background(), "correct horse battery", hash)
			results <- err
		}()
	}
	started.Wait()
	for c.authSt().hashQueue.Load() < 2 {
		time.Sleep(time.Millisecond)
	}
	// the third finds the queue full and is refused at once, with a clear error, without waiting
	start := time.Now()
	_, err := c.verifyPassword(context.Background(), "x", hash)
	if kindOfAcct(err) != kernel.KindRateLimited || !strings.Contains(err.Error(), "busy") || time.Since(start) > time.Second {
		t.Fatalf("the request beyond the queue: %v after %v", err, time.Since(start))
	}
	if _, err := c.hashPassword(context.Background(), "x"); kindOfAcct(err) != kernel.KindRateLimited {
		t.Fatalf("hashing beyond the queue: %v", err)
	}
	// free the slots: the two waiters finish
	<-c.authSt().hashSem
	<-c.authSt().hashSem
	for i := 0; i < 2; i++ {
		select {
		case err := <-results:
			if err != nil {
				t.Fatalf("a queued check failed: %v", err)
			}
		case <-time.After(20 * time.Second):
			t.Fatal("a queued check never ran")
		}
	}
	if c.authSt().hashQueue.Load() != 0 || len(c.authSt().hashSem) != 0 {
		t.Fatalf("leaked: queue %d, slots %d", c.authSt().hashQueue.Load(), len(c.authSt().hashSem))
	}
	// a waiter gives up when its request is cancelled
	c.authSt().hashSem <- struct{}{}
	c.authSt().hashSem <- struct{}{}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := c.verifyPassword(ctx, "x", hash); err == nil {
		t.Fatal("a cancelled waiter ran")
	}
	if c.authSt().hashQueue.Load() != 0 {
		t.Fatal("a cancelled waiter stayed in the queue")
	}
}

func kindOfAcct(err error) kernel.Kind {
	var e *kernel.Error
	if errors.As(err, &e) {
		return e.Kind
	}
	return 0
}

func TestProductionArgonParameters(t *testing.T) {
	m, ti, p := ArgonMemoryKiB, ArgonTime, ArgonThreads
	defer func() { ArgonMemoryKiB, ArgonTime, ArgonThreads = m, ti, p }()
	ArgonMemoryKiB, ArgonTime, ArgonThreads = 64*1024, 2, 2
	start := time.Now()
	h, _ := HashPassword("correct horse battery 9")
	if !strings.Contains(h, "m=65536,t=2,p=2") {
		t.Fatalf("parameters not recorded in the hash: %s", h)
	}
	if ok, _ := VerifyPassword("correct horse battery 9", h); !ok {
		t.Fatal("verify")
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("two hashes took %v", d)
	}
	// A hash made with cheaper settings still verifies: parameters come from the hash.
	ArgonMemoryKiB, ArgonTime, ArgonThreads = 1024, 1, 1
	old, _ := HashPassword("correct horse battery 9")
	ArgonMemoryKiB, ArgonTime, ArgonThreads = 64*1024, 2, 2
	if ok, _ := VerifyPassword("correct horse battery 9", old); !ok {
		t.Fatal("older hash no longer verifies")
	}
}
