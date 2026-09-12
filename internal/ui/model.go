package ui

import (
	"fmt"
	"math"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/maxajbarlow/pingm/internal/monitor"
)

// Filter selects which hosts the table displays. It never changes what is
// probed, so hidden hosts keep accumulating accurate statistics.
type Filter int

const (
	FilterAll Filter = iota
	FilterUp
	FilterDown
)

func (f Filter) String() string {
	switch f {
	case FilterUp:
		return "up"
	case FilterDown:
		return "down"
	default:
		return "all"
	}
}

// ParseFilter accepts the flag spellings, including the online/offline wording.
func ParseFilter(s string) (Filter, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "up", "online":
		return FilterUp, nil
	case "down", "offline":
		return FilterDown, nil
	case "all", "any", "":
		return FilterAll, nil
	default:
		return FilterAll, fmt.Errorf("invalid filter %q (expected up, down, or all)", s)
	}
}

func (f Filter) admits(v monitor.HostView) bool {
	switch f {
	case FilterUp:
		return v.State == monitor.StateUp
	case FilterDown:
		return v.State == monitor.StateDown
	default:
		return true
	}
}

// Chrome lines around the host rows: title, blank, labels, rule above; blank
// plus two footer lines below.
const (
	headerLines = 4
	footerLines = 3
)

type tickMsg time.Time
type frameMsg time.Time
type finishedMsg struct{}

// How long a newly-alive row stays highlighted, and how often the screen is
// repainted while one is. The frame ticker runs only while something is
// actually animating, so an idle table still costs nothing to display.
const (
	flashDuration = 1300 * time.Millisecond
	frameInterval = 90 * time.Millisecond
)

// Controller is the part of the monitor the table needs: a consistent
// snapshot to draw, and the ability to retune the probe cadence. An interface
// rather than *monitor.Monitor so the model can be exercised without opening
// a socket.
type Controller interface {
	Snapshot() []monitor.HostView
	SetInterval(time.Duration)
}

// intervalLadder is what the +/- keys step through: a 1-2-5 sequence spanning
// 100ms to 10s. Stepping by rungs rather than by a fixed amount means the
// whole useful range is a few presses away at either end, and every stop is a
// round number worth reading in the footer.
var intervalLadder = [...]time.Duration{
	100 * time.Millisecond,
	200 * time.Millisecond,
	500 * time.Millisecond,
	1 * time.Second,
	2 * time.Second,
	5 * time.Second,
	10 * time.Second,
}

// slower returns the next rung above d. An interval that sits between rungs
// (from -i 300ms, say) moves to the next rung up rather than snapping first,
// and one already at or beyond the top is left alone.
func slower(d time.Duration) time.Duration {
	for _, step := range intervalLadder {
		if step > d {
			return step
		}
	}
	return d
}

// faster returns the next rung below d, leaving anything at or below the
// bottom rung alone — including an interval the user deliberately set lower
// than the ladder reaches.
func faster(d time.Duration) time.Duration {
	for i := len(intervalLadder) - 1; i >= 0; i-- {
		if intervalLadder[i] < d {
			return intervalLadder[i]
		}
	}
	return d
}

// refreshFor keeps redrawing in step with probing: repainting faster only
// reprints identical numbers, and the trend arrows are defined against the
// previous refresh.
func refreshFor(interval time.Duration) time.Duration {
	switch {
	case interval < 100*time.Millisecond:
		return 100 * time.Millisecond
	case interval > 2*time.Second:
		return 2 * time.Second
	default:
		return interval
	}
}

// Model is the Bubble Tea model driving the live table.
type Model struct {
	mon      Controller
	finished <-chan struct{}

	filter   Filter
	interval time.Duration
	limit    int
	refresh  time.Duration

	width, height int
	views         []monitor.HostView

	// Previous readings per host, for trend arrows. Indexed by host, so a row
	// moving around under a filter keeps its own history.
	prevLoss []float64
	prevAvg  []float64
	hasPrev  []bool

	// aliveAt records when each host most recently came up, which drives the
	// highlight. Indexed by host, so the marker follows the host even as a
	// filter moves its row around.
	aliveAt   []time.Time
	animating bool

	// now is injected so the animation can be tested without sleeping.
	now func() time.Time

	done     bool
	quitting bool
}

