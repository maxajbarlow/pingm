package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/maxajbarlow/pingm/internal/monitor"
)

// Trend describes how a metric moved since the previous refresh. Lower is
// better for everything shown here — less loss, less latency.
type Trend int

const (
	TrendNone Trend = iota
	TrendBetter
	TrendWorse
)

// Column widths, in display cells. HOST is sized from the host list; the rest
// are fixed so the table does not shuffle as values change.
const (
	wStatus  = 7
	wLatency = 10
	wLoss    = 8
	wStat    = 10
	wJitter  = 10
	wSpark   = monitor.RecentSamples
	wTrend   = 2 // Arrow plus its leading space.
	wFlash   = 2 // Left marker plus its trailing space.
	gutter   = 2

	// The host column is elastic between these bounds: wide enough that a
	// truncated name is still recognisable, narrow enough that one very long
	// hostname cannot squeeze every other column off screen.
	minHost = 12
	maxHost = 40

	// defaultWidth stands in when the terminal size is not yet known, which
	// is true for the first frame before Bubble Tea reports it.
	defaultWidth = 80
)

// FlashMax is the highest flash level, reached the instant a host changes
// state; it counts down to zero over flashDuration.
const FlashMax = len(flashRamp)

// Both ramps are indexed by the same level, so they must stay the same length.
var _ = [1]struct{}{}[len(flashRamp)-len(flashDownRamp)]

// Flash is the highlight on a row whose host has just changed state. The zero
// value is a calm row.
type Flash struct {
	Level int  // FlashMax the instant it happened, counting down to zero.
	Down  bool // Whether the change was a host dropping out rather than coming alive.
}

// Active reports whether this row should currently be highlighted.
func (f Flash) Active() bool { return f.Level > 0 && f.Level <= FlashMax }

// colour is the ramp entry for the current level. FlashMax is freshest, so it
// maps to the first (brightest) entry.
func (f Flash) colour() lipgloss.AdaptiveColor {
	if f.Down {
		return flashDownRamp[FlashMax-f.Level]
	}
	return flashRamp[FlashMax-f.Level]
}

// marker renders the left-edge highlight for a row, or blank space when the
// row is not flashing. It always occupies wFlash cells so nothing shifts.
func (f Flash) marker() string {
	if !f.Active() {
		return pad(wFlash)
	}
	return lipgloss.NewStyle().Foreground(f.colour()).Render(glyphFlash) + pad(wFlash-1)
}

// hostStyle brightens a flashing row's name alongside the marker, so the
// highlight reads as belonging to the row rather than sitting beside it.
func (f Flash) hostStyle() lipgloss.Style {
	if !f.Active() {
		return styleHost
	}
	return lipgloss.NewStyle().Foreground(f.colour())
}

// Columns is the table layout for the current terminal: the one width that
// depends on the data, plus which optional columns there is room for.
type Columns struct {
	Host int

	ShowStats  bool // MIN and MAX.
	ShowJitter bool
	ShowSpark  bool
}

// NewColumns fits the table to the terminal, in two stages.
//
// First the essentials — host, status, latency, loss and avg — are made to
// fit, shrinking the host column if that is what it takes: a truncated name is
// still recognisable, whereas a dropped STATUS column is simply gone. Then the
// optional columns are added in descending order of usefulness while they
// still fit, so a wide terminal earns detail rather than a narrow one being
// punished.
//
// The trade only runs that way. A hostname is never truncated to make room for
// an optional column, because knowing which host a row is beats knowing its
// jitter.
func NewColumns(views []monitor.HostView, termWidth int) Columns {
	if termWidth <= 0 {
		termWidth = defaultWidth
	}

	host := len("HOST")
	for _, v := range views {
		if w := lipgloss.Width(v.Display); w > host {
			host = w
		}
	}
	if host > maxHost {
		host = maxHost
	}

	c := Columns{Host: host}
	for c.Host > minHost && c.TotalWidth() > termWidth {
		c.Host--
	}

	// Stop at the first column that does not fit rather than skipping it: the
	// order is a priority list, so nothing less useful should sneak in ahead
	// of something that was just too wide.
	for _, optional := range []*bool{&c.ShowStats, &c.ShowJitter, &c.ShowSpark} {
		*optional = true
		if c.TotalWidth() > termWidth {
			*optional = false
			break
		}
	}
	return c
}

// TotalWidth is the full table width, used to size the rule under the header.
func (c Columns) TotalWidth() int {
	total := wFlash + c.Host
	add := func(w int) { total += gutter + w }

	add(wStatus)
	add(wLatency + wTrend)
	add(wLoss + wTrend)
	if c.ShowStats {
		add(wStat) // MIN
	}
	add(wStat + wTrend) // AVG
	if c.ShowStats {
		add(wStat) // MAX
	}
	if c.ShowJitter {
		add(wJitter)
	}
	if c.ShowSpark {
		add(wSpark)
	}
	return total
}

