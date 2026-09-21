package agent

import (
	"context"
	"log/slog"
	"strings"
	"sync"
)

type lineHandler struct {
	mu    *sync.Mutex
	lines *[]string
}

func (h lineHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h lineHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	*h.lines = append(*h.lines, r.Message)
	return nil
}
func (h lineHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h lineHandler) WithGroup(string) slog.Handler      { return h }

// captureLog collects the messages of what is logged.
func captureLog(lines *[]string) *slog.Logger {
	return slog.New(lineHandler{mu: &sync.Mutex{}, lines: lines})
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }
