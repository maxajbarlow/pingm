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
		Jitter: 2 * time.Millisecond,
		Loss:   0, HasLatency: true, HasHistory: true, HasJitter: true,
	}
}

// testCols is the full table, every optional column on, as a wide terminal
// would render it. Tests that care about narrowing build their own.
func testCols(host int) Columns {
	return Columns{Host: host, ShowStats: true, ShowJitter: true, ShowSpark: true}
}

// wideTerminal is comfortably past the widest layout, so NewColumns keeps
// every column and tests see the full table.
const wideTerminal = 200

// rowState bundles the common no-trend, no-flash case.
func rowState(lossTrend, avgTrend Trend, f Flash) RowState {
	return RowState{LossTrend: lossTrend, AvgTrend: avgTrend, Flash: f}
}

func TestRowShowsUpHostWithLatency(t *testing.T) {
	got := Row(upView("10.0.0.1"), testCols(12), rowState(TrendNone, TrendNone, Flash{}))
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

	got := Row(v, testCols(12), rowState(TrendNone, TrendNone, Flash{}))
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
	got := Row(v, testCols(12), rowState(TrendNone, TrendNone, Flash{}))
	if !strings.Contains(got, "WAIT") {
		t.Errorf("row %q missing WAIT", got)
	}
	if strings.Count(got, glyphNone) < 4 {
		t.Errorf("row %q should dash out every metric, got %d", got, strings.Count(got, glyphNone))
	}
}

func TestRowIncludesTrendArrows(t *testing.T) {
	got := Row(upView("h"), testCols(4), rowState(TrendWorse, TrendBetter, Flash{}))
	if !strings.Contains(got, glyphWorse) {
		t.Errorf("row %q missing the worse arrow", got)
	}
	if !strings.Contains(got, glyphBetter) {
		t.Errorf("row %q missing the better arrow", got)
	}
}

// Every row must occupy the same width, or the columns visibly shear.
func TestRowsAreUniformWidth(t *testing.T) {
	cols := testCols(15)
	up := upView("10.0.0.1")

	down := up
	down.State = monitor.StateDown
	down.HasLatency = false
	down.Loss = 100

	waiting := monitor.HostView{Display: "some-host.example.com", State: monitor.StateWaiting}

	want := lipgloss.Width(Row(up, cols, rowState(TrendNone, TrendNone, Flash{})))
	for name, v := range map[string]monitor.HostView{"down": down, "waiting": waiting} {
		for _, tr := range []Trend{TrendNone, TrendBetter, TrendWorse} {
			got := lipgloss.Width(Row(v, cols, rowState(tr, tr, Flash{})))
			if got != want {
				t.Errorf("%s row with trend %v is %d wide, want %d", name, tr, got, want)
			}
		}
	}
}

func TestRowWidthMatchesHeaderWidth(t *testing.T) {
	cols := testCols(15)
	rule := strings.Split(Header(cols), "\n")[1]

	if got, want := lipgloss.Width(rule), cols.TotalWidth(); got != want {
		t.Errorf("rule width = %d, want TotalWidth %d", got, want)
	}
	if got := lipgloss.Width(Row(upView("10.0.0.1"), cols, rowState(TrendNone, TrendNone, Flash{}))); got != cols.TotalWidth() {
		t.Errorf("row width = %d, want TotalWidth %d", got, cols.TotalWidth())
	}
}

func TestNewColumnsSizesToLongestHost(t *testing.T) {
	views := []monitor.HostView{{Display: "a"}, {Display: "much-longer-host.example"}, {Display: "bb"}}
	if got, want := NewColumns(views, wideTerminal).Host, len("much-longer-host.example"); got != want {
		t.Errorf("Host width = %d, want %d", got, want)
	}
}

func TestNewColumnsNeverGoesBelowTheLabel(t *testing.T) {
	if got := NewColumns([]monitor.HostView{{Display: "a"}}, wideTerminal).Host; got != len("HOST") {
		t.Errorf("Host width = %d, want %d", got, len("HOST"))
	}
}

