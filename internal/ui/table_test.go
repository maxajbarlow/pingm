package ui

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/maxajbarlow/pingm/internal/monitor"
	"github.com/muesli/termenv"
)

func TestMain(m *testing.M) {
	// Render without colour so assertions can match plain text. The profile
	// constants run TrueColor, ANSI256, ANSI, Ascii — so Ascii is not zero.
	lipgloss.SetColorProfile(termenv.Ascii)
	m.Run()
}

func TestFormatDurationScalesPrecisionToMagnitude(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{80 * time.Microsecond, "0.080 ms"},
		{999 * time.Microsecond, "0.999 ms"},
		{5500 * time.Microsecond, "5.50 ms"},
		{12300 * time.Microsecond, "12.3 ms"},
		{340 * time.Millisecond, "340.0 ms"},
	}
	for _, tc := range cases {
		if got := FormatDuration(tc.d, true); got != tc.want {
			t.Errorf("FormatDuration(%v) = %q, want %q", tc.d, got, tc.want)
		}
	}
}

func TestFormatDurationShowsDashWhenUnknown(t *testing.T) {
	if got := FormatDuration(5*time.Millisecond, false); got != glyphNone {
		t.Errorf("FormatDuration(unknown) = %q, want %q", got, glyphNone)
	}
}

func TestFormatLoss(t *testing.T) {
	if got := FormatLoss(33.333, true); got != "33.3%" {
		t.Errorf("FormatLoss(33.333) = %q, want 33.3%%", got)
	}
	if got := FormatLoss(0, false); got != glyphNone {
		t.Errorf("FormatLoss(unknown) = %q, want %q", got, glyphNone)
	}
}

func TestTrendOf(t *testing.T) {
	cases := []struct {
		name       string
		curr, prev float64
		hasPrev    bool
		want       Trend
	}{
		{"no previous reading", 5, 0, false, TrendNone},
		{"unchanged", 5, 5, true, TrendNone},
		{"fell is better", 4, 5, true, TrendBetter},
		{"rose is worse", 6, 5, true, TrendWorse},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := TrendOf(tc.curr, tc.prev, tc.hasPrev); got != tc.want {
				t.Errorf("TrendOf(%v,%v,%v) = %v, want %v", tc.curr, tc.prev, tc.hasPrev, got, tc.want)
			}
		})
	}
}

func upView(host string) monitor.HostView {
	return monitor.HostView{
		Display: host, State: monitor.StateUp,
		Last: 12300 * time.Microsecond, Min: 10 * time.Millisecond,
		Max: 15 * time.Millisecond, Avg: 12 * time.Millisecond,
		Loss: 0, HasLatency: true, HasHistory: true,
	}
}

func TestRowShowsUpHostWithLatency(t *testing.T) {
	got := Row(upView("10.0.0.1"), Columns{Host: 12}, TrendNone, TrendNone, FlashNone)
	for _, want := range []string{"10.0.0.1", glyphUp + " UP", "12.3 ms", "0.0%"} {
		if !strings.Contains(got, want) {
			t.Errorf("row %q missing %q", got, want)
		}
	}
}

// A down host keeps its lifetime statistics but has no current latency.
func TestRowForDownHostHidesLatencyButKeepsHistory(t *testing.T) {
	v := upView("10.0.0.2")
	v.State = monitor.StateDown
	v.HasLatency = false
	v.Loss = 25

	got := Row(v, Columns{Host: 12}, TrendNone, TrendNone, FlashNone)
	if !strings.Contains(got, glyphDown+" DOWN") {
		t.Errorf("row %q missing DOWN status", got)
	}
	if !strings.Contains(got, "25.0%") {
		t.Errorf("row %q missing loss", got)
	}
	if !strings.Contains(got, "10.0 ms") || !strings.Contains(got, "15.0 ms") {
		t.Errorf("row %q dropped its min/max history", got)
	}
}

func TestRowForWaitingHostShowsDashes(t *testing.T) {
	v := monitor.HostView{Display: "10.0.0.3", State: monitor.StateWaiting}
	got := Row(v, Columns{Host: 12}, TrendNone, TrendNone, FlashNone)
	if !strings.Contains(got, "WAIT") {
		t.Errorf("row %q missing WAIT", got)
	}
	if strings.Count(got, glyphNone) < 4 {
		t.Errorf("row %q should dash out every metric, got %d", got, strings.Count(got, glyphNone))
	}
}

