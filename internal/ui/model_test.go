package ui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/maxajbarlow/pingm/internal/monitor"
)

func ms(n int) time.Duration { return time.Duration(n) * time.Millisecond }

// fakeSource replays scripted snapshots so the model can be driven without a
// socket or a terminal.
type fakeSource struct {
	frames     [][]monitor.HostView
	at         int
	intervals  []time.Duration // Every SetInterval the model asked for.
	privileged bool
}

func (f *fakeSource) SetInterval(d time.Duration) { f.intervals = append(f.intervals, d) }

func (f *fakeSource) Privileged() bool { return f.privileged }

// Snapshot returns the next scripted frame, holding on the last one.
func (f *fakeSource) Snapshot() []monitor.HostView {
	frame := f.frames[f.at]
	if f.at < len(f.frames)-1 {
		f.at++
	}
	return frame
}

func up(host string, rtt, avg time.Duration, loss float64) monitor.HostView {
	return monitor.HostView{
		Display: host, State: monitor.StateUp, Last: rtt,
		Min: rtt, Max: rtt, Avg: avg, Loss: loss,
		HasLatency: true, HasHistory: true,
	}
}

func down(host string, loss float64) monitor.HostView {
	return monitor.HostView{Display: host, State: monitor.StateDown, Loss: loss}
}

func unresolved(host string) monitor.HostView {
	return monitor.HostView{Display: host, State: monitor.StateUnresolved, Loss: 100}
}

func waiting(host string) monitor.HostView {
	return monitor.HostView{Display: host, State: monitor.StateWaiting}
}

func newTestModel(frames ...[]monitor.HostView) Model {
	src := &fakeSource{frames: frames}
	m := NewModel(src, FilterAll, time.Second, 0, nil)
	m.width, m.height = 100, 30
	return m
}

func advance(m Model) Model {
	next, _ := m.Update(tickMsg(time.Now()))
	return next.(Model)
}

func TestParseFilter(t *testing.T) {
	cases := map[string]Filter{
		"up": FilterUp, "online": FilterUp, "UP": FilterUp, "  up  ": FilterUp,
		"down": FilterDown, "offline": FilterDown, "Down": FilterDown,
		"all": FilterAll, "any": FilterAll, "": FilterAll,
	}
	for in, want := range cases {
		got, err := ParseFilter(in)
		if err != nil {
			t.Errorf("ParseFilter(%q) errored: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("ParseFilter(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestParseFilterRejectsUnknownValue(t *testing.T) {
	if _, err := ParseFilter("sideways"); err == nil {
		t.Error("ParseFilter(\"sideways\") succeeded, want an error")
	}
}

func TestViewListsEveryHostByDefault(t *testing.T) {
	m := newTestModel([]monitor.HostView{up("10.0.0.1", ms(5), ms(5), 0), down("10.0.0.2", 100)})
	out := plain(m.View())

	for _, want := range []string{"10.0.0.1", "10.0.0.2", "UP", "DOWN"} {
		if !strings.Contains(out, want) {
			t.Errorf("view missing %q:\n%s", want, out)
		}
	}
}

func TestFilterUpHidesDownHosts(t *testing.T) {
	m := newTestModel([]monitor.HostView{up("10.0.0.1", ms(5), ms(5), 0), down("10.0.0.2", 100)})
	m.filter = FilterUp
	out := plain(m.View())

	if !strings.Contains(out, "10.0.0.1") {
		t.Error("view dropped the up host")
	}
	if strings.Contains(out, "10.0.0.2") {
		t.Error("view showed a down host under -f up")
	}
	if !strings.Contains(out, "Filter: up (1 of 2)") {
		t.Errorf("footer missing the filter summary:\n%s", out)
	}
}

func TestFilterDownHidesUpHosts(t *testing.T) {
	m := newTestModel([]monitor.HostView{up("10.0.0.1", ms(5), ms(5), 0), down("10.0.0.2", 100)})
	m.filter = FilterDown
	out := plain(m.View())

	if strings.Contains(out, "10.0.0.1") {
		t.Error("view showed an up host under -f down")
	}
	if !strings.Contains(out, "10.0.0.2") {
		t.Error("view dropped the down host")
	}
}

func TestKeysSwitchTheFilterLive(t *testing.T) {
	m := newTestModel([]monitor.HostView{up("10.0.0.1", ms(5), ms(5), 0), down("10.0.0.2", 100)})

	for _, tc := range []struct {
		key  string
		want Filter
	}{{"d", FilterDown}, {"u", FilterUp}, {"a", FilterAll}} {
		next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(tc.key)})
		m = next.(Model)
		if m.filter != tc.want {
			t.Errorf("after %q filter = %v, want %v", tc.key, m.filter, tc.want)
		}
	}
}

func TestQuitKeyStopsTheProgram(t *testing.T) {
	m := newTestModel([]monitor.HostView{up("h", ms(1), ms(1), 0)})
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})

	if cmd == nil {
		t.Fatal("pressing q returned no command, want tea.Quit")
	}
	if !next.(Model).quitting {
		t.Error("model did not mark itself as quitting")
	}
	if got := next.(Model).View(); got != "" {
		t.Errorf("a quitting model rendered %q, want empty", got)
	}
}

// An empty table must say why, in the terms of the active filter.
func TestEmptyNoticeExplainsTheFilter(t *testing.T) {
	cases := []struct {
		name   string
		filter Filter
		views  []monitor.HostView
		want   string
	}{
		{"nothing probed yet", FilterDown, []monitor.HostView{waiting("a"), waiting("b")}, "Waiting for the first replies"},
		{"one host still pending", FilterDown, []monitor.HostView{up("a", ms(1), ms(1), 0), waiting("b")}, "Waiting for the first replies"},
		{"all responding", FilterDown, []monitor.HostView{up("a", ms(1), ms(1), 0), up("b", ms(1), ms(1), 0)}, "All 2 hosts are responding."},
		{"single host responding", FilterDown, []monitor.HostView{up("a", ms(1), ms(1), 0)}, "The host is responding."},
		{"none responding", FilterUp, []monitor.HostView{down("a", 100), down("b", 100)}, "No hosts are responding."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newTestModel(tc.views)
			m.filter = tc.filter
			if out := plain(m.View()); !strings.Contains(out, tc.want) {
				t.Errorf("view missing %q:\n%s", tc.want, out)
			}
		})
	}
}

