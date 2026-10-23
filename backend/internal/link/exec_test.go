package link

import (
	"context"
	"os"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// These run a real shell: what is being tested is the behaviour of a real process, its pipes and its group, which a fake
// ExecFunc cannot show.

func TestOutputIsCappedWhileTheProgramKeepsRunningToTheEnd(t *testing.T) {
	// 5 MB on each stream, far more than the cap. A writer that stopped accepting would block the shell on a full pipe.
	start := time.Now()
	out, errOut, err := osExec(context.Background(), "/bin/sh", "-c", "head -c 5000000 /dev/zero; head -c 3000000 /dev/zero >&2; echo done")
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != maxToolOutput || len(errOut) != maxToolOutput {
		t.Errorf("kept %d bytes of stdout and %d of stderr, want %d each", len(out), len(errOut), maxToolOutput)
	}
	if took := time.Since(start); took > 4*time.Second {
		t.Errorf("took %s: the program was held up by its own output", took)
	}
	// What is kept is the start, and a short output is kept whole.
	out, errOut, err = osExec(context.Background(), "/bin/sh", "-c", "echo hello; echo oops >&2; exit 3")
	if string(out) != "hello\n" || string(errOut) != "oops\n" || err == nil || !strings.Contains(err.Error(), "exit status 3") {
		t.Errorf("out %q err %q error %v", out, errOut, err)
	}
}

func TestTheProgramRunsWithAMinimalEnvironmentPlusWhatIsAskedFor(t *testing.T) {
	t.Setenv("IKHNOS_SECRET_TEST", "leak")
	out, _, err := runTool(context.Background(), []string{"EXTRA=1"}, "/bin/sh", "-c", `echo "$LC_ALL $EXTRA [$IKHNOS_SECRET_TEST]"`)
	if err != nil || strings.TrimSpace(string(out)) != "C 1 []" {
		t.Errorf("out %q err %v", out, err)
	}
}

// A program that is killed at its deadline takes what it started with it, and the call returns when the deadline does, not
// after the grace period for pipes held open by a survivor (during which the shared gate would stay held).
func TestTheWholeProcessGroupIsKilledAtTheDeadline(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("process groups are used on Linux only")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	start := time.Now()
	out, _, err := osExec(ctx, "/bin/sh", "-c", "sleep 30 & echo $!; wait")
	took := time.Since(start)
	if err == nil {
		t.Error("a killed program reported success")
	}
	if took < 400*time.Millisecond || took > 3*time.Second {
		t.Errorf("returned after %s, want about the 500ms deadline (not the 5s the pipes would be waited for)", took)
	}
	pid, perr := strconv.Atoi(strings.TrimSpace(string(out)))
	if perr != nil {
		t.Fatalf("no pid in %q", out)
	}
	deadline := time.Now().Add(3 * time.Second)
	for !gone(pid) {
		if time.Now().After(deadline) {
			t.Fatalf("process %d, started by the program, is still running", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// gone says a process no longer runs: it has no /proc entry or it is a zombie, which nothing in this sandbox may reap.
func gone(pid int) bool {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return true
	}
	s := string(b)
	i := strings.LastIndexByte(s, ')')
	return i < 0 || len(s) < i+3 || s[i+2] == 'Z'
}

func TestACancelledContextStopsAProgramThatNeverWrites(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(100 * time.Millisecond); cancel() }()
	start := time.Now()
	_, _, err := osExec(ctx, "/bin/sh", "-c", "sleep 30")
	if err == nil || time.Since(start) > 3*time.Second {
		t.Errorf("err %v after %s", err, time.Since(start))
	}
}