// NewModel builds the table model. finished is closed when probing has stopped
// of its own accord, which only happens in -c mode.
func NewModel(mon Controller, filter Filter, interval time.Duration, limit int, finished <-chan struct{}) Model {
	views := mon.Snapshot()

	return Model{
		mon:      mon,
		finished: finished,
		filter:   filter,
		interval: interval,
		limit:    limit,
		refresh:  refreshFor(interval),
		views:    views,
		prevLoss: make([]float64, len(views)),
		prevAvg:  make([]float64, len(views)),
		hasPrev:  make([]bool, len(views)),
		aliveAt:  make([]time.Time, len(views)),
		now:      time.Now,
		width:    80,
		height:   24,
	}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(m.tick(), m.waitForFinish())
}

func (m Model) frame() tea.Cmd {
	return tea.Tick(frameInterval, func(t time.Time) tea.Msg { return frameMsg(t) })
}

func (m Model) tick() tea.Cmd {
	return tea.Tick(m.refresh, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m Model) waitForFinish() tea.Cmd {
	if m.finished == nil {
		return nil
	}
	return func() tea.Msg {
		<-m.finished
		return finishedMsg{}
	}
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil

	case tea.KeyMsg:
		switch msg.String() {
		case "q", "esc", "ctrl+c":
			m.quitting = true
			return m, tea.Quit
		case "a":
			m.filter = FilterAll
		case "u":
			m.filter = FilterUp
		case "d":
			m.filter = FilterDown
		case "+", "=":
			// "=" is the unshifted key, so + works without reaching for shift.
			m.setInterval(slower(m.interval))
		case "-", "_":
			m.setInterval(faster(m.interval))
		}
		return m, nil

	case finishedMsg:
		m.done = true
		m.refreshViews()
		return m, nil

	case tickMsg:
		m.refreshViews()
		// Starting a highlight turns the frame ticker on; it switches itself
		// off again once every highlight has faded.
		if m.anyFlashing() && !m.animating {
			m.animating = true
			return m, tea.Batch(m.tick(), m.frame())
		}
		return m, m.tick()

	case frameMsg:
		if !m.anyFlashing() {
			m.animating = false
			return m, nil
		}
		return m, m.frame()
	}
	return m, nil
}

// refreshViews takes a fresh snapshot, first rolling the readings it is about
// to replace into the trend baseline. A host still waiting has no baseline
// worth comparing against, so it gets no arrow on its first real reading.
func (m *Model) refreshViews() {
	next := m.mon.Snapshot()

	for i := range m.views {
		// A host that was not up and now is has just come alive — whether
		// that is a recovery or its very first reply.
		if i < len(next) && m.views[i].State != monitor.StateUp && next[i].State == monitor.StateUp {
			m.aliveAt[i] = m.now()
		}
		m.prevLoss[i] = m.views[i].Loss
		m.prevAvg[i] = float64(m.views[i].Avg)
		m.hasPrev[i] = m.views[i].State != monitor.StateWaiting
	}
	m.views = next
}

// flash is how strongly host i is highlighted right now, counting down from
// FlashMax at the moment it came alive to FlashNone once flashDuration has
// elapsed.
func (m Model) flash(i int) Flash {
	if i >= len(m.aliveAt) || m.aliveAt[i].IsZero() {
		return FlashNone
	}
	elapsed := m.now().Sub(m.aliveAt[i])
	if elapsed < 0 || elapsed >= flashDuration {
		return FlashNone
	}
	remaining := float64(flashDuration-elapsed) / float64(flashDuration)
	level := Flash(math.Ceil(remaining * float64(FlashMax)))
	if level > FlashMax {
		level = FlashMax
	}
	return level
}

func (m Model) anyFlashing() bool {
	for i := range m.aliveAt {
		if m.flash(i) > FlashNone {
			return true
		}
	}
	return false
}