func TestViewClipsToTerminalHeightAndSaysHowMany(t *testing.T) {
	var views []monitor.HostView
	for i := 0; i < 40; i++ {
		views = append(views, down("10.0.0."+string(rune('a'+i%26)), 100))
	}
	m := newTestModel(views)
	m.height = 14 // 14 - 4 header - 3 footer = 7 budget, one of which is the notice

	out := plain(m.View())
	if !strings.Contains(out, "34 below") {
		t.Errorf("clipped view did not report what is off screen:\n%s", out)
	}
	if lines := strings.Count(out, "\n"); lines > m.height {
		t.Errorf("view used %d lines, want at most %d", lines, m.height)
	}
}

func TestWindowResizeIsHonoured(t *testing.T) {
	m := newTestModel([]monitor.HostView{up("h", ms(1), ms(1), 0)})
	next, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 50})

	if got := next.(Model); got.width != 120 || got.height != 50 {
		t.Errorf("size = %dx%d, want 120x50", got.width, got.height)
	}
}

// Trend arrows compare the newest reading with the previous refresh.
func TestTrendArrowsFollowSuccessiveRefreshes(t *testing.T) {
	frame1 := []monitor.HostView{up("h", ms(10), ms(10), 0)}
	frame2 := []monitor.HostView{up("h", ms(20), ms(20), 0)} // avg rose: worse
	frame3 := []monitor.HostView{up("h", ms(5), ms(5), 0)}   // avg fell: better

	m := newTestModel(frame1, frame2, frame3)

	m = advance(m) // now showing frame2, baseline frame1
	if got := m.avgTrend(0); got != TrendWorse {
		t.Errorf("avg trend = %v, want TrendWorse", got)
	}
	m = advance(m) // now showing frame3, baseline frame2
	if got := m.avgTrend(0); got != TrendBetter {
		t.Errorf("avg trend = %v, want TrendBetter", got)
	}
}

func TestNoTrendArrowOnTheFirstReading(t *testing.T) {
	m := newTestModel([]monitor.HostView{waiting("h")}, []monitor.HostView{up("h", ms(9), ms(9), 0)})
	m = advance(m)

	if got := m.avgTrend(0); got != TrendNone {
		t.Errorf("avg trend = %v, want TrendNone for a first reading", got)
	}
}

func TestLossTrendTracksPacketLoss(t *testing.T) {
	m := newTestModel(
		[]monitor.HostView{up("h", ms(5), ms(5), 0)},
		[]monitor.HostView{up("h", ms(5), ms(5), 25)},
	)
	m = advance(m)
	if got := m.lossTrend(0); got != TrendWorse {
		t.Errorf("loss trend = %v, want TrendWorse", got)
	}
}

func TestStatusLineShowsIntervalCountAndDone(t *testing.T) {
	src := &fakeSource{frames: [][]monitor.HostView{{up("h", ms(1), ms(1), 0)}}}
	m := NewModel(src, FilterAll, 250*time.Millisecond, 5, nil)
	m.width, m.height = 100, 30

	out := plain(m.View())
	if !strings.Contains(out, "Interval: 250ms") {
		t.Errorf("status line missing the interval:\n%s", out)
	}
	if !strings.Contains(out, "Count: 5") {
		t.Errorf("status line missing the count:\n%s", out)
	}

	next, _ := m.Update(finishedMsg{})
	if out := plain(next.(Model).View()); !strings.Contains(out, "Done") {
		t.Errorf("finished model does not report Done:\n%s", out)
	}
}

func TestRefreshRateIsClampedToASensibleRange(t *testing.T) {
	src := &fakeSource{frames: [][]monitor.HostView{{}}}

	if got := NewModel(src, FilterAll, time.Millisecond, 0, nil).refresh; got != 100*time.Millisecond {
		t.Errorf("refresh = %v, want it raised to 100ms", got)
	}
	if got := NewModel(src, FilterAll, time.Minute, 0, nil).refresh; got != 2*time.Second {
		t.Errorf("refresh = %v, want it capped at 2s", got)
	}
	if got := NewModel(src, FilterAll, 500*time.Millisecond, 0, nil).refresh; got != 500*time.Millisecond {
		t.Errorf("refresh = %v, want it left alone", got)
	}
}

