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
	wTrend   = 2 // Arrow plus its leading space.
	gutter   = 2
)

// Columns carries the one width that depends on the data.
type Columns struct{ Host int }

// NewColumns sizes the host column to the longest name, within a sane bound so
// one very long hostname cannot squeeze every other column off screen.
func NewColumns(views []monitor.HostView) Columns {
	width := len("HOST")
	for _, v := range views {
		if w := lipgloss.Width(v.Display); w > width {
			width = w
		}
	}
	if width > 40 {
		width = 40
	}
	return Columns{Host: width}
}

// TotalWidth is the full table width, used to size the rule under the header.
func (c Columns) TotalWidth() int {
	cols := []int{c.Host, wStatus, wLatency + wTrend, wLoss + wTrend, wStat, wStat + wTrend, wStat}
	total := 0
	for _, w := range cols {
		total += w
	}
	return total + gutter*(len(cols)-1)
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

// Header renders the column labels and the rule beneath them.
func Header(c Columns) string {
	// Columns carrying a trend arrow reserve wTrend cells to its right, so
	// their label is aligned to the value's field rather than to the arrow.
	labels := left(styleHeader, "HOST", c.Host) +
		pad(gutter) + left(styleHeader, "STATUS", wStatus) +
		pad(gutter) + right(styleHeader, "LATENCY", wLatency) + pad(wTrend) +
		pad(gutter) + right(styleHeader, "LOSS", wLoss) + pad(wTrend) +
		pad(gutter) + right(styleHeader, "MIN", wStat) +
		pad(gutter) + right(styleHeader, "AVG", wStat) + pad(wTrend) +
		pad(gutter) + right(styleHeader, "MAX", wStat)

	rule := styleRule.Render(strings.Repeat("─", c.TotalWidth()))
	return labels + "\n" + rule
}

// Row renders one host. Numeric columns are right-aligned so magnitudes line
// up and an outlier is visible without reading every digit.
func Row(v monitor.HostView, c Columns, lossTrend, avgTrend Trend) string {
	host := left(styleHost, truncate(v.Display, c.Host), c.Host)

	var status string
	switch v.State {
	case monitor.StateUp:
		status = left(styleUp, glyphUp+" UP", wStatus)
	case monitor.StateDown:
		status = left(styleDown, glyphDown+" DOWN", wStatus)
	default:
		status = left(styleWait, "WAIT", wStatus)
	}

	latency := rightTrend(valueStyle(v.HasLatency), FormatDuration(v.Last, v.HasLatency), TrendNone, wLatency)
	loss := rightTrend(lossStyle(v), FormatLoss(v.Loss, v.State != monitor.StateWaiting), lossTrend, wLoss)
	minCell := right(valueStyle(v.HasHistory), FormatDuration(v.Min, v.HasHistory), wStat)
	avgCell := rightTrend(valueStyle(v.HasHistory), FormatDuration(v.Avg, v.HasHistory), avgTrend, wStat)
	maxCell := right(valueStyle(v.HasHistory), FormatDuration(v.Max, v.HasHistory), wStat)

	return host + pad(gutter) + status +
		pad(gutter) + latency +
		pad(gutter) + loss +
		pad(gutter) + minCell +
		pad(gutter) + avgCell +
		pad(gutter) + maxCell
}

// lossStyle tints loss once it is non-zero, so partial loss stands out on a
// host that is still nominally up — the state that usually matters most.
func lossStyle(v monitor.HostView) lipgloss.Style {
	switch {
	case v.State == monitor.StateWaiting:
		return styleFaint
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

func pad(n int) string { return strings.Repeat(" ", n) }

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
