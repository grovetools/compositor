// renderbench — side-by-side comparison of bubbletea vs compositor rendering.
//
// It builds a complex lipgloss layout (tabs, table, status bar) similar to
// grove-nav, simulates 300 frames of typical interaction (cursor movement,
// selection changes, filter typing), and measures:
//
//   - Bubbletea mode:  len(View()) per frame  (full repaint every frame)
//   - Compositor mode: actual bytes written    (dirty-cell diff only)
//
// Outputs an HTML report to /tmp/compositor-bench.html with per-frame charts.
package main

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/grovetools/compositor"
)

const (
	width      = 200
	height     = 50
	numFrames  = 300
	outputPath = "/tmp/compositor-bench.html"
)

// ── Simulated nav-like layout ───────────────────────────────────────

var (
	tabStyle = lipgloss.NewStyle().
			Padding(0, 2).
			Bold(true)
	activeTabStyle = tabStyle.
			Foreground(lipgloss.Color("229")).
			Background(lipgloss.Color("57"))
	inactiveTabStyle = tabStyle.
				Foreground(lipgloss.Color("250")).
				Background(lipgloss.Color("236"))
	rowStyle = lipgloss.NewStyle().
			Width(width - 4).
			Padding(0, 1)
	selectedRowStyle = rowStyle.
				Foreground(lipgloss.Color("229")).
				Background(lipgloss.Color("57"))
	statusStyle = lipgloss.NewStyle().
			Width(width).
			Background(lipgloss.Color("236")).
			Foreground(lipgloss.Color("250")).
			Padding(0, 1)
	filterStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("214")).
			Bold(true)
)

type frameState struct {
	activeTab int
	cursor    int
	filter    string
	items     []string
}

func generateItems() []string {
	ecosystems := []string{"grovetools", "myapp", "infra", "platform", "analytics"}
	projects := []string{"core", "nav", "terminal", "compositor", "flow", "daemon", "tend", "cx", "nb", "hooks"}
	branches := []string{"main", "feat/new-ui", "fix/crash", "refactor/cleanup", "chore/deps"}
	statuses := []string{"clean", "modified", "ahead 3", "behind 1", "diverged"}

	items := make([]string, 40)
	for i := range items {
		eco := ecosystems[i%len(ecosystems)]
		proj := projects[i%len(projects)]
		branch := branches[i%len(branches)]
		status := statuses[i%len(statuses)]
		items[i] = fmt.Sprintf("%-15s %-12s %-25s %s", eco, proj, branch, status)
	}
	return items
}

func renderFrame(s frameState) string {
	var b strings.Builder

	// Tab bar
	tabs := []string{"Sessions", "Keys", "History", "Groups", "Windows"}
	var tabParts []string
	for i, t := range tabs {
		if i == s.activeTab {
			tabParts = append(tabParts, activeTabStyle.Render(t))
		} else {
			tabParts = append(tabParts, inactiveTabStyle.Render(t))
		}
	}
	tabBar := lipgloss.JoinHorizontal(lipgloss.Top, tabParts...)
	b.WriteString(tabBar)
	b.WriteByte('\n')

	// Separator
	b.WriteString(strings.Repeat("─", width))
	b.WriteByte('\n')

	// Filter line
	if s.filter != "" {
		b.WriteString(filterStyle.Render("Filter: " + s.filter))
		b.WriteByte('\n')
	} else {
		b.WriteString("  Type to filter...")
		b.WriteByte('\n')
	}

	// Header
	header := fmt.Sprintf("  %-15s %-12s %-25s %s", "ECOSYSTEM", "PROJECT", "BRANCH", "STATUS")
	b.WriteString(lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("245")).Render(header))
	b.WriteByte('\n')

	// Table rows — show visible window
	visibleRows := height - 6 // tabs + sep + filter + header + status + padding
	startIdx := 0
	if s.cursor >= visibleRows {
		startIdx = s.cursor - visibleRows + 1
	}

	rendered := 0
	for i := startIdx; i < len(s.items) && rendered < visibleRows; i++ {
		if s.filter != "" && !strings.Contains(strings.ToLower(s.items[i]), strings.ToLower(s.filter)) {
			continue
		}
		if i == s.cursor {
			b.WriteString(selectedRowStyle.Render("▸ " + s.items[i]))
		} else {
			b.WriteString(rowStyle.Render("  " + s.items[i]))
		}
		b.WriteByte('\n')
		rendered++
	}

	// Fill remaining rows
	for rendered < visibleRows {
		b.WriteString(rowStyle.Render(""))
		b.WriteByte('\n')
		rendered++
	}

	// Status bar
	statusText := fmt.Sprintf(" %d items | cursor: %d | tab: %s",
		len(s.items), s.cursor, []string{"Sessions", "Keys", "History", "Groups", "Windows"}[s.activeTab])
	b.WriteString(statusStyle.Render(statusText))

	return b.String()
}