func TestFilterString(t *testing.T) {
	for f, want := range map[Filter]string{FilterAll: "all", FilterUp: "up", FilterDown: "down"} {
		if got := f.String(); got != want {
			t.Errorf("Filter(%d).String() = %q, want %q", f, got, want)
		}
	}
}

// --- Alive highlight ------------------------------------------------------

// clock is a controllable time source so the fade can be tested without sleeps.
type clock struct{ t time.Time }

func (c *clock) now() time.Time          { return c.t }
func (c *clock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newClockedModel(frames ...[]monitor.HostView) (Model, *clock) {
	c := &clock{t: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)}
	m := NewModel(&fakeSource{frames: frames}, FilterAll, time.Second, 0, nil)
	m.now = c.now
	m.width, m.height = 100, 30
	return m, c
}

func TestHostComingUpFromDownStartsAtFullFlash(t *testing.T) {
	m, _ := newClockedModel(
		[]monitor.HostView{down("10.0.0.1", 100)},
		[]monitor.HostView{up("10.0.0.1", ms(5), ms(5), 50)},
	)
	if got := m.flash(0).Level; got != 0 {
		t.Fatalf("flash = %v before the host came up, want FlashNone", got)
	}

	m = advance(m)
	if got := m.flash(0).Level; got != FlashMax {
		t.Errorf("flash = %v the instant the host came up, want FlashMax", got)
	}
}

func TestFirstReplyAlsoFlashes(t *testing.T) {
	m, _ := newClockedModel(
		[]monitor.HostView{waiting("10.0.0.1")},
		[]monitor.HostView{up("10.0.0.1", ms(5), ms(5), 0)},
	)
	m = advance(m)
	if got := m.flash(0).Level; got != FlashMax {
		t.Errorf("flash = %v on a host's first reply, want FlashMax", got)
	}
}

func TestFlashFadesToNothingOverTheDuration(t *testing.T) {
	m, c := newClockedModel(
		[]monitor.HostView{down("h", 100)},
		[]monitor.HostView{up("h", ms(5), ms(5), 50)},
	)
	m = advance(m)

	prev := m.flash(0).Level
	if prev != FlashMax {
		t.Fatalf("flash = %v at the start, want FlashMax", prev)
	}
	// Step through the animation; intensity must never increase.
	for elapsed := frameInterval; elapsed < flashDuration; elapsed += frameInterval {
		c.advance(frameInterval)
		got := m.flash(0).Level
		if got > prev {
			t.Errorf("flash rose from %v to %v at %v into the fade", prev, got, elapsed)
		}
		if got < 0 {
			t.Errorf("flash went negative (%v) at %v", got, elapsed)
		}
		prev = got
	}
	c.advance(frameInterval)
	if got := m.flash(0).Level; got != 0 {
		t.Errorf("flash = %v after the full duration, want FlashNone", got)
	}
}

func TestFlashEndsExactlyAtTheDuration(t *testing.T) {
	m, c := newClockedModel(
		[]monitor.HostView{down("h", 100)},
		[]monitor.HostView{up("h", ms(5), ms(5), 50)},
	)
	m = advance(m)

	c.advance(flashDuration - time.Millisecond)
	if m.flash(0).Level == 0 {
		t.Error("flash ended before the duration elapsed")
	}
	c.advance(time.Millisecond)
	if got := m.flash(0).Level; got != 0 {
		t.Errorf("flash = %v at exactly the duration, want FlashNone", got)
	}
}

// A host that stays up must not keep re-flashing on every refresh.
func TestStayingUpDoesNotReFlash(t *testing.T) {
	m, c := newClockedModel(
		[]monitor.HostView{down("h", 100)},
		[]monitor.HostView{up("h", ms(5), ms(5), 50)},
		[]monitor.HostView{up("h", ms(6), ms(6), 40)},
	)
	m = advance(m) // came up: flashes
	c.advance(flashDuration)
	m = advance(m) // still up

	if got := m.flash(0).Level; got != 0 {
		t.Errorf("flash = %v for a host that never went away, want FlashNone", got)
	}
}

func TestGoingDownFlashesInTheOtherDirection(t *testing.T) {
	m, _ := newClockedModel(
		[]monitor.HostView{up("h", ms(5), ms(5), 0)},
		[]monitor.HostView{down("h", 50)},
	)
	m = advance(m)

	got := m.flash(0)
	if got.Level != FlashMax {
		t.Errorf("flash level = %d for a host going down, want %d", got.Level, FlashMax)
	}
	if !got.Down {
		t.Error("a host going down flashed as though it had come alive")
	}
}

// A host that was never up must not flash red the moment it is first probed,
// or scanning a subnet would light up every dead address at once.
func TestFirstProbeFailureDoesNotFlash(t *testing.T) {
	m, _ := newClockedModel(
		[]monitor.HostView{waiting("h")},
		[]monitor.HostView{down("h", 100)},
	)
	m = advance(m)
	if got := m.flash(0); got.Active() {
		t.Errorf("flash = %+v on a host that was never up, want none", got)
	}
}

