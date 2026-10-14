package link

import (
	"bytes"
	"context"
	"os/exec"
	"time"
)

// maxToolOutput is how much of a program's stdout and of its stderr is kept. The tools print a few kilobytes; anything past
// this is dropped as it arrives, so a program that misbehaves (or is made to by what it talks to) cannot grow the prober's
// memory.
const maxToolOutput = 1 << 20

// limitedBuffer keeps the first max bytes written to it and discards the rest. It still accepts every write: a writer that
// failed would make exec stop copying, the program would block on a full pipe and only die at the deadline.
type limitedBuffer struct {
	buf bytes.Buffer
	max int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if room := b.max - b.buf.Len(); room > 0 {
		b.buf.Write(p[:min(room, len(p))])
	}
	return len(p), nil
}

func capBytes(b []byte) []byte {
	if len(b) > maxToolOutput {
		return b[:maxToolOutput]
	}
	return b
}

// runTool runs a program with a minimal environment (plus env), at most maxToolOutput bytes kept of each of its outputs, and
// its whole process group killed when ctx ends (see killGroupOnCancel). This is what the package's real ExecFunc and
// ExecEnvFunc do; tests substitute fakes.
func runTool(ctx context.Context, env []string, name string, args ...string) ([]byte, []byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	out, errb := &limitedBuffer{max: maxToolOutput}, &limitedBuffer{max: maxToolOutput}
	cmd.Stdout, cmd.Stderr = out, errb
	cmd.Env = append([]string{"PATH=/usr/bin:/bin", "LC_ALL=C"}, env...)
	// Still bounded in case something outside the group holds the pipes open.
	cmd.WaitDelay = 5 * time.Second
	killGroupOnCancel(cmd)
	err := cmd.Run()
	return out.buf.Bytes(), errb.buf.Bytes(), err
}

// osExec is the real ExecFunc.
func osExec(ctx context.Context, name string, args ...string) ([]byte, []byte, error) {
	return runTool(ctx, nil, name, args...)
}