// ── Frame sequence simulation ───────────────────────────────────────

func simulateFrames(items []string) []frameState {
	frames := make([]frameState, numFrames)
	state := frameState{items: items}

	rng := rand.New(rand.NewSource(42)) // deterministic

	for i := range frames {
		// Simulate typical nav interactions
		switch {
		case i < 30:
			// Initial render, small cursor movements
			state.cursor = i % len(items)
		case i < 60:
			// Typing a filter, one char at a time
			filterChars := "grovetools"
			idx := (i - 30) % len(filterChars)
			state.filter = filterChars[:idx+1]
		case i < 90:
			// Clear filter, browse
			state.filter = ""
			state.cursor = rng.Intn(len(items))
		case i < 120:
			// Tab switching
			state.activeTab = (i - 90) % 5
		case i < 180:
			// Mixed: rapid cursor movement (scrolling)
			state.activeTab = 0
			state.cursor = (state.cursor + 1) % len(items)
		case i < 220:
			// Idle — same state, no changes (tests compositor skip)
			// state unchanged
		case i < 260:
			// Another filter pass
			filterChars := "nav"
			idx := (i - 220) % len(filterChars)
			state.filter = filterChars[:idx+1]
			state.cursor = 0
		default:
			// Final browse
			state.filter = ""
			state.cursor = rng.Intn(len(items))
		}

		frames[i] = state
	}
	return frames
}

// ── Measurement ─────────────────────────────────────────────────────

type frameSample struct {
	Frame           int    `json:"frame"`
	BubbleteaBytes  int    `json:"bubbletea_bytes"`
	CompositorBytes uint64 `json:"compositor_bytes"`
	DirtyCells      uint64 `json:"dirty_cells"`
	BlitUs          uint64 `json:"blit_us"`
	FlushUs         uint64 `json:"flush_us"`
	Scenario        string `json:"scenario"`
}

func main() {
	items := generateItems()
	frames := simulateFrames(items)

	// Open /dev/null for compositor flush target
	devNull, err := os.Open("/dev/null")
	if err != nil {
		fmt.Fprintf(os.Stderr, "cannot open /dev/null: %v\n", err)
		os.Exit(1)
	}
	defer devNull.Close()
	nullFd := int(devNull.Fd())

	// Suppress Zig-side logging
	compositor.SetLogFunc(func(level int, msg string) {})

	comp := compositor.New(width, height, compositor.LogError)
	defer comp.Free()

	samples := make([]frameSample, len(frames))
	var prevStats compositor.Stats

	for i, fs := range frames {
		view := renderFrame(fs)

		// Bubbletea: writes full View() every frame
		btBytes := len(view)

		// Compositor: blit + flush, measure delta
		comp.BlitANSI(0, 0, width, height, view)
		comp.Flush(nullFd)

		stats := comp.GetStats()
		compBytes := stats.BytesWritten - prevStats.BytesWritten
		dirtyCells := stats.DirtyCellsFlushed - prevStats.DirtyCellsFlushed
		blitUs := stats.BlitANSITimeUs - prevStats.BlitANSITimeUs
		flushUs := stats.FlushTimeUs - prevStats.FlushTimeUs

		// Determine scenario label
		scenario := "browse"
		switch {
		case i < 30:
			scenario = "cursor move"
		case i < 60:
			scenario = "typing filter"
		case i < 90:
			scenario = "clear + browse"
		case i < 120:
			scenario = "tab switch"
		case i < 180:
			scenario = "rapid scroll"
		case i < 220:
			scenario = "idle"
		case i < 260:
			scenario = "typing filter"
		}

		samples[i] = frameSample{
			Frame:           i,
			BubbleteaBytes:  btBytes,
			CompositorBytes: compBytes,
			DirtyCells:      dirtyCells,
			BlitUs:          blitUs,
			FlushUs:         flushUs,
			Scenario:        scenario,
		}
		prevStats = stats
	}

	// Compute totals
	finalStats := comp.GetStats()

	var totalBT int
	for _, s := range samples {
		totalBT += s.BubbleteaBytes
	}

	samplesJSON, _ := json.Marshal(samples)
	reduction := float64(totalBT-int(finalStats.BytesWritten)) / float64(totalBT) * 100

	html := generateHTML(samplesJSON, totalBT, finalStats, reduction)
	if err := os.WriteFile(outputPath, []byte(html), 0644); err != nil {
		fmt.Fprintf(os.Stderr, "write failed: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Report written to %s\n", outputPath)
	fmt.Printf("\nQuick summary:\n")
	fmt.Printf("  Frames:         %d\n", numFrames)
	fmt.Printf("  Screen:         %d×%d\n", width, height)
	fmt.Printf("  Bubbletea total: %s\n", humanBytes(totalBT))
	fmt.Printf("  Compositor total: %s\n", humanBytes(int(finalStats.BytesWritten)))
	fmt.Printf("  Reduction:      %.1f%%\n", reduction)
	fmt.Printf("  Dirty cells:    %d (avg %.0f/frame)\n",
		finalStats.DirtyCellsFlushed,
		float64(finalStats.DirtyCellsFlushed)/float64(numFrames))
}

func humanBytes(b int) string {
	switch {
	case b >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(b)/float64(1<<20))
	case b >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(b)/float64(1<<10))
	default:
		return fmt.Sprintf("%d B", b)
	}
}