func TestRecoveringTwiceFlashesAgain(t *testing.T) {
	m, c := newClockedModel(
		[]monitor.HostView{down("h", 100)},
		[]monitor.HostView{up("h", ms(5), ms(5), 50)},
		[]monitor.HostView{down("h", 60)},
		[]monitor.HostView{up("h", ms(5), ms(5), 40)},
	)
	m = advance(m)
	c.advance(flashDuration) // first flash expires
	m = advance(m)           // goes down
	m = advance(m)           // comes back

	if got := m.flash(0).Level; got != FlashMax {
		t.Errorf("flash = %v on a second recovery, want FlashMax", got)
	}
}

func TestFlashOnlyAffectsTheHostThatChanged(t *testing.T) {
	m, _ := newClockedModel(
		[]monitor.HostView{up("a", ms(5), ms(5), 0), down("b", 100)},
		[]monitor.HostView{up("a", ms(5), ms(5), 0), up("b", ms(9), ms(9), 50)},
	)
	m = advance(m)

	if got := m.flash(0).Level; got != 0 {
		t.Errorf("host a flash = %v, want FlashNone — it never changed", got)
	}
	if got := m.flash(1).Level; got != FlashMax {
		t.Errorf("host b flash = %v, want FlashMax", got)
	}
}

// The frame ticker must start when a highlight begins and stop when it ends,
// so an idle table is not repainted 11 times a second for nothing.
func TestAnimationTickerRunsOnlyWhileFlashing(t *testing.T) {
	m, c := newClockedModel(
		[]monitor.HostView{down("h", 100)},
		[]monitor.HostView{up("h", ms(5), ms(5), 50)},
	)
	if m.anyFlashing() {
		t.Fatal("anyFlashing() is true before anything happened")
	}

	next, cmd := m.Update(tickMsg(time.Now()))
	m = next.(Model)
	if !m.animating {
		t.Error("the frame ticker did not start when a host came alive")
	}
	if cmd == nil {
		t.Error("no command returned to drive the animation")
	}

	c.advance(flashDuration)
	next, cmd = m.Update(frameMsg(time.Now()))
	m = next.(Model)
	if m.animating {
		t.Error("the frame ticker kept running after the highlight faded")
	}
	if cmd != nil {
		t.Error("a further frame was scheduled after the highlight faded")
	}
}

func TestFlashIndexOutOfRangeIsSafe(t *testing.T) {
	m, _ := newClockedModel([]monitor.HostView{up("h", ms(1), ms(1), 0)})
	if got := m.flash(99).Level; got != 0 {
		t.Errorf("flash(99) = %v, want FlashNone", got)
	}
}

// --- Interval shortcuts ---------------------------------------------------

func press(m Model, key string) Model {
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)})
	return next.(Model)
}

func TestLadderStepsThroughRoundNumbers(t *testing.T) {
	want := []time.Duration{
		100 * time.Millisecond, 200 * time.Millisecond, 500 * time.Millisecond,
		time.Second, 2 * time.Second, 5 * time.Second, 10 * time.Second,
	}
	got := []time.Duration{intervalLadder[0]}
	for d := intervalLadder[0]; slower(d) != d; {
		d = slower(d)
		got = append(got, d)
	}
	if len(got) != len(want) {
		t.Fatalf("ladder climbed through %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("rung %d = %v, want %v", i, got[i], want[i])
		}
	}
}

func TestLadderIsSymmetric(t *testing.T) {
	for _, d := range intervalLadder {
		if up := slower(d); up != d {
			if back := faster(up); back != d {
				t.Errorf("slower(%v)=%v then faster gave %v, want %v", d, up, back, d)
			}
		}
	}
}

func TestLadderClampsAtBothEnds(t *testing.T) {
	fastest, slowest := intervalLadder[0], intervalLadder[len(intervalLadder)-1]
	if got := faster(fastest); got != fastest {
		t.Errorf("faster(%v) = %v, want it clamped", fastest, got)
	}
	if got := slower(slowest); got != slowest {
		t.Errorf("slower(%v) = %v, want it clamped", slowest, got)
	}
}

// An interval the user chose off-ladder must step to a neighbouring rung
// rather than snapping somewhere surprising.
func TestOffLadderIntervalStepsToTheNeighbouringRung(t *testing.T) {
	cases := []struct {
		start, up, down time.Duration
	}{
		{300 * time.Millisecond, 500 * time.Millisecond, 200 * time.Millisecond},
		{1500 * time.Millisecond, 2 * time.Second, time.Second},
		{7 * time.Second, 10 * time.Second, 5 * time.Second},
	}
	for _, tc := range cases {
		if got := slower(tc.start); got != tc.up {
			t.Errorf("slower(%v) = %v, want %v", tc.start, got, tc.up)
		}
		if got := faster(tc.start); got != tc.down {
			t.Errorf("faster(%v) = %v, want %v", tc.start, got, tc.down)
		}
	}
}

// A deliberately chosen interval below the ladder must not be sped up further.
func TestIntervalBelowTheLadderIsNotSpedUp(t *testing.T) {
	if got := faster(25 * time.Millisecond); got != 25*time.Millisecond {
		t.Errorf("faster(25ms) = %v, want it left alone", got)
	}
	if got := slower(25 * time.Millisecond); got != 100*time.Millisecond {
		t.Errorf("slower(25ms) = %v, want the first rung", got)
	}
}

