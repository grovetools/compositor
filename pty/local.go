package pty

import (
	"os"
	"os/exec"

	creackpty "github.com/creack/pty"
)

// LocalBackend wraps a local creack/pty master fd and child process.
// This is the traditional mode where the shell is a child of the TUI.
type LocalBackend struct {
	ptmx *os.File
	cmd  *exec.Cmd
}

// StartLocal spawns a shell in a new PTY with the given size and working
// directory, returning a LocalBackend that implements Backend.
func StartLocal(dir string, rows, cols uint16, environ []string) (*LocalBackend, error) {
	shell := os.Getenv("SHELL")
	// Prefer SHELL from the passed environ (caller's environment) over
	// the process-level env, since the daemon may have inherited a
	// different shell than the user's login shell.
	for _, e := range environ {
		if len(e) > 6 && e[:6] == "SHELL=" {
			shell = e[6:]
			break
		}
	}
	if shell == "" {
		shell = "/bin/sh"
	}

	cmd := exec.Command(shell)
	cmd.Dir = dir
	cmd.Env = append(environ, "TERM=xterm-256color", "GROVE_TERMINAL=1")

	ptmx, err := creackpty.StartWithSize(cmd, &creackpty.Winsize{
		Rows: rows,
		Cols: cols,
	})
	if err != nil {
		return nil, err
	}

	return &LocalBackend{ptmx: ptmx, cmd: cmd}, nil
}

// StartLocalCommand spawns an arbitrary command in a new PTY with the given
// size and working directory, returning a LocalBackend that implements Backend.
func StartLocalCommand(cmd *exec.Cmd, rows, cols uint16) (*LocalBackend, error) {
	ptmx, err := creackpty.StartWithSize(cmd, &creackpty.Winsize{
		Rows: rows,
		Cols: cols,
	})
	if err != nil {
		return nil, err
	}
	return &LocalBackend{ptmx: ptmx, cmd: cmd}, nil
}

func (b *LocalBackend) Read(p []byte) (int, error)  { return b.ptmx.Read(p) }
func (b *LocalBackend) Write(p []byte) (int, error)  { return b.ptmx.Write(p) }

func (b *LocalBackend) Close() error {
	err := b.ptmx.Close()
	if b.cmd != nil && b.cmd.Process != nil {
		_ = b.cmd.Process.Kill()
	}
	return err
}

func (b *LocalBackend) Resize(rows, cols uint16) error {
	return creackpty.Setsize(b.ptmx, &creackpty.Winsize{Rows: rows, Cols: cols})
}

func (b *LocalBackend) Name() string {
	return b.ptmx.Name()
}

func (b *LocalBackend) Pid() int {
	if b.cmd != nil && b.cmd.Process != nil {
		return b.cmd.Process.Pid
	}
	return -1
}

func (b *LocalBackend) Fd() uintptr {
	return b.ptmx.Fd()
}