func TestNewColumnsCapsRunawayHostnames(t *testing.T) {
	long := strings.Repeat("x", 200)
	if got := NewColumns([]monitor.HostView{{Display: long}}, wideTerminal).Host; got != 40 {
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
	cols := testCols(10)
	labels := plain(strings.Split(Header(cols), "\n")[0])
	row := plain(Row(upView("10.0.0.1"), cols, rowState(TrendNone, TrendNone, Flash{})))

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
	cols := testCols(15)
	want := lipgloss.Width(Row(upView("10.0.0.1"), cols, rowState(TrendNone, TrendNone, Flash{})))

	for level := 0; level <= FlashMax; level++ {
		for _, down := range []bool{false, true} {
			f := Flash{Level: level, Down: down}
			got := lipgloss.Width(Row(upView("10.0.0.1"), cols, rowState(TrendNone, TrendNone, f)))
			if got != want {
				t.Errorf("row at flash %+v is %d wide, want %d", f, got, want)
			}
		}
	}
}

func TestFlashingRowStillMatchesTheHeaderWidth(t *testing.T) {
	cols := testCols(15)
	got := lipgloss.Width(Row(upView("10.0.0.1"), cols, rowState(TrendNone, TrendNone, Flash{Level: FlashMax})))
	if got != cols.TotalWidth() {
		t.Errorf("flashing row width = %d, want TotalWidth %d", got, cols.TotalWidth())
	}
}

func TestFlashDrawsAMarkerOnlyWhileActive(t *testing.T) {
	cols := testCols(12)

	calm := plain(Row(upView("10.0.0.1"), cols, rowState(TrendNone, TrendNone, Flash{})))
	if strings.Contains(calm, glyphFlash) {
		t.Errorf("a calm row carries a flash marker: %q", calm)
	}

	for level := 1; level <= FlashMax; level++ {
		lit := plain(Row(upView("10.0.0.1"), cols, rowState(TrendNone, TrendNone, Flash{Level: level})))
		if !strings.Contains(lit, glyphFlash) {
			t.Errorf("row at flash %d has no marker: %q", level, lit)
		}
	}
}

func TestFlashMarkerFadesThroughDistinctColours(t *testing.T) {
	// The suite renders without colour, but the whole point of the fade is
	// colour, so switch it on for this one test.
	lipgloss.SetColorProfile(termenv.ANSI256)
	defer lipgloss.SetColorProfile(termenv.Ascii)

	seen := map[string]bool{}
	for level := 1; level <= FlashMax; level++ {
		// Both directions share the level scale, so a full fade in each must
		// produce twice as many distinct colours between them.
		seen[Flash{Level: level}.marker()] = true
		seen[Flash{Level: level, Down: true}.marker()] = true
	}
	if want := 2 * FlashMax; len(seen) != want {
		t.Errorf("got %d distinct marker colours, want %d", len(seen), want)
	}
}

func TestFlashOutOfRangeRendersAsCalm(t *testing.T) {
	cols := testCols(12)
	calm := Row(upView("h"), cols, rowState(TrendNone, TrendNone, Flash{}))

	for _, f := range []Flash{{Level: -1}, {Level: FlashMax + 1}, {Level: 99}} {
		if got := Row(upView("h"), cols, rowState(TrendNone, TrendNone, f)); got != calm {
			t.Errorf("flash %+v did not fall back to a calm row", f)
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
	cols := testCols(10)
	labels := plain(strings.Split(Header(cols), "\n")[0])

	want := startColumn(t, labels, "HOST")
	if want != wFlash {
		t.Errorf("HOST starts at column %d, want the marker column reserved (%d)", want, wFlash)
	}
	for _, f := range []Flash{{}, {Level: FlashMax}, {Level: FlashMax, Down: true}} {
		row := plain(Row(upView("10.0.0.1"), cols, rowState(TrendNone, TrendNone, f)))
		if got := startColumn(t, row, "10.0.0.1"); got != want {
			t.Errorf("hostname at flash %+v starts at column %d, want %d", f, got, want)
		}
	}
}

// --- Responsive columns ---------------------------------------------------

// hostsOfWidth builds a view list whose longest name is exactly n characters.
func hostsOfWidth(n int) []monitor.HostView {
	return []monitor.HostView{{Display: strings.Repeat("h", n)}}
}

// A wide terminal earns every column; a narrow one keeps only the essentials.
func TestColumnsAppearAsTheTerminalWidens(t *testing.T) {
	views := hostsOfWidth(15)

	cases := []struct {
		width                int
		stats, jitter, spark bool
	}{
		{70, false, false, false},
		{95, true, false, false},
		{110, true, true, false},
		{130, true, true, true},
		{400, true, true, true},
	}
	for _, tc := range cases {
		c := NewColumns(views, tc.width)
		if c.ShowStats != tc.stats || c.ShowJitter != tc.jitter || c.ShowSpark != tc.spark {
			t.Errorf("at width %d: stats=%v jitter=%v spark=%v, want %v/%v/%v",
				tc.width, c.ShowStats, c.ShowJitter, c.ShowSpark, tc.stats, tc.jitter, tc.spark)
		}
	}
}

// Whatever is dropped, the table must still fit the terminal it was sized for.
func TestColumnsFitTheTerminalTheyWereSizedFor(t *testing.T) {
	for _, hostLen := range []int{4, 15, 40, 200} {
		views := hostsOfWidth(hostLen)
		for width := 40; width <= 200; width += 7 {
			c := NewColumns(views, width)
			// Below the floor the essentials simply cannot fit, and a
			// truncated table beats no table.
			if total := c.TotalWidth(); total > width && c.Host > minHost {
				t.Errorf("host %d at width %d: table is %d wide with host column still at %d",
					hostLen, width, total, c.Host)
			}
		}
	}
}

// When the essentials will not fit, the host column gives ground rather than
// STATUS or LOSS disappearing: a truncated name is still recognisable.
func TestHostColumnShrinksToKeepTheEssentialColumns(t *testing.T) {
	c := NewColumns(hostsOfWidth(40), 80)

	if c.Host >= 40 {
		if c.TotalWidth() > 80 {
			t.Errorf("host column stayed at %d and overflowed to %d wide", c.Host, c.TotalWidth())
		}
	}
	if c.Host < minHost {
		t.Errorf("host column shrank to %d, below the floor of %d", c.Host, minHost)
	}
	if c.TotalWidth() > 80 {
		t.Errorf("table is %d wide in an 80-column terminal", c.TotalWidth())
	}
}

// The opposite trade is never made: an optional column is worth less than the
// hostname it would have to truncate to fit.
func TestHostColumnIsNeverTradedForAnOptionalColumn(t *testing.T) {
	views := hostsOfWidth(40)
	roomy := NewColumns(views, 400).Host

	for _, width := range []int{100, 120, 140} {
		if got := NewColumns(views, width).Host; got != roomy {
			t.Errorf("at width %d the host column shrank to %d to make room for a column, want %d",
				width, got, roomy)
		}
	}
}

// Dropping a column must take its label with it.
func TestHeaderAndRowAgreeOnWhichColumnsExist(t *testing.T) {
	views := hostsOfWidth(15)
	for _, width := range []int{70, 95, 110, 130} {
		c := NewColumns(views, width)
		labels := plain(strings.Split(Header(c), "\n")[0])
		row := plain(Row(upView("hhhhhhhhhhhhhhh"), c, rowState(TrendNone, TrendNone, Flash{})))

		if lipgloss.Width(labels) != lipgloss.Width(row) {
			t.Errorf("at width %d: header is %d wide but a row is %d",
				width, lipgloss.Width(labels), lipgloss.Width(row))
		}
		if got, want := strings.Contains(labels, "MIN"), c.ShowStats; got != want {
			t.Errorf("at width %d: MIN label present=%v, want %v", width, got, want)
		}
		if got, want := strings.Contains(labels, "JITTER"), c.ShowJitter; got != want {
			t.Errorf("at width %d: JITTER label present=%v, want %v", width, got, want)
		}
		if got, want := strings.Contains(labels, "RECENT"), c.ShowSpark; got != want {
			t.Errorf("at width %d: RECENT label present=%v, want %v", width, got, want)
		}
	}
}

func TestUnknownTerminalWidthFallsBackToADefault(t *testing.T) {
	views := hostsOfWidth(15)
	if got, want := NewColumns(views, 0), NewColumns(views, defaultWidth); got != want {
		t.Errorf("NewColumns with no width = %+v, want the default layout %+v", got, want)
	}
}

// --- Jitter ---------------------------------------------------------------

func TestRowShowsJitterWhenThereIsRoom(t *testing.T) {
	v := upView("h")
	v.Jitter = 2 * time.Millisecond

	got := plain(Row(v, testCols(10), rowState(TrendNone, TrendNone, Flash{})))
	if !strings.Contains(got, "2.00 ms") {
		t.Errorf("row %q missing its jitter figure", got)
	}
}

// One reply has no spread, so the cell must say nothing rather than zero.
func TestRowDashesJitterWithoutEnoughReplies(t *testing.T) {
	v := upView("h")
	v.HasJitter = false

	cols := Columns{Host: 10, ShowJitter: true}
	got := plain(Row(v, cols, rowState(TrendNone, TrendNone, Flash{})))
	if strings.Contains(got, "0.000 ms") {
		t.Errorf("row %q printed a jitter of zero instead of dashing it out", got)
	}
	if !strings.Contains(got, glyphNone) {
		t.Errorf("row %q did not dash out its unknown jitter", got)
	}
}

// --- Sparkline ------------------------------------------------------------

func samples(rtts ...time.Duration) []monitor.Sample {
	out := make([]monitor.Sample, len(rtts))
	for i, d := range rtts {
		out[i] = monitor.Sample{RTT: d, OK: d >= 0}
	}
	return out
}

func TestSparklineIsAlwaysExactlyItsColumnWidth(t *testing.T) {
	for _, n := range []int{0, 1, 5, wSpark, wSpark + 20} {
		var s []monitor.Sample
		for i := 0; i < n; i++ {
			s = append(s, monitor.Sample{RTT: time.Duration(i) * time.Millisecond, OK: true})
		}
		if got := lipgloss.Width(Sparkline(s, wSpark)); got != wSpark {
			t.Errorf("%d samples rendered %d wide, want %d", n, got, wSpark)
		}
	}
}

// The newest sample must always sit at the right edge, so the line grows
// leftwards instead of shuffling under the reader.
func TestSparklineKeepsTheNewestSampleOnTheRight(t *testing.T) {
	s := samples(ms(1), ms(100))
	got := plain(Sparkline(s, wSpark))

	runes := []rune(got)
	if last := runes[len(runes)-1]; last != sparkRamp[len(sparkRamp)-1] {
		t.Errorf("rightmost glyph is %q, want the tallest bar %q", last, sparkRamp[len(sparkRamp)-1])
	}
}

func TestSparklineShowsLossAsAGapNotABar(t *testing.T) {
	s := []monitor.Sample{
		{RTT: ms(5), OK: true},
		{OK: false},
		{RTT: ms(5), OK: true},
	}
	got := plain(Sparkline(s, wSpark))

	if !strings.Contains(got, glyphLost) {
		t.Errorf("sparkline %q did not mark the lost probe", got)
	}
	if strings.Contains(got, string(sparkRamp[len(sparkRamp)-1])) {
		t.Errorf("sparkline %q drew a full-height bar for a lost probe", got)
	}
}

// A flat run has no range to scale against and must not read as "fastest".
func TestSparklineSitsMidHeightWhenEveryReplyMatches(t *testing.T) {
	s := samples(ms(5), ms(5), ms(5))
	got := plain(Sparkline(s, wSpark))

	if strings.Contains(got, string(sparkRamp[0])) {
		t.Errorf("a flat sparkline %q collapsed to the floor", got)
	}
}

func TestSparklineOldestSamplesFallOffTheLeft(t *testing.T) {
	var s []monitor.Sample
	for i := 0; i < wSpark+5; i++ {
		s = append(s, monitor.Sample{RTT: time.Duration(i) * time.Millisecond, OK: true})
	}
	// Only the newest window is drawn, so the slowest sample — the last one —
	// must be the tallest bar on screen.
	got := plain(Sparkline(s, wSpark))
	runes := []rune(got)
	if runes[len(runes)-1] != sparkRamp[len(sparkRamp)-1] {
		t.Errorf("sparkline %q did not keep the newest sample", got)
	}
}

// --- Unresolved hosts -----------------------------------------------------

func TestUnresolvedHostIsNotCalledDown(t *testing.T) {
	v := monitor.HostView{Display: "nope.invalid", State: monitor.StateUnresolved, Loss: 100}
	got := plain(Row(v, testCols(14), rowState(TrendNone, TrendNone, Flash{})))

	if !strings.Contains(got, glyphUnresolved+" DNS") {
		t.Errorf("row %q does not call out the DNS failure", got)
	}
	if strings.Contains(got, "DOWN") {
		t.Errorf("row %q blames the host for a name that never resolved", got)
	}
}

// --- Down duration --------------------------------------------------------

func TestFormatDownFor(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{3 * time.Second, "3s"},
		{59 * time.Second, "59s"},
		{90 * time.Second, "1m30s"},
		{59*time.Minute + 59*time.Second, "59m59s"},
		{time.Hour + 4*time.Minute, "1h04m"},
		{26 * time.Hour, "1d02h"},
	}
	for _, tc := range cases {
		if got := FormatDownFor(tc.d); got != tc.want {
			t.Errorf("FormatDownFor(%v) = %q, want %q", tc.d, got, tc.want)
		}
	}
}

// The latency cell is dead space on a down host, so it carries the outage
// length instead — but only once there is one to report.
func TestDownHostShowsHowLongItHasBeenGone(t *testing.T) {
	v := upView("h")
	v.State = monitor.StateDown
	v.HasLatency = false

	cols := testCols(10)
	with := plain(Row(v, cols, RowState{DownFor: 90 * time.Second, HasDownFor: true}))
	if !strings.Contains(with, "1m30s") {
		t.Errorf("row %q does not show the outage length", with)
	}

	without := plain(Row(v, cols, RowState{}))
	if !strings.Contains(without, glyphNone) {
		t.Errorf("row %q should dash out an unknown outage length", without)
	}
}

// An outage readout must not make the row a different width from every other.
func TestDownDurationDoesNotChangeTheRowWidth(t *testing.T) {
	cols := testCols(10)
	want := lipgloss.Width(Row(upView("h"), cols, RowState{}))

	v := upView("h")
	v.State = monitor.StateDown
	v.HasLatency = false
	for _, d := range []time.Duration{time.Second, 90 * time.Second, 26 * time.Hour, 400 * 24 * time.Hour} {
		got := lipgloss.Width(Row(v, cols, RowState{DownFor: d, HasDownFor: true}))
		if got != want {
			t.Errorf("row with a %v outage is %d wide, want %d", d, got, want)
		}
	}
}