// FormatDuration renders an RTT with precision that suits its magnitude, so a
// 0.08 ms loopback and a 340 ms satellite link are both readable. Returns an
// em dash when there is no reading to show.
func FormatDuration(d time.Duration, known bool) string {
	if !known {
		return glyphNone
	}
	msec := float64(d) / float64(time.Millisecond)
	switch {
	case msec < 1:
		return fmt.Sprintf("%.3f ms", msec)
	case msec < 10:
		return fmt.Sprintf("%.2f ms", msec)
	default:
		return fmt.Sprintf("%.1f ms", msec)
	}
}

// FormatLoss renders a packet-loss percentage.
func FormatLoss(pct float64, known bool) string {
	if !known {
		return glyphNone
	}
	return fmt.Sprintf("%.1f%%", pct)
}

// TrendOf compares a reading with the previous one. Equal values and a missing
// previous reading both mean no arrow.
func TrendOf(curr, prev float64, hasPrev bool) Trend {
	switch {
	case !hasPrev, curr == prev:
		return TrendNone
	case curr < prev:
		return TrendBetter
	default:
		return TrendWorse
	}
}

// Sparkline draws the recent probe outcomes as a run of bars, newest on the
// right, with unanswered probes shown as gaps rather than bars so loss can
// never be mistaken for latency.
//
// Bars are scaled to the window's own range rather than the host's lifetime
// range. A lifetime scale flattens as it widens, which would hide exactly the
// small recent wobble the sparkline exists to show.
func Sparkline(samples []monitor.Sample, width int) string {
	if width <= 0 {
		return ""
	}
	if len(samples) > width {
		samples = samples[len(samples)-width:]
	}
	if len(samples) == 0 {
		return pad(width)
	}

	var lo, hi time.Duration
	var any bool
	for _, s := range samples {
		if !s.OK {
			continue
		}
		if !any || s.RTT < lo {
			lo = s.RTT
		}
		if !any || s.RTT > hi {
			hi = s.RTT
		}
		any = true
	}

	var b strings.Builder
	for _, s := range samples {
		if !s.OK {
			b.WriteString(styleLost.Render(glyphLost))
			continue
		}
		// A flat run has no range to scale against, so it sits mid-height
		// rather than collapsing to the floor and reading as "fast".
		level := len(sparkRamp) / 2
		if hi > lo {
			level = int(float64(s.RTT-lo)/float64(hi-lo)*float64(len(sparkRamp)-1) + 0.5)
		}
		if level < 0 {
			level = 0
		}
		if level >= len(sparkRamp) {
			level = len(sparkRamp) - 1
		}
		b.WriteString(styleSpark.Render(string(sparkRamp[level])))
	}
	// Left-padded, so the newest sample sits at the right edge from the first
	// probe onwards and the line grows leftwards instead of shuffling.
	return pad(width-len(samples)) + b.String()
}

// Header renders the column labels and the rule beneath them.
func Header(c Columns) string {
	// Columns carrying a trend arrow reserve wTrend cells to its right, so
	// their label is aligned to the value's field rather than to the arrow.
	var b strings.Builder
	b.WriteString(pad(wFlash) + left(styleHeader, "HOST", c.Host))
	b.WriteString(pad(gutter) + left(styleHeader, "STATUS", wStatus))
	b.WriteString(pad(gutter) + right(styleHeader, "LATENCY", wLatency) + pad(wTrend))
	b.WriteString(pad(gutter) + right(styleHeader, "LOSS", wLoss) + pad(wTrend))
	if c.ShowStats {
		b.WriteString(pad(gutter) + right(styleHeader, "MIN", wStat))
	}
	b.WriteString(pad(gutter) + right(styleHeader, "AVG", wStat) + pad(wTrend))
	if c.ShowStats {
		b.WriteString(pad(gutter) + right(styleHeader, "MAX", wStat))
	}
	if c.ShowJitter {
		b.WriteString(pad(gutter) + right(styleHeader, "JITTER", wJitter))
	}
	if c.ShowSpark {
		b.WriteString(pad(gutter) + left(styleHeader, "RECENT", wSpark))
	}

	rule := styleRule.Render(strings.Repeat("─", c.TotalWidth()))
	return b.String() + "\n" + rule
}

// RowState is the per-row information the table derives rather than reads off
// a snapshot: how the numbers moved, whether the row is highlighted, and how
// long a fallen host has been gone. Bundled into a struct so adding the next
// one does not grow Row another positional argument.
type RowState struct {
	LossTrend  Trend
	AvgTrend   Trend
	Flash      Flash
	DownFor    time.Duration
	HasDownFor bool
}