func TestRowIncludesTrendArrows(t *testing.T) {
	got := Row(upView("h"), Columns{Host: 4}, TrendWorse, TrendBetter, FlashNone)
	if !strings.Contains(got, glyphWorse) {
		t.Errorf("row %q missing the worse arrow", got)
	}
	if !strings.Contains(got, glyphBetter) {
		t.Errorf("row %q missing the better arrow", got)
	}
}

// Every row must occupy the same width, or the columns visibly shear.
func TestRowsAreUniformWidth(t *testing.T) {
	cols := Columns{Host: 15}
	up := upView("10.0.0.1")

	down := up
	down.State = monitor.StateDown
	down.HasLatency = false
	down.Loss = 100

	waiting := monitor.HostView{Display: "some-host.example.com", State: monitor.StateWaiting}

	want := lipgloss.Width(Row(up, cols, TrendNone, TrendNone, FlashNone))
	for name, v := range map[string]monitor.HostView{"down": down, "waiting": waiting} {
		for _, tr := range []Trend{TrendNone, TrendBetter, TrendWorse} {
			got := lipgloss.Width(Row(v, cols, tr, tr, FlashNone))
			if got != want {
				t.Errorf("%s row with trend %v is %d wide, want %d", name, tr, got, want)
			}
		}
	}
}

func TestRowWidthMatchesHeaderWidth(t *testing.T) {
	cols := Columns{Host: 15}
	rule := strings.Split(Header(cols), "\n")[1]

	if got, want := lipgloss.Width(rule), cols.TotalWidth(); got != want {
		t.Errorf("rule width = %d, want TotalWidth %d", got, want)
	}
	if got := lipgloss.Width(Row(upView("10.0.0.1"), cols, TrendNone, TrendNone, FlashNone)); got != cols.TotalWidth() {
		t.Errorf("row width = %d, want TotalWidth %d", got, cols.TotalWidth())
	}
}

func TestNewColumnsSizesToLongestHost(t *testing.T) {
	views := []monitor.HostView{{Display: "a"}, {Display: "much-longer-host.example"}, {Display: "bb"}}
	if got, want := NewColumns(views).Host, len("much-longer-host.example"); got != want {
		t.Errorf("Host width = %d, want %d", got, want)
	}
}

func TestNewColumnsNeverGoesBelowTheLabel(t *testing.T) {
	if got := NewColumns([]monitor.HostView{{Display: "a"}}).Host; got != len("HOST") {
		t.Errorf("Host width = %d, want %d", got, len("HOST"))
	}
}

func TestNewColumnsCapsRunawayHostnames(t *testing.T) {
	long := strings.Repeat("x", 200)
	if got := NewColumns([]monitor.HostView{{Display: long}}).Host; got != 40 {
		t.Errorf("Host width = %d, want it capped at 40", got)
	}
}

func TestTruncateAddsEllipsisWithinWidth(t *testing.T) {
	got := truncate("abcdefghij", 5)
	if lipgloss.Width(got) > 5 {
		t.Errorf("truncate produced %q (%d wide), want <= 5", got, lipgloss.Width(got))
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("truncate = %q, want an ellipsis suffix", got)
	}
}

func TestTruncateLeavesShortStringsAlone(t *testing.T) {
	if got := truncate("abc", 10); got != "abc" {
		t.Errorf("truncate = %q, want %q", got, "abc")
	}
}

// ansi matches a CSI escape sequence. The Ascii colour profile drops colour
// but keeps attributes such as bold, so styled output still carries escapes.
var ansi = regexp.MustCompile(`\x1b\[[0-9;?]*[A-Za-z]`)

// plain strips ANSI escapes so positions can be measured in display columns.
func plain(s string) string { return ansi.ReplaceAllString(s, "") }

// endColumn is the display column just past the given text.
func endColumn(t *testing.T, line, text string) int {
	t.Helper()
	idx := strings.Index(line, text)
	if idx < 0 {
		t.Fatalf("%q not found in %q", text, line)
	}
	return lipgloss.Width(line[:idx]) + lipgloss.Width(text)
}