func TestIntervalAboveTheLadderIsNotSlowedFurther(t *testing.T) {
	if got := slower(time.Minute); got != time.Minute {
		t.Errorf("slower(1m) = %v, want it left alone", got)
	}
	if got := faster(time.Minute); got != 10*time.Second {
		t.Errorf("faster(1m) = %v, want the top rung", got)
	}
}

func newIntervalModel(start time.Duration) (Model, *fakeSource) {
	src := &fakeSource{frames: [][]monitor.HostView{{up("h", ms(5), ms(5), 0)}}}
	m := NewModel(src, FilterAll, start, 0, nil)
	m.width, m.height = 100, 30
	return m, src
}

func TestPlusSlowsDownAndMinusSpeedsUp(t *testing.T) {
	m, src := newIntervalModel(time.Second)

	m = press(m, "+")
	if m.interval != 2*time.Second {
		t.Errorf("after + interval = %v, want 2s", m.interval)
	}
	m = press(m, "-")
	m = press(m, "-")
	if m.interval != 500*time.Millisecond {
		t.Errorf("after two - interval = %v, want 500ms", m.interval)
	}

	want := []time.Duration{2 * time.Second, time.Second, 500 * time.Millisecond}
	if len(src.intervals) != len(want) {
		t.Fatalf("monitor was told %v, want %v", src.intervals, want)
	}
	for i := range want {
		if src.intervals[i] != want[i] {
			t.Errorf("SetInterval call %d = %v, want %v", i, src.intervals[i], want[i])
		}
	}
}

// "=" is the unshifted key, so + works without reaching for shift.
func TestUnshiftedKeysWorkToo(t *testing.T) {
	m, _ := newIntervalModel(time.Second)
	if m = press(m, "="); m.interval != 2*time.Second {
		t.Errorf("after = interval = %v, want 2s", m.interval)
	}
	if m = press(m, "_"); m.interval != time.Second {
		t.Errorf("after _ interval = %v, want 1s", m.interval)
	}
}

// Pressing past either end must not keep poking the monitor.
func TestPressingPastTheEndDoesNothing(t *testing.T) {
	m, src := newIntervalModel(10 * time.Second)
	for i := 0; i < 3; i++ {
		m = press(m, "+")
	}
	if m.interval != 10*time.Second {
		t.Errorf("interval = %v, want it pinned at 10s", m.interval)
	}
	if len(src.intervals) != 0 {
		t.Errorf("monitor was retuned %v for a no-op change", src.intervals)
	}
}

func TestChangingIntervalRetunesTheRedrawRate(t *testing.T) {
	m, _ := newIntervalModel(time.Second)

	m = press(m, "+") // 2s
	if m.refresh != 2*time.Second {
		t.Errorf("refresh = %v at a 2s interval, want 2s", m.refresh)
	}
	m = press(m, "+") // 5s — redraw must stay capped
	if m.refresh != 2*time.Second {
		t.Errorf("refresh = %v at a 5s interval, want it capped at 2s", m.refresh)
	}
	for i := 0; i < 5; i++ {
		m = press(m, "-") // down to 100ms
	}
	if m.interval != 100*time.Millisecond {
		t.Fatalf("interval = %v, want 100ms", m.interval)
	}
	if m.refresh != 100*time.Millisecond {
		t.Errorf("refresh = %v at a 100ms interval, want 100ms", m.refresh)
	}
}

func TestFooterShowsTheCurrentInterval(t *testing.T) {
	m, _ := newIntervalModel(time.Second)
	if out := plain(m.View()); !strings.Contains(out, "Interval: 1s") {
		t.Errorf("footer does not show the starting interval:\n%s", out)
	}
	m = press(m, "+")
	if out := plain(m.View()); !strings.Contains(out, "Interval: 2s") {
		t.Errorf("footer did not follow the change:\n%s", out)
	}
}

func TestKeyHintsMentionTheRateKeys(t *testing.T) {
	m, _ := newIntervalModel(time.Second)
	if out := plain(m.View()); !strings.Contains(out, "+/-") {
		t.Errorf("key hints do not mention +/-:\n%s", out)
	}
}

// --- Scrolling ------------------------------------------------------------

// pressSpecial sends a non-rune key such as an arrow or Page Down.
func pressSpecial(m Model, t tea.KeyType) Model {
	next, _ := m.Update(tea.KeyMsg{Type: t})
	return next.(Model)
}

func wheel(m Model, button tea.MouseButton) Model {
	next, _ := m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: button})
	return next.(Model)
}

// manyDown builds a table far taller than any terminal.
func manyDown(n int) []monitor.HostView {
	views := make([]monitor.HostView, n)
	for i := range views {
		views[i] = down(fmt.Sprintf("10.0.0.%d", i), 100)
	}
	return views
}

// scrollable is a model with 40 hosts and room for 6 of them.
func scrollable() Model {
	m := newTestModel(manyDown(40))
	m.height = 14 // 14 - 4 header - 3 footer = 7, one of which is the indicator
	return m
}

func TestWheelScrollsTheTable(t *testing.T) {
	m := scrollable()
	if m.offset != 0 {
		t.Fatalf("offset = %d before scrolling, want 0", m.offset)
	}

	m = wheel(m, tea.MouseButtonWheelDown)
	if m.offset != wheelLines {
		t.Errorf("offset = %d after one notch down, want %d", m.offset, wheelLines)
	}

	m = wheel(m, tea.MouseButtonWheelUp)
	if m.offset != 0 {
		t.Errorf("offset = %d after scrolling back up, want 0", m.offset)
	}
}