// Row renders one host. Numeric columns are right-aligned so magnitudes line
// up and an outlier is visible without reading every digit.
func Row(v monitor.HostView, c Columns, s RowState) string {
	var b strings.Builder
	b.WriteString(s.Flash.marker() + left(s.Flash.hostStyle(), truncate(v.Display, c.Host), c.Host))
	b.WriteString(pad(gutter) + statusCell(v))
	b.WriteString(pad(gutter) + latencyCell(v, s))
	b.WriteString(pad(gutter) + rightTrend(lossStyle(v), FormatLoss(v.Loss, v.State != monitor.StateWaiting), s.LossTrend, wLoss))
	if c.ShowStats {
		b.WriteString(pad(gutter) + right(valueStyle(v.HasHistory), FormatDuration(v.Min, v.HasHistory), wStat))
	}
	b.WriteString(pad(gutter) + rightTrend(valueStyle(v.HasHistory), FormatDuration(v.Avg, v.HasHistory), s.AvgTrend, wStat))
	if c.ShowStats {
		b.WriteString(pad(gutter) + right(valueStyle(v.HasHistory), FormatDuration(v.Max, v.HasHistory), wStat))
	}
	if c.ShowJitter {
		b.WriteString(pad(gutter) + right(valueStyle(v.HasJitter), FormatDuration(v.Jitter, v.HasJitter), wJitter))
	}
	if c.ShowSpark {
		b.WriteString(pad(gutter) + Sparkline(v.Recent, wSpark))
	}
	return b.String()
}

// latencyCell shows the current round trip, or — for a host that cannot supply
// one — how long it has been gone. The cell is dead space on a down host
// otherwise, and "how long has this been broken" is the next thing anyone asks
// after "is it broken". Muted styling and the arrow keep it from being misread
// as a reading.
func latencyCell(v monitor.HostView, s RowState) string {
	if !v.HasLatency && s.HasDownFor {
		return rightTrend(styleMuted, "↓ "+FormatDownFor(s.DownFor), TrendNone, wLatency)
	}
	return rightTrend(valueStyle(v.HasLatency), FormatDuration(v.Last, v.HasLatency), TrendNone, wLatency)
}

// FormatDownFor renders an outage length compactly, coarsening as it grows:
// seconds matter while you are watching a reboot, hours only need to be
// approximate.
func FormatDownFor(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	default:
		return fmt.Sprintf("%dd%02dh", int(d.Hours())/24, int(d.Hours())%24)
	}
}

// statusCell names the host's condition. Unresolved is called out separately
// from down because it points at a different fix: the name is wrong, rather
// than the host being unreachable.
func statusCell(v monitor.HostView) string {
	switch v.State {
	case monitor.StateUp:
		return left(styleUp, glyphUp+" UP", wStatus)
	case monitor.StateDown:
		return left(styleDown, glyphDown+" DOWN", wStatus)
	case monitor.StateUnresolved:
		return left(styleWarn, glyphUnresolved+" DNS", wStatus)
	default:
		return left(styleWait, "WAIT", wStatus)
	}
}

// lossStyle tints loss once it is non-zero, so partial loss stands out on a
// host that is still nominally up — the state that usually matters most.
func lossStyle(v monitor.HostView) lipgloss.Style {
	switch {
	case v.State == monitor.StateWaiting:
		return styleFaint
	case v.State == monitor.StateUnresolved:
		return styleWarn
	case v.Loss >= 100:
		return styleDown
	case v.Loss > 0:
		return styleWarn
	default:
		return styleValue
	}
}

func valueStyle(known bool) lipgloss.Style {
	if known {
		return styleValue
	}
	return styleFaint
}

func trendMark(t Trend) string {
	switch t {
	case TrendBetter:
		return styleBetter.Render(glyphBetter)
	case TrendWorse:
		return styleWorse.Render(glyphWorse)
	default:
		return " "
	}
}

// rightTrend right-aligns a value in width and appends the trend arrow in a
// reserved slot, so a row does not shift sideways as arrows come and go.
func rightTrend(style lipgloss.Style, text string, t Trend, width int) string {
	return right(style, text, width) + " " + trendMark(t)
}

func left(style lipgloss.Style, text string, width int) string {
	return style.Width(width).Align(lipgloss.Left).Render(text)
}

func right(style lipgloss.Style, text string, width int) string {
	return style.Width(width).Align(lipgloss.Right).Render(text)
}

func pad(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.Repeat(" ", n)
}

func truncate(s string, width int) string {
	if lipgloss.Width(s) <= width {
		return s
	}
	runes := []rune(s)
	for len(runes) > 1 && lipgloss.Width(string(runes)+"…") > width {
		runes = runes[:len(runes)-1]
	}
	return string(runes) + "…"
}
