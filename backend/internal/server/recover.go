package server

import (
	"fmt"
	"runtime/debug"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// recoverPanic turns a panic in a gRPC handler into an Internal status for that one call or stream: the process keeps
// serving everyone else, the stack goes to the log, and the counter goes up. The agent whose stream it was sees a failed
// connection and reconnects, which starts the stream loop again. Use it as `defer c.recoverPanic(what, &err)`.
func (c *Core) recoverPanic(what string, err *error) {
	if p := recover(); p != nil {
		Metrics.streamPanics.Add(1)
		c.Log.Error("panic recovered in a request handler", "handler", what, "panic", fmt.Sprint(p), "stack", string(debug.Stack()))
		*err = status.Error(codes.Internal, "internal error")
	}
}

// safely runs fn on a background goroutine of the server without letting a panic take the process down.
func (c *Core) safely(what string, fn func()) {
	defer func() {
		if p := recover(); p != nil {
			Metrics.streamPanics.Add(1)
			c.Log.Error("panic recovered in a background task", "task", what, "panic", fmt.Sprint(p), "stack", string(debug.Stack()))
		}
	}()
	fn()
}