// setInterval retunes probing and redrawing together. A no-op change is
// skipped so pressing past either end of the ladder costs nothing.
func (m *Model) setInterval(d time.Duration) {
	if d == m.interval || d <= 0 {
		return
	}
	m.interval = d
	m.refresh = refreshFor(d)
	m.mon.SetInterval(d)
}

func (m Model) matching() []int {
	idx := make([]int, 0, len(m.views))
	for i, v := range m.views {
		if m.filter.admits(v) {
			idx = append(idx, i)
		}
	}
	return idx
}

func (m Model) View() string {
	if m.quitting {
		return ""
	}

	cols := NewColumns(m.views)
	matched := m.matching()

	var b strings.Builder
	b.WriteString(styleTitle.Render("pingm"))
	b.WriteString(styleHint.Render("  live multi-host ping"))
	b.WriteString("\n\n")
	b.WriteString(Header(cols))
	b.WriteString("\n")

	budget := m.height - headerLines - footerLines
	if budget < 1 {
		budget = 1
	}

	if len(matched) == 0 {
		b.WriteString(styleMuted.Render(m.emptyNotice()))
		b.WriteString("\n")
	} else {
		shown, hidden := matched, 0
		if len(shown) > budget {
			shown, hidden = shown[:budget-1], len(shown)-(budget-1)
		}
		for _, i := range shown {
			b.WriteString(Row(m.views[i], cols, m.lossTrend(i), m.avgTrend(i), m.flash(i)))
			b.WriteString("\n")
		}
		if hidden > 0 {
			b.WriteString(styleMuted.Render(fmt.Sprintf("… %d more hidden — resize the terminal to show them", hidden)))
			b.WriteString("\n")
		}
	}

	b.WriteString("\n")
	b.WriteString(styleFooter.Render(m.statusLine(len(matched))))
	b.WriteString("\n")
	b.WriteString(m.keyHints())
	return b.String()
}

func (m Model) lossTrend(i int) Trend {
	return TrendOf(m.views[i].Loss, m.prevLoss[i], m.hasPrev[i])
}

func (m Model) avgTrend(i int) Trend {
	return TrendOf(float64(m.views[i].Avg), m.prevAvg[i], m.hasPrev[i])
}

// emptyNotice explains an empty table in the terms of the active filter, and
// only claims a verdict once every host has actually been probed.
func (m Model) emptyNotice() string {
	if !m.allProbed() {
		return "Waiting for the first replies…"
	}
	switch m.filter {
	case FilterUp:
		return "No hosts are responding."
	case FilterDown:
		if len(m.views) == 1 {
			return "The host is responding."
		}
		return fmt.Sprintf("All %d hosts are responding.", len(m.views))
	default:
		return "No hosts to show."
	}
}

func (m Model) allProbed() bool {
	for _, v := range m.views {
		if v.State == monitor.StateWaiting {
			return false
		}
	}
	return true
}

func (m Model) statusLine(matched int) string {
	parts := []string{fmt.Sprintf("Interval: %s", m.interval)}
	if m.limit > 0 {
		parts = append(parts, fmt.Sprintf("Count: %d", m.limit))
	}
	if m.filter != FilterAll {
		parts = append(parts, fmt.Sprintf("Filter: %s (%d of %d)", m.filter, matched, len(m.views)))
	}
	if m.done {
		parts = append(parts, "Done")
	}
	return strings.Join(parts, "  ·  ")
}

func (m Model) keyHints() string {
	key := func(k, label string) string {
		return styleKeyName.Render(k) + styleFooter.Render(" "+label)
	}
	hints := []string{
		key("a", "all"),
		key("u", "up"),
		key("d", "down"),
		key("+/-", "rate"),
		key("q", "quit"),
	}
	legend := styleBetter.Render(glyphBetter) + styleFooter.Render(" better  ") +
		styleWorse.Render(glyphWorse) + styleFooter.Render(" worse")
	return strings.Join(hints, styleFooter.Render("  ")) + styleFooter.Render("   ·   ") + legend
}