func TestWheelStopsAtBothEnds(t *testing.T) {
	m := scrollable()

	for i := 0; i < 100; i++ {
		m = wheel(m, tea.MouseButtonWheelUp)
	}
	if m.offset != 0 {
		t.Errorf("offset = %d after scrolling up past the top, want 0", m.offset)
	}

	for i := 0; i < 100; i++ {
		m = wheel(m, tea.MouseButtonWheelDown)
	}
	if want := m.maxOffset(); m.offset != want {
		t.Errorf("offset = %d after scrolling down past the bottom, want %d", m.offset, want)
	}
	// The last row must actually be reachable, not stranded below the fold.
	if !strings.Contains(plain(m.View()), "10.0.0.39") {
		t.Error("scrolling to the bottom never reveals the final host")
	}
}

// A click or a drag must not move the table under the user.
func TestNonWheelMouseEventsAreIgnored(t *testing.T) {
	m := scrollable()
	m = wheel(m, tea.MouseButtonWheelDown)
	before := m.offset

	next, _ := m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	if got := next.(Model).offset; got != before {
		t.Errorf("a left click moved the table from %d to %d", before, got)
	}
	next, _ = m.Update(tea.MouseMsg{Action: tea.MouseActionMotion, Button: tea.MouseButtonWheelDown})
	if got := next.(Model).offset; got != before {
		t.Errorf("a motion event moved the table from %d to %d", before, got)
	}
}

func TestKeyboardScrollingWorksAsAFallback(t *testing.T) {
	m := scrollable()

	m = pressSpecial(m, tea.KeyDown)
	if m.offset != 1 {
		t.Errorf("offset = %d after one down arrow, want 1", m.offset)
	}
	m = pressSpecial(m, tea.KeyUp)
	if m.offset != 0 {
		t.Errorf("offset = %d after one up arrow, want 0", m.offset)
	}

	m = press(m, "j")
	if m.offset != 1 {
		t.Errorf("offset = %d after j, want 1", m.offset)
	}
	m = press(m, "k")
	if m.offset != 0 {
		t.Errorf("offset = %d after k, want 0", m.offset)
	}

	m = pressSpecial(m, tea.KeyPgDown)
	if m.offset != m.rowBudget() {
		t.Errorf("offset = %d after Page Down, want a page of %d", m.offset, m.rowBudget())
	}

	m = press(m, "G")
	if want := m.maxOffset(); m.offset != want {
		t.Errorf("offset = %d after G, want the bottom at %d", m.offset, want)
	}
	m = press(m, "g")
	if m.offset != 0 {
		t.Errorf("offset = %d after g, want the top", m.offset)
	}
}

// Shrinking the window must not leave the view scrolled past the new end.
func TestResizingClampsTheScrollPosition(t *testing.T) {
	m := scrollable()
	m = press(m, "G")

	next, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 60})
	m = next.(Model)
	if want := m.maxOffset(); m.offset > want {
		t.Errorf("offset = %d after growing the window, want at most %d", m.offset, want)
	}
}

// Narrowing the filter can leave fewer rows than the offset assumed.
func TestFilteringClampsTheScrollPosition(t *testing.T) {
	views := manyDown(40)
	views[0] = up("10.0.0.0", ms(5), ms(5), 0)

	m := newTestModel(views)
	m.height = 14
	m = press(m, "G") // Scrolled to the bottom of 39 down hosts.
	m = press(m, "u") // Now only one host matches.

	if m.offset != 0 {
		t.Errorf("offset = %d after filtering down to one row, want 0", m.offset)
	}
	if !strings.Contains(plain(m.View()), "10.0.0.0") {
		t.Error("the only matching host is not visible after filtering")
	}
}

// A table that fits needs no indicator and no wasted row.
func TestShortTableShowsEveryRowAndNoScrollNotice(t *testing.T) {
	m := newTestModel([]monitor.HostView{up("a", ms(1), ms(1), 0), down("b", 100)})
	out := plain(m.View())

	if strings.Contains(out, "scroll with the wheel") {
		t.Errorf("a table that fits advertised scrolling:\n%s", out)
	}
	for _, host := range []string{"a", "b"} {
		if !strings.Contains(out, host) {
			t.Errorf("host %q missing from a table that fits:\n%s", host, out)
		}
	}
}

func TestScrollNoticeNamesBothDirections(t *testing.T) {
	m := scrollable()
	m = wheel(m, tea.MouseButtonWheelDown)

	out := plain(m.View())
	if !strings.Contains(out, "above") || !strings.Contains(out, "below") {
		t.Errorf("mid-table view did not report both directions:\n%s", out)
	}
}

// --- Pause ----------------------------------------------------------------

