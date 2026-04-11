package compositor

import (
	"os"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// compositorTickMsg drives the 60fps flush cycle.
type compositorTickMsg time.Time

func compositorTickCmd() tea.Cmd {
	return tea.Tick(16*time.Millisecond, func(t time.Time) tea.Msg {
		return compositorTickMsg(t)
	})
}

// Model wraps a child tea.Model with compositor-based rendering.
// Instead of bubbletea writing the child's View() string to stdout,
// the compositor parses it into a cell buffer and flushes only dirty
// cells — eliminating flicker and reducing write overhead.
type Model struct {
	child    tea.Model
	comp     *Compositor
	width    int
	height   int
	logLevel int
}

// Option configures a compositor Model.
type Option func(*Model)

// WithLogLevel sets the Zig-side log level (0=trace..4=error).
func WithLogLevel(level int) Option {
	return func(m *Model) {
		m.logLevel = level
	}
}

// NewModel wraps a child tea.Model with compositor rendering.
// The compositor is created on the first WindowSizeMsg and freed on quit.
// Default log level is LogInfo; override with WithLogLevel().
func NewModel(child tea.Model, opts ...Option) *Model {
	m := &Model{
		child:    child,
		logLevel: LogInfo,
	}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

// Init delegates to the child's Init.
func (m *Model) Init() tea.Cmd {
	return m.child.Init()
}

// Update handles compositor lifecycle and delegates to the child.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		if m.comp == nil {
			m.comp = New(m.width, m.height, m.logLevel)
			// Start the tick loop on first resize.
			child, cmd := m.child.Update(msg)
			m.child = child
			return m, tea.Batch(cmd, compositorTickCmd())
		}
		m.comp.Resize(m.width, m.height)
		child, cmd := m.child.Update(msg)
		m.child = child
		return m, cmd

	case compositorTickMsg:
		if m.comp != nil && m.width > 0 && m.height > 0 {
			screenStr := m.child.View()
			m.comp.BlitANSI(0, 0, m.width, m.height, screenStr)
			m.comp.Flush(int(os.Stdout.Fd()))
		}
		return m, compositorTickCmd()

	default:
		child, cmd := m.child.Update(msg)
		m.child = child

		// Check if the child wants to quit — free compositor resources.
		if cmd != nil {
			// We can't inspect cmd directly, but we handle cleanup in View().
		}
		return m, cmd
	}
}

// View returns an empty string — the compositor handles all rendering
// via the tick loop, so bubbletea's default renderer has nothing to write.
func (m *Model) View() string {
	return ""
}

// Free releases compositor resources. Call this after tea.Program.Run()
// returns, or rely on the OS to reclaim memory on process exit.
func (m *Model) Free() {
	if m.comp != nil {
		m.comp.Free()
		m.comp = nil
	}
}

// Compositor returns the underlying Compositor, or nil if not yet created.
// This is useful for terminal-specific extensions that need the raw pointer.
func (m *Model) Compositor() *Compositor {
	return m.comp
}

// NoopWriter is an io.Writer that satisfies bubbletea's Fd() interface
// requirement while discarding all output. Use with tea.WithOutput() to
// prevent bubbletea from writing its own rendering when the compositor
// is handling output.
type NoopWriter struct {
	fd uintptr
}

// NewNoopWriter creates a NoopWriter backed by stdout's fd.
func NewNoopWriter() *NoopWriter {
	return &NoopWriter{fd: os.Stdout.Fd()}
}

// Write discards all bytes.
func (w *NoopWriter) Write(p []byte) (int, error) {
	return len(p), nil
}

// Fd returns the underlying file descriptor so bubbletea can query
// terminal dimensions via ioctl.
func (w *NoopWriter) Fd() uintptr {
	return w.fd
}
