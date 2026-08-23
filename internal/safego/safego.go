// Package safego guards background goroutines against taking down the
// whole process. net/http already recovers a panic inside a request
// handler on its own; it does nothing for a goroutine started with a
// bare `go` outside that request lifecycle (a poll loop, a janitor, an
// async job worker) — an unrecovered panic there crashes every other
// goroutine, session, and in-flight request along with it.
package safego

import (
	"log/slog"
	"runtime/debug"
)

// Recover logs and swallows a panic if one is in flight. Call via
// `defer safego.Recover("name")` at the top of a goroutine function.
// The goroutine still stops running past the panic point — this only
// keeps that contained instead of taking the process down with it.
func Recover(name string) {
	if r := recover(); r != nil {
		slog.Error("panic recovered", "goroutine", name, "panic", r, "stack", string(debug.Stack()))
	}
}