func generateHTML(samplesJSON []byte, totalBT int, stats compositor.Stats, reduction float64) string {
	return fmt.Sprintf(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>Compositor vs Bubbletea — Render Benchmark</title>
<script src="https://cdn.jsdelivr.net/npm/chart.js@4"></script>
<style>
  * { margin: 0; padding: 0; box-sizing: border-box; }
  body { font-family: -apple-system, BlinkMacSystemFont, 'SF Mono', monospace; background: #0d1117; color: #c9d1d9; padding: 24px; }
  h1 { font-size: 1.4rem; margin-bottom: 8px; color: #f0f6fc; }
  .subtitle { color: #8b949e; margin-bottom: 24px; font-size: 0.9rem; }
  .grid { display: grid; grid-template-columns: repeat(4, 1fr); gap: 16px; margin-bottom: 24px; }
  .card { background: #161b22; border: 1px solid #30363d; border-radius: 8px; padding: 16px; }
  .card .label { font-size: 0.75rem; color: #8b949e; text-transform: uppercase; letter-spacing: 0.05em; }
  .card .value { font-size: 1.8rem; font-weight: 700; margin-top: 4px; }
  .card .value.green { color: #3fb950; }
  .card .value.blue { color: #58a6ff; }
  .card .value.purple { color: #bc8cff; }
  .card .value.orange { color: #d29922; }
  .chart-container { background: #161b22; border: 1px solid #30363d; border-radius: 8px; padding: 20px; margin-bottom: 16px; }
  .chart-title { font-size: 0.95rem; color: #f0f6fc; margin-bottom: 12px; }
  .chart-wrap { position: relative; height: 280px; }
  .scenario-bar { height: 30px; position: relative; margin-bottom: 16px; display: flex; border-radius: 4px; overflow: hidden; font-size: 0.7rem; }
  .scenario-bar div { display: flex; align-items: center; justify-content: center; color: #0d1117; font-weight: 600; }
  .legend { display: flex; gap: 16px; flex-wrap: wrap; margin: 12px 0; font-size: 0.8rem; }
  .legend span { display: flex; align-items: center; gap: 4px; }
  .legend .dot { width: 10px; height: 10px; border-radius: 2px; display: inline-block; }
  .timestamp { color: #484f58; font-size: 0.75rem; margin-top: 24px; }
</style>
</head>
<body>
<h1>Compositor vs Bubbletea Rendering</h1>
<p class="subtitle">%d frames · %d×%d terminal · nav-like lipgloss layout</p>

<div class="grid">
  <div class="card">
    <div class="label">Bubbletea Total</div>
    <div class="value orange">%s</div>
  </div>
  <div class="card">
    <div class="label">Compositor Total</div>
    <div class="value green">%s</div>
  </div>
  <div class="card">
    <div class="label">Bytes Saved</div>
    <div class="value blue">%.1f%%</div>
  </div>
  <div class="card">
    <div class="label">Avg Dirty Cells / Frame</div>
    <div class="value purple">%.0f</div>
  </div>
</div>

<div class="chart-container">
  <div class="chart-title">Bytes Written Per Frame</div>
  <div class="legend">
    <span><span class="dot" style="background:#d29922"></span> Bubbletea (full repaint)</span>
    <span><span class="dot" style="background:#3fb950"></span> Compositor (dirty cells only)</span>
  </div>
  <div class="chart-wrap"><canvas id="bytesChart"></canvas></div>
</div>

<div class="chart-container">
  <div class="chart-title">Compositor Dirty Cells Per Frame</div>
  <div class="legend">
    <span><span class="dot" style="background:#bc8cff"></span> Dirty cells</span>
    <span><span class="dot" style="background:#484f58"></span> Total cells (%d)</span>
  </div>
  <div class="chart-wrap"><canvas id="cellsChart"></canvas></div>
</div>

<div class="chart-container">
  <div class="chart-title">Compositor Timing (µs per frame)</div>
  <div class="legend">
    <span><span class="dot" style="background:#58a6ff"></span> BlitANSI (parse)</span>
    <span><span class="dot" style="background:#f97583"></span> Flush (write)</span>
  </div>
  <div class="chart-wrap"><canvas id="timingChart"></canvas></div>
</div>

<div class="chart-container">
  <div class="chart-title">Byte Ratio: Compositor / Bubbletea</div>
  <div class="legend">
    <span><span class="dot" style="background:#58a6ff"></span> Ratio (lower = better)</span>
    <span>1.0 = identical output</span>
  </div>
  <div class="chart-wrap"><canvas id="ratioChart"></canvas></div>
</div>

<p class="timestamp">Generated %s</p>

<script>
const samples = %s;
const labels = samples.map(s => s.frame);
const scenarios = samples.map(s => s.scenario);

Chart.defaults.color = '#8b949e';
Chart.defaults.borderColor = '#21262d';
Chart.defaults.font.family = '-apple-system, BlinkMacSystemFont, SF Mono, monospace';

// Scenario color bands as background plugin
const scenarioColors = {
  'cursor move': '#1f6feb33',
  'typing filter': '#d2992233',
  'clear + browse': '#3fb95033',
  'tab switch': '#bc8cff33',
  'rapid scroll': '#f9758333',
  'idle': '#48505833',
  'browse': '#8b949e22',
};

const scenarioPlugin = {
  id: 'scenarioBands',
  beforeDraw(chart) {
    const { ctx, chartArea: {left, right, top, bottom}, scales: {x} } = chart;
    if (!x) return;
    let prev = scenarios[0], start = 0;
    for (let i = 1; i <= scenarios.length; i++) {
      if (i === scenarios.length || scenarios[i] !== prev) {
        const x0 = x.getPixelForValue(start);
        const x1 = x.getPixelForValue(i - 1);
        ctx.fillStyle = scenarioColors[prev] || '#8b949e11';
        ctx.fillRect(x0, top, x1 - x0, bottom - top);
        if (i < scenarios.length) { prev = scenarios[i]; start = i; }
      }
    }
  }
};

function makeChart(id, datasets, opts = {}) {
  new Chart(document.getElementById(id), {
    type: 'line',
    data: { labels, datasets },
    options: {
      responsive: true, maintainAspectRatio: false,
      animation: false,
      plugins: { legend: { display: false } },
      scales: {
        x: { title: { display: true, text: 'Frame' }, ticks: { maxTicksLimit: 20 } },
        y: { title: { display: true, text: opts.yLabel || '' }, beginAtZero: true, ...opts.yScale },
      },
      elements: { point: { radius: 0 }, line: { borderWidth: 1.5 } },
      interaction: { mode: 'index', intersect: false },
      ...opts.chartOpts,
    },
    plugins: [scenarioPlugin],
  });
}

// Bytes per frame
makeChart('bytesChart', [
  { label: 'Bubbletea', data: samples.map(s => s.bubbletea_bytes), borderColor: '#d29922', backgroundColor: '#d2992233' },
  { label: 'Compositor', data: samples.map(s => s.compositor_bytes), borderColor: '#3fb950', backgroundColor: '#3fb95033' },
], { yLabel: 'Bytes' });

// Dirty cells
makeChart('cellsChart', [
  { label: 'Dirty Cells', data: samples.map(s => s.dirty_cells), borderColor: '#bc8cff', backgroundColor: '#bc8cff33', fill: true },
], { yLabel: 'Cells', yScale: { max: %d } });

// Timing
makeChart('timingChart', [
  { label: 'BlitANSI', data: samples.map(s => s.blit_us), borderColor: '#58a6ff' },
  { label: 'Flush', data: samples.map(s => s.flush_us), borderColor: '#f97583' },
], { yLabel: 'µs' });

// Ratio
makeChart('ratioChart', [
  { label: 'Ratio', data: samples.map(s => s.bubbletea_bytes > 0 ? (s.compositor_bytes / s.bubbletea_bytes).toFixed(3) : 0), borderColor: '#58a6ff', backgroundColor: '#58a6ff22', fill: true },
], { yLabel: 'Ratio', yScale: { max: 1.2 } });
</script>
</body>
</html>`,
		numFrames, width, height,
		humanBytes(totalBT),
		humanBytes(int(stats.BytesWritten)),
		reduction,
		float64(stats.DirtyCellsFlushed)/float64(numFrames),
		width*height,
		time.Now().Format("2006-01-02 15:04:05"),
		string(samplesJSON),
		width*height,
	)
}
