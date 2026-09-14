package ui

import (
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
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

// admits decides whether a host belongs in the current view. A name that never
// resolved counts as down: the filter means "show me what is not answering",
// and an unresolved host is certainly not answering.
func (f Filter) admits(v monitor.HostView) bool {
	switch f {
	case FilterUp:
		return v.State == monitor.StateUp
	case FilterDown:
		return v.State == monitor.StateDown || v.State == monitor.StateUnresolved
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

// How long a newly-changed row stays highlighted, and how often the screen is
// repainted while one is. The frame ticker runs only while something is
// actually animating, so an idle table still costs nothing to display.
const (
	flashDuration = 1300 * time.Millisecond
	frameInterval = 90 * time.Millisecond

	// wheelLines is how far one notch of the mouse wheel moves the table.
	// Three matches what terminals and pagers do by default.
	wheelLines = 3
)

// Controller is the part of the monitor the table needs: a consistent
// snapshot to draw, the ability to retune the probe cadence, and whether the
// socket needed privileges. An interface rather than *monitor.Monitor so the
// model can be exercised without opening a socket.
type Controller interface {
	Snapshot() []monitor.HostView
	SetInterval(time.Duration)
	Privileged() bool
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

	// offset is the first matching row on screen. Held rather than derived so
	// the view stays put as hosts change state underneath it.
	offset int

	// Previous readings per host, for trend arrows. Indexed by host, so a row
	// moving around under a filter keeps its own history.
	prevLoss []float64
	prevAvg  []float64
	hasPrev  []bool

	// flashAt records when each host most recently changed state and which
	// way it went, which drives the highlight. Indexed by host, so the marker
	// follows the host even as a filter moves its row around.
	flashAt   []time.Time
	flashDown []bool
	animating bool

	// downSince is when each host stopped answering, and outlives the flash:
	// it is what the table reads out in place of a latency the host cannot
	// supply. Zero means the host is up, or has never been seen down.
	downSince []time.Time

	// now is injected so the animation can be tested without sleeping.
	now func() time.Time

	// bell rings the terminal when a host changes state, if the user has
	// asked for it. Injected so tests need not make noise.
	bell    func()
	belling bool

	paused   bool
	showHelp bool
	done     bool
	quitting bool
}

// NewModel builds the table model. finished is closed when probing has stopped
// of its own accord, which only happens in -c mode.
func NewModel(mon Controller, filter Filter, interval time.Duration, limit int, finished <-chan struct{}) Model {
	views := mon.Snapshot()

	return Model{
		mon:       mon,
		finished:  finished,
		filter:    filter,
		interval:  interval,
		limit:     limit,
		refresh:   refreshFor(interval),
		views:     views,
		prevLoss:  make([]float64, len(views)),
		prevAvg:   make([]float64, len(views)),
		hasPrev:   make([]bool, len(views)),
		flashAt:   make([]time.Time, len(views)),
		flashDown: make([]bool, len(views)),
		downSince: make([]time.Time, len(views)),
		now:       time.Now,
		bell:      ringBell,
		width:     defaultWidth,
		height:    24,
	}
}

// ringBell writes BEL to the terminal. It goes to stderr rather than through
// the renderer because BEL moves no cursor and paints nothing, so it cannot
// disturb the frame the renderer is drawing.
func ringBell() { fmt.Fprint(os.Stderr, "\a") }

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
		m.clampOffset()
		return m, nil

	case tea.MouseMsg:
		return m.handleMouse(msg), nil

	case tea.KeyMsg:
		return m.handleKey(msg)

	case finishedMsg:
		m.done = true
		m.refreshViews()
		return m, nil

	case tickMsg:
		// A paused table keeps probing and keeps collecting; it simply stops
		// taking new snapshots, so the numbers on screen hold still while the
		// statistics underneath stay honest.
		if !m.paused {
			m.refreshViews()
		}
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

// handleMouse maps the wheel onto the table. Only wheel events are consumed:
// clicks and drags are left alone so a stray click cannot move the view.
func (m Model) handleMouse(msg tea.MouseMsg) Model {
	if msg.Action != tea.MouseActionPress {
		return m
	}
	switch msg.Button {
	case tea.MouseButtonWheelUp:
		m.scroll(-wheelLines)
	case tea.MouseButtonWheelDown:
		m.scroll(wheelLines)
	}
	return m
}

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()

	// Help is modal: while it is up, any key dismisses it rather than doing
	// its usual job, so there is no way to act on a table you cannot see.
	if m.showHelp {
		if key == "ctrl+c" {
			m.quitting = true
			return m, tea.Quit
		}
		m.showHelp = false
		return m, nil
	}

	switch key {
	case "q", "esc", "ctrl+c":
		m.quitting = true
		return m, tea.Quit
	case "?":
		m.showHelp = true
	case "a":
		m.filter = FilterAll
		m.clampOffset()
	case "u":
		m.filter = FilterUp
		m.clampOffset()
	case "d":
		m.filter = FilterDown
		m.clampOffset()
	case "p":
		m.paused = !m.paused
	case "b":
		m.belling = !m.belling
	case "+", "=":
		// "=" is the unshifted key, so + works without reaching for shift.
		m.setInterval(slower(m.interval))
	case "-", "_":
		m.setInterval(faster(m.interval))
	case "up", "k":
		m.scroll(-1)
	case "down", "j":
		m.scroll(1)
	case "pgup", "b-page":
		m.scroll(-m.rowBudget())
	case "pgdown", " ":
		m.scroll(m.rowBudget())
	case "home", "g":
		m.offset = 0
	case "end", "G":
		m.offset = m.maxOffset()
	}
	return m, nil
}

// scroll moves the view by delta rows, stopping at either end.
func (m *Model) scroll(delta int) {
	m.offset += delta
	m.clampOffset()
}

// rowBudget is how many host rows fit between the header and the footer.
func (m Model) rowBudget() int {
	budget := m.height - headerLines - footerLines
	if budget < 1 {
		return 1
	}
	return budget
}

// maxOffset is the furthest the table can scroll: far enough to bring the last
// row into view, and no further, so the bottom of the list never floats up
// into empty space.
func (m Model) maxOffset() int {
	// One row goes to the scroll indicator whenever there is anything to
	// scroll, which is exactly when this matters.
	visible := m.rowBudget() - 1
	if visible < 1 {
		visible = 1
	}
	over := len(m.matching()) - visible
	if over < 0 {
		return 0
	}
	return over
}

func (m *Model) clampOffset() {
	if max := m.maxOffset(); m.offset > max {
		m.offset = max
	}
	if m.offset < 0 {
		m.offset = 0
	}
}

// refreshViews takes a fresh snapshot, first rolling the readings it is about
// to replace into the trend baseline. A host still waiting has no baseline
// worth comparing against, so it gets no arrow on its first real reading.
func (m *Model) refreshViews() {
	next := m.mon.Snapshot()
	rang := false

	for i := range m.views {
		if i < len(next) {
			rang = m.noteTransition(i, m.views[i].State, next[i].State) || rang
		}
		m.prevLoss[i] = m.views[i].Loss
		m.prevAvg[i] = float64(m.views[i].Avg)
		m.hasPrev[i] = m.views[i].State != monitor.StateWaiting
	}
	m.views = next

	if rang && m.belling && m.bell != nil {
		m.bell()
	}
	m.clampOffset()
}

// noteTransition records a change of state for host i and reports whether it
// is worth ringing the bell for.
//
// A host coming alive always flashes, first reply included, because that is
// news either way. A host dropping out flashes only if it was up beforehand:
// on `pingm 10.0.0.0/24` most addresses are dead from the start, and lighting
// all of them red at once would say nothing and drown out the one that
// matters. The bell is stricter still and stays silent on first readings, so
// starting the tool never sets off a fanfare.
func (m *Model) noteTransition(i int, prev, next monitor.State) bool {
	switch {
	case prev != monitor.StateUp && next == monitor.StateUp:
		m.flashAt[i] = m.now()
		m.flashDown[i] = false
		m.downSince[i] = time.Time{}
		return prev != monitor.StateWaiting

	case prev == monitor.StateUp && next != monitor.StateUp:
		m.flashAt[i] = m.now()
		m.flashDown[i] = true
		m.downSince[i] = m.now()
		return true

	case prev == monitor.StateWaiting && next != monitor.StateWaiting:
		// Down from the very first probe: worth timing, not worth announcing.
		m.downSince[i] = m.now()
		return false
	}
	return false
}

// flash is how strongly host i is highlighted right now, counting down from
// FlashMax at the moment it changed state to zero once flashDuration has
// elapsed.
func (m Model) flash(i int) Flash {
	if i >= len(m.flashAt) || m.flashAt[i].IsZero() {
		return Flash{}
	}
	elapsed := m.now().Sub(m.flashAt[i])
	if elapsed < 0 || elapsed >= flashDuration {
		return Flash{}
	}
	remaining := float64(flashDuration-elapsed) / float64(flashDuration)
	level := int(math.Ceil(remaining * float64(FlashMax)))
	if level > FlashMax {
		level = FlashMax
	}
	return Flash{Level: level, Down: m.flashDown[i]}
}

func (m Model) anyFlashing() bool {
	for i := range m.flashAt {
		if m.flash(i).Active() {
			return true
		}
	}
	return false
}

// downFor is how long host i has been unreachable, or false if it is up or has
// never been seen to fall over.
func (m Model) downFor(i int) (time.Duration, bool) {
	if i >= len(m.downSince) || m.downSince[i].IsZero() {
		return 0, false
	}
	if m.views[i].State == monitor.StateUp {
		return 0, false
	}
	d := m.now().Sub(m.downSince[i])
	if d < 0 {
		return 0, false
	}
	return d, true
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
	if m.showHelp {
		return m.helpView()
	}

	cols := NewColumns(m.views, m.width)
	matched := m.matching()

	var b strings.Builder
	b.WriteString(styleTitle.Render("pingm"))
	b.WriteString(styleHint.Render("  live multi-host ping"))
	b.WriteString("\n\n")
	b.WriteString(Header(cols))
	b.WriteString("\n")

	budget := m.rowBudget()

	switch {
	case len(matched) == 0:
		b.WriteString(styleMuted.Render(m.emptyNotice()))
		b.WriteString("\n")

	case len(matched) <= budget:
		for _, i := range matched {
			b.WriteString(m.renderRow(i, cols))
			b.WriteString("\n")
		}

	default:
		// One row is given over to the scroll indicator, which is only ever
		// drawn when there is something off screen to point at.
		visible := budget - 1
		start := m.offset
		if max := len(matched) - visible; start > max {
			start = max
		}
		if start < 0 {
			start = 0
		}
		end := start + visible
		if end > len(matched) {
			end = len(matched)
		}
		for _, i := range matched[start:end] {
			b.WriteString(m.renderRow(i, cols))
			b.WriteString("\n")
		}
		b.WriteString(styleMuted.Render(scrollNotice(start, len(matched)-end)))
		b.WriteString("\n")
	}

	b.WriteString("\n")
	b.WriteString(styleFooter.Render(truncate(m.statusLine(len(matched)), m.width)))
	b.WriteString("\n")
	b.WriteString(m.keyHints())
	return b.String()
}

func (m Model) renderRow(i int, cols Columns) string {
	down, hasDown := m.downFor(i)
	return Row(m.views[i], cols, RowState{
		LossTrend:  m.lossTrend(i),
		AvgTrend:   m.avgTrend(i),
		Flash:      m.flash(i),
		DownFor:    down,
		HasDownFor: hasDown,
	})
}

// scrollNotice says what is off screen in either direction, and how to reach
// it. Naming the wheel matters: nothing else on screen suggests the table
// scrolls at all.
func scrollNotice(above, below int) string {
	var parts []string
	if above > 0 {
		parts = append(parts, fmt.Sprintf("↑ %d above", above))
	}
	if below > 0 {
		parts = append(parts, fmt.Sprintf("↓ %d below", below))
	}
	return strings.Join(parts, "  ·  ") + "  ·  scroll with the wheel"
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

// tally counts the hosts in each state.
type tally struct{ up, down, dns, waiting int }

func (m Model) tally() tally {
	var t tally
	for _, v := range m.views {
		switch v.State {
		case monitor.StateUp:
			t.up++
		case monitor.StateDown:
			t.down++
		case monitor.StateUnresolved:
			t.dns++
		default:
			t.waiting++
		}
	}
	return t
}

// medianLatency is the middle of the current readings across every host that
// is up. The median rather than the mean, because one satellite link in a
// table of LAN hosts would drag a mean somewhere no host actually is.
func (m Model) medianLatency() (time.Duration, bool) {
	var live []time.Duration
	for _, v := range m.views {
		if v.State == monitor.StateUp && v.HasLatency {
			live = append(live, v.Last)
		}
	}
	if len(live) == 0 {
		return 0, false
	}
	sort.Slice(live, func(i, j int) bool { return live[i] < live[j] })
	return live[len(live)/2], true
}

func (m Model) statusLine(matched int) string {
	parts := []string{fmt.Sprintf("Interval: %s", m.interval)}

	// The roll-up earns its place only with a table too big to count by eye.
	if len(m.views) > 1 {
		t := m.tally()
		counts := []string{fmt.Sprintf("%d up", t.up), fmt.Sprintf("%d down", t.down)}
		if t.dns > 0 {
			counts = append(counts, fmt.Sprintf("%d dns", t.dns))
		}
		if t.waiting > 0 {
			counts = append(counts, fmt.Sprintf("%d waiting", t.waiting))
		}
		parts = append(parts, strings.Join(counts, " · "))
		if med, ok := m.medianLatency(); ok {
			parts = append(parts, "median "+FormatDuration(med, true))
		}
	}

	if m.limit > 0 {
		parts = append(parts, fmt.Sprintf("Count: %d", m.limit))
	}
	if m.filter != FilterAll {
		parts = append(parts, fmt.Sprintf("Filter: %s (%d of %d)", m.filter, matched, len(m.views)))
	}
	if m.belling {
		parts = append(parts, "Bell")
	}
	if m.mon != nil && m.mon.Privileged() {
		// Worth saying: it means the binary is running with elevated rights,
		// and it explains behaviour that differs from the unprivileged path.
		parts = append(parts, "raw socket")
	}
	if m.paused {
		parts = append(parts, "Paused (still probing)")
	}
	if m.done {
		parts = append(parts, "Done")
	}
	return strings.Join(parts, "  ·  ")
}

func (m Model) keyHints() string {
	hints := []string{
		key("a", "all"),
		key("u", "up"),
		key("d", "down"),
		key("+/-", "rate"),
		key("p", "pause"),
		key("?", "help"),
		key("q", "quit"),
	}
	legend := styleBetter.Render(glyphBetter) + styleFooter.Render(" better  ") +
		styleWorse.Render(glyphWorse) + styleFooter.Render(" worse")
	return strings.Join(hints, styleFooter.Render("  ")) + styleFooter.Render("   ·   ") + legend
}

func key(k, label string) string {
	return styleKeyName.Render(k) + styleFooter.Render(" "+label)
}

// helpBorderCost is what the rounded border and its padding add to each side
// of the help box, in display cells.
const helpBorderCost = 6

// helpView replaces the table rather than floating over it. A terminal has no
// real z-order, and a half-covered table is harder to read than none at all.
//
// It degrades in two steps as the terminal narrows: first the frame goes,
// because the keys are the point and a box spilling off the right edge helps
// nobody, and then the descriptions are truncated. The key column itself is
// never touched — a key you cannot read is a key you cannot press.
func (m Model) helpView() string {
	rows := [][2]string{
		{"a / u / d", "show all hosts, only up, or only down"},
		{"wheel", "scroll the table"},
		{"↑ ↓ / j k", "scroll a row at a time"},
		{"PgUp/PgDn", "scroll a page at a time"},
		{"g / G", "jump to the top or the bottom"},
		{"+ / -", "probe slower or faster (= and _ work too)"},
		{"p", "pause the display; probing carries on"},
		{"b", "ring the bell when a host changes state"},
		{"?", "show this help"},
		{"q", "quit (Esc and Ctrl-C also work)"},
	}

	const gap = 3
	keyWidth, textWidth := 0, 0
	for _, r := range rows {
		if w := lipgloss.Width(r[0]); w > keyWidth {
			keyWidth = w
		}
		if w := lipgloss.Width(r[1]); w > textWidth {
			textWidth = w
		}
	}

	framed := keyWidth+gap+textWidth+helpBorderCost <= m.width
	avail := m.width
	if framed {
		avail -= helpBorderCost
	}
	descWidth := avail - keyWidth - gap
	if descWidth < 1 {
		descWidth = 1
	}

	var b strings.Builder
	b.WriteString(styleHelpTitle.Render(truncate("pingm — keys", avail)))
	b.WriteString("\n\n")
	for _, r := range rows {
		b.WriteString(left(styleKeyName, r[0], keyWidth))
		b.WriteString(styleFooter.Render(pad(gap) + truncate(r[1], descWidth)))
		b.WriteString("\n")
	}
	b.WriteString("\n")
	b.WriteString(styleHint.Render(truncate("press any key to go back", avail)))

	body := b.String()
	if framed {
		body = styleHelpBox.Render(body)
	}
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, body)
}