func TestPauseFreezesTheDisplayButNotTheProbing(t *testing.T) {
	src := &fakeSource{frames: [][]monitor.HostView{
		{up("h", ms(5), ms(5), 0)},
		{up("h", ms(99), ms(99), 0)},
	}}
	m := NewModel(src, FilterAll, time.Second, 0, nil)
	m.width, m.height = 120, 30

	m = press(m, "p")
	if !m.paused {
		t.Fatal("p did not pause")
	}

	m = advance(m)
	if got := plain(m.View()); !strings.Contains(got, "5.00 ms") {
		t.Errorf("a paused table moved on from its last reading:\n%s", got)
	}
	if !strings.Contains(plain(m.View()), "Paused") {
		t.Error("a paused table does not say so")
	}

	m = press(m, "p")
	m = advance(m)
	if got := plain(m.View()); !strings.Contains(got, "99.0 ms") {
		t.Errorf("unpausing did not catch up to the current reading:\n%s", got)
	}
}

// --- Bell -----------------------------------------------------------------

// bellCounter returns a model whose bell increments a counter instead of
// making a noise, plus the counter.
func bellCounter(frames ...[]monitor.HostView) (Model, *int) {
	rings := 0
	m, _ := newClockedModel(frames...)
	m.bell = func() { rings++ }
	return m, &rings
}

func TestBellIsSilentUntilItIsTurnedOn(t *testing.T) {
	m, rings := bellCounter(
		[]monitor.HostView{up("h", ms(5), ms(5), 0)},
		[]monitor.HostView{down("h", 50)},
	)
	m = advance(m)
	if *rings != 0 {
		t.Errorf("the bell rang %d times while switched off", *rings)
	}
}

func TestBellRingsOnAStateChangeOnceEnabled(t *testing.T) {
	m, rings := bellCounter(
		[]monitor.HostView{up("h", ms(5), ms(5), 0)},
		[]monitor.HostView{down("h", 50)},
		[]monitor.HostView{up("h", ms(5), ms(5), 40)},
	)
	m = press(m, "b")
	if !m.belling {
		t.Fatal("b did not enable the bell")
	}

	m = advance(m) // Goes down.
	if *rings != 1 {
		t.Errorf("the bell rang %d times when the host went down, want 1", *rings)
	}
	m = advance(m) // Comes back.
	if *rings != 2 {
		t.Errorf("the bell rang %d times in total, want 2", *rings)
	}
}

// Starting the tool must not set off a fanfare as the first replies land.
func TestBellStaysSilentOnFirstReadings(t *testing.T) {
	m, rings := bellCounter(
		[]monitor.HostView{waiting("a"), waiting("b")},
		[]monitor.HostView{up("a", ms(5), ms(5), 0), down("b", 100)},
	)
	m = press(m, "b")
	m = advance(m)

	if *rings != 0 {
		t.Errorf("the bell rang %d times on the first readings, want 0", *rings)
	}
}

// One ring per refresh, however many hosts moved at once.
func TestBellRingsOnceForASimultaneousChange(t *testing.T) {
	m, rings := bellCounter(
		[]monitor.HostView{up("a", ms(5), ms(5), 0), up("b", ms(5), ms(5), 0)},
		[]monitor.HostView{down("a", 50), down("b", 50)},
	)
	m = press(m, "b")
	m = advance(m)

	if *rings != 1 {
		t.Errorf("the bell rang %d times for one refresh, want 1", *rings)
	}
}

func TestBellStateIsShownInTheFooter(t *testing.T) {
	m := newTestModel([]monitor.HostView{up("h", ms(5), ms(5), 0)})
	if strings.Contains(plain(m.View()), "Bell") {
		t.Error("the footer mentions the bell while it is off")
	}
	m = press(m, "b")
	if !strings.Contains(plain(m.View()), "Bell") {
		t.Error("the footer does not mention the bell once it is on")
	}
}

// --- Help -----------------------------------------------------------------

func TestHelpOpensAndAnyKeyCloses(t *testing.T) {
	m := newTestModel([]monitor.HostView{up("h", ms(5), ms(5), 0)})

	m = press(m, "?")
	if !m.showHelp {
		t.Fatal("? did not open the help")
	}
	out := plain(m.View())
	for _, want := range []string{"pingm — keys", "scroll the table", "pause the display"} {
		if !strings.Contains(out, want) {
			t.Errorf("help is missing %q:\n%s", want, out)
		}
	}

	m = press(m, "x")
	if m.showHelp {
		t.Error("a key press did not dismiss the help")
	}
}

// Keys must not act on a table the help is covering.
func TestKeysDoNotFallThroughTheHelp(t *testing.T) {
	m := newTestModel(manyDown(40))
	m.height = 14
	m = press(m, "?")

	m = press(m, "u")
	if m.filter != FilterAll {
		t.Errorf("filter changed to %v through the help overlay", m.filter)
	}
	if m.showHelp {
		t.Error("the key should have dismissed the help rather than acting")
	}
}

func TestCtrlCStillQuitsFromTheHelp(t *testing.T) {
	m := newTestModel([]monitor.HostView{up("h", ms(5), ms(5), 0)})
	m = press(m, "?")

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if !next.(Model).quitting {
		t.Error("Ctrl-C did not quit from the help")
	}
	if cmd == nil {
		t.Error("Ctrl-C returned no quit command")
	}
}

// --- Roll-up --------------------------------------------------------------

func TestFooterCountsEveryState(t *testing.T) {
	m := newTestModel([]monitor.HostView{
		up("a", ms(5), ms(5), 0),
		up("b", ms(15), ms(15), 0),
		down("c", 100),
		unresolved("nope.invalid"),
		waiting("d"),
	})
	m.width = 200

	out := plain(m.View())
	for _, want := range []string{"2 up", "1 down", "1 dns", "1 waiting"} {
		if !strings.Contains(out, want) {
			t.Errorf("footer missing %q:\n%s", want, out)
		}
	}
}

