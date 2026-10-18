package estate

import (
	"fmt"
	"sync"
	"testing"

	"continuum/internal/kernel"
	"continuum/internal/testkit"
)

func TestWorkspaceSavesRaceExactlyOneWinner(t *testing.T) {
	e := newEnv(t)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var wins, conflicts int
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := e.core.SaveWorkspace(e.ctx, fmt.Sprint("u", i), 0, []byte(`{"schemaVersion":3}`))
			mu.Lock()
			defer mu.Unlock()
			switch testkit.KindOf(err) {
			case 0:
				wins++
			case kernel.KindConflict:
				conflicts++
			default:
				t.Errorf("unexpected error: %v", err)
			}
		}(i)
	}
	wg.Wait()
	if wins != 1 || conflicts != 19 {
		t.Fatalf("%d winners and %d conflicts among 20 simultaneous first saves", wins, conflicts)
	}
}