// A column label must sit over its values, not over the trend-arrow slot.
func TestHeaderLabelsAlignWithValueColumns(t *testing.T) {
	cols := Columns{Host: 10}
	labels := plain(strings.Split(Header(cols), "\n")[0])
	row := plain(Row(upView("10.0.0.1"), cols, TrendNone, TrendNone, FlashNone))

	for _, probe := range []struct{ label, value string }{
		{"LATENCY", "12.3 ms"},
		{"LOSS", "0.0%"},
		{"MIN", "10.0 ms"},
		{"AVG", "12.0 ms"},
		{"MAX", "15.0 ms"},
	} {
		if got, want := endColumn(t, labels, probe.label), endColumn(t, row, probe.value); got != want {
			t.Errorf("%s label ends at column %d but its value ends at %d", probe.label, got, want)
		}
	}
}

// --- Alive highlight ------------------------------------------------------

// A flashing row must be exactly as wide as a calm one, or the whole table
// shears sideways for the duration of the animation.
func TestFlashingRowKeepsTheSameWidth(t *testing.T) {
	cols := Columns{Host: 15}
	want := lipgloss.Width(Row(upView("10.0.0.1"), cols, TrendNone, TrendNone, FlashNone))

	for f := FlashNone; f <= FlashMax; f++ {
		got := lipgloss.Width(Row(upView("10.0.0.1"), cols, TrendNone, TrendNone, f))
		if got != want {
			t.Errorf("row at flash %d is %d wide, want %d", f, got, want)
		}
	}
}

func TestFlashingRowStillMatchesTheHeaderWidth(t *testing.T) {
	cols := Columns{Host: 15}
	got := lipgloss.Width(Row(upView("10.0.0.1"), cols, TrendNone, TrendNone, FlashMax))
	if got != cols.TotalWidth() {
		t.Errorf("flashing row width = %d, want TotalWidth %d", got, cols.TotalWidth())
	}
}

func TestFlashDrawsAMarkerOnlyWhileActive(t *testing.T) {
	cols := Columns{Host: 12}

	calm := plain(Row(upView("10.0.0.1"), cols, TrendNone, TrendNone, FlashNone))
	if strings.Contains(calm, glyphFlash) {
		t.Errorf("a calm row carries a flash marker: %q", calm)
	}

	for f := Flash(1); f <= FlashMax; f++ {
		lit := plain(Row(upView("10.0.0.1"), cols, TrendNone, TrendNone, f))
		if !strings.Contains(lit, glyphFlash) {
			t.Errorf("row at flash %d has no marker: %q", f, lit)
		}
	}
}

func TestFlashMarkerFadesThroughDistinctColours(t *testing.T) {
	// The suite renders without colour, but the whole point of the fade is
	// colour, so switch it on for this one test.
	lipgloss.SetColorProfile(termenv.ANSI256)
	defer lipgloss.SetColorProfile(termenv.Ascii)

	seen := map[string]bool{}
	for f := Flash(1); f <= FlashMax; f++ {
		seen[f.marker()] = true
	}
	if len(seen) != int(FlashMax) {
		t.Errorf("got %d distinct marker colours, want %d", len(seen), FlashMax)
	}
}

func TestFlashOutOfRangeRendersAsCalm(t *testing.T) {
	cols := Columns{Host: 12}
	calm := Row(upView("h"), cols, TrendNone, TrendNone, FlashNone)

	for _, f := range []Flash{-1, FlashMax + 1, 99} {
		if got := Row(upView("h"), cols, TrendNone, TrendNone, f); got != calm {
			t.Errorf("flash %d did not fall back to a calm row", f)
		}
	}
}

// startColumn is the display column at which the given text begins.
func startColumn(t *testing.T, line, text string) int {
	t.Helper()
	idx := strings.Index(line, text)
	if idx < 0 {
		t.Fatalf("%q not found in %q", text, line)
	}
	return lipgloss.Width(line[:idx])
}

// HOST is left-aligned, so the label and the hostname must share a start
// column — and a flashing row must not push its hostname sideways.
func TestHeaderReservesTheMarkerColumn(t *testing.T) {
	cols := Columns{Host: 10}
	labels := plain(strings.Split(Header(cols), "\n")[0])

	want := startColumn(t, labels, "HOST")
	if want != wFlash {
		t.Errorf("HOST starts at column %d, want the marker column reserved (%d)", want, wFlash)
	}
	for _, f := range []Flash{FlashNone, FlashMax} {
		row := plain(Row(upView("10.0.0.1"), cols, TrendNone, TrendNone, f))
		if got := startColumn(t, row, "10.0.0.1"); got != want {
			t.Errorf("hostname at flash %d starts at column %d, want %d", f, got, want)
		}
	}
}