// The median, not the mean: one slow host must not drag the figure somewhere
// no host actually is.
func TestFooterMedianIgnoresAnOutlier(t *testing.T) {
	m := newTestModel([]monitor.HostView{
		up("a", ms(10), ms(10), 0),
		up("b", ms(12), ms(12), 0),
		up("c", ms(4000), ms(4000), 0),
	})
	m.width = 200

	if got := plain(m.View()); !strings.Contains(got, "median 12.0 ms") {
		t.Errorf("footer median is wrong:\n%s", got)
	}
}

// A single host is easy to read off the table itself.
func TestNoRollUpForASingleHost(t *testing.T) {
	m := newTestModel([]monitor.HostView{up("h", ms(5), ms(5), 0)})
	if got := plain(m.View()); strings.Contains(got, "1 up") {
		t.Errorf("a one-host table showed a roll-up:\n%s", got)
	}
}

func TestPrivilegedSocketIsDisclosed(t *testing.T) {
	plainSrc := &fakeSource{frames: [][]monitor.HostView{{up("h", ms(5), ms(5), 0)}}}
	m := NewModel(plainSrc, FilterAll, time.Second, 0, nil)
	m.width = 200
	if strings.Contains(plain(m.View()), "raw socket") {
		t.Error("an unprivileged run claimed a raw socket")
	}

	rawSrc := &fakeSource{frames: [][]monitor.HostView{{up("h", ms(5), ms(5), 0)}}, privileged: true}
	m = NewModel(rawSrc, FilterAll, time.Second, 0, nil)
	m.width = 200
	if !strings.Contains(plain(m.View()), "raw socket") {
		t.Error("a privileged run did not disclose its raw socket")
	}
}

// --- Unresolved hosts -----------------------------------------------------

func TestDownFilterIncludesUnresolvedHosts(t *testing.T) {
	m := newTestModel([]monitor.HostView{
		up("good", ms(5), ms(5), 0),
		unresolved("nope.invalid"),
	})
	m = press(m, "d")

	out := plain(m.View())
	if !strings.Contains(out, "nope.invalid") {
		t.Errorf("the down filter hid an unresolved host:\n%s", out)
	}
	if strings.Contains(out, "good") {
		t.Errorf("the down filter kept an up host:\n%s", out)
	}
}

func TestUpFilterExcludesUnresolvedHosts(t *testing.T) {
	m := newTestModel([]monitor.HostView{unresolved("nope.invalid")})
	m = press(m, "u")

	if got := plain(m.View()); strings.Contains(got, "nope.invalid") {
		t.Errorf("the up filter kept an unresolved host:\n%s", got)
	}
}

// --- Down duration --------------------------------------------------------

func TestDownDurationCountsFromTheMomentItFell(t *testing.T) {
	m, c := newClockedModel(
		[]monitor.HostView{up("h", ms(5), ms(5), 0)},
		[]monitor.HostView{down("h", 50)},
	)
	m = advance(m)

	c.advance(90 * time.Second)
	got, ok := m.downFor(0)
	if !ok {
		t.Fatal("no down duration for a host that fell over")
	}
	if got != 90*time.Second {
		t.Errorf("down for %v, want 90s", got)
	}
	if out := plain(m.View()); !strings.Contains(out, "1m30s") {
		t.Errorf("the table does not show how long the host has been down:\n%s", out)
	}
}

func TestDownDurationClearsOnRecovery(t *testing.T) {
	m, c := newClockedModel(
		[]monitor.HostView{up("h", ms(5), ms(5), 0)},
		[]monitor.HostView{down("h", 50)},
		[]monitor.HostView{up("h", ms(5), ms(5), 40)},
	)
	m = advance(m)
	c.advance(time.Minute)
	m = advance(m)

	if _, ok := m.downFor(0); ok {
		t.Error("a recovered host still reports a down duration")
	}
}

// A host that has never answered is still worth timing, even though it never
// fell over as such.
func TestDownDurationStartsAtTheFirstFailure(t *testing.T) {
	m, c := newClockedModel(
		[]monitor.HostView{waiting("h")},
		[]monitor.HostView{down("h", 100)},
	)
	m = advance(m)
	c.advance(30 * time.Second)

	got, ok := m.downFor(0)
	if !ok || got != 30*time.Second {
		t.Errorf("down for %v (ok=%v), want 30s", got, ok)
	}
}

// A help box that spills off the right edge helps nobody; the frame is what
// goes when there is no room for it.
func TestHelpFitsANarrowTerminal(t *testing.T) {
	for _, width := range []int{40, 55, 66, 100} {
		m := newTestModel([]monitor.HostView{up("h", ms(5), ms(5), 0)})
		m.width, m.height = width, 30
		m = press(m, "?")

		for _, line := range strings.Split(plain(m.View()), "\n") {
			if got := lipgloss.Width(line); got > width {
				t.Errorf("at width %d a help line is %d wide: %q", width, got, line)
			}
		}
		if !strings.Contains(plain(m.View()), "q") {
			t.Errorf("at width %d the help lost its key list", width)
		}
	}
}
