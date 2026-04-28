// Package pty defines the Backend interface for PTY I/O, abstracting over
// local PTYs (creack/pty) and daemon-owned PTYs (WebSocket).
package pty

import "io"

// Backend is the abstraction that ShellPanel uses for PTY I/O.
// LocalBackend wraps a creack/pty master fd; DaemonBackend wraps a
// WebSocket connection to the daemon's /api/pty/attach/{id} endpoint.
type Backend interface {
	io.ReadWriteCloser

	// Resize sets the PTY window size (rows x cols).
	Resize(rows, cols uint16) error

	// Name returns a human-readable label (e.g. shell name or session ID).
	Name() string

	// Pid returns the PID of the child process, or -1 for remote backends.
	Pid() int

	// Fd returns the PTY master file descriptor for direct stdin routing,
	// or ^uintptr(0) for remote backends that have no local fd.
	Fd() uintptr
}
