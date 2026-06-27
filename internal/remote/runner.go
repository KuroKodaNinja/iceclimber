// Package remote provides the controller's command/transport access to the
// sandbox host over SSH. Run executes non-interactive POSIX sh commands and is
// the boundary that probe (and later the dispatcher) are tested against.
package remote

import "context"

// Result is the outcome of a single remote command. ExitCode carries the
// command's own status; a non-zero ExitCode is NOT an error — probe relies on
// inspecting it. Run returns a non-nil error only for transport/connection
// failures, never for a command that merely exited non-zero.
type Result struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int
}

// Runner executes commands on the sandbox host. It is intentionally tiny so it
// can be faked at the boundary in tests.
type Runner interface {
	// Run executes cmd via a fresh non-interactive shell session.
	Run(ctx context.Context, cmd string) (Result, error)
	// Close releases the underlying connection.
	Close() error
}
