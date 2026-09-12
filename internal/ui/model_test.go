package ui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/maxajbarlow/pingm/internal/monitor"
)

func ms(n int) time.Duration { return time.Duration(n) * time.Millisecond }

// fakeSource replays scripted snapshots so the model can be driven without a
// socket or a terminal.
type fakeSource struct {
	frames    [][]monitor.HostView
	at        int
	intervals []time.Duration // Every SetInterval the model asked for.
}

func (f *fakeSource) SetInterval(d time.Duration) { f.intervals = append(f.intervals, d) }

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
	if !strings.Contains(out, "more hidden") {
		t.Errorf("clipped view did not report hidden rows:\n%s", out)
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
	if got := m.flash(0); got != FlashNone {
		t.Fatalf("flash = %v before the host came up, want FlashNone", got)
	}

	m = advance(m)
	if got := m.flash(0); got != FlashMax {
		t.Errorf("flash = %v the instant the host came up, want FlashMax", got)
	}
}

func TestFirstReplyAlsoFlashes(t *testing.T) {
	m, _ := newClockedModel(
		[]monitor.HostView{waiting("10.0.0.1")},
		[]monitor.HostView{up("10.0.0.1", ms(5), ms(5), 0)},
	)
	m = advance(m)
	if got := m.flash(0); got != FlashMax {
		t.Errorf("flash = %v on a host's first reply, want FlashMax", got)
	}
}

func TestFlashFadesToNothingOverTheDuration(t *testing.T) {
	m, c := newClockedModel(
		[]monitor.HostView{down("h", 100)},
		[]monitor.HostView{up("h", ms(5), ms(5), 50)},
	)
	m = advance(m)

	prev := m.flash(0)
	if prev != FlashMax {
		t.Fatalf("flash = %v at the start, want FlashMax", prev)
	}
	// Step through the animation; intensity must never increase.
	for elapsed := frameInterval; elapsed < flashDuration; elapsed += frameInterval {
		c.advance(frameInterval)
		got := m.flash(0)
		if got > prev {
			t.Errorf("flash rose from %v to %v at %v into the fade", prev, got, elapsed)
		}
		if got < FlashNone {
			t.Errorf("flash went negative (%v) at %v", got, elapsed)
		}
		prev = got
	}
	c.advance(frameInterval)
	if got := m.flash(0); got != FlashNone {
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
	if m.flash(0) == FlashNone {
		t.Error("flash ended before the duration elapsed")
	}
	c.advance(time.Millisecond)
	if got := m.flash(0); got != FlashNone {
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

	if got := m.flash(0); got != FlashNone {
		t.Errorf("flash = %v for a host that never went away, want FlashNone", got)
	}
}

func TestGoingDownDoesNotFlash(t *testing.T) {
	m, _ := newClockedModel(
		[]monitor.HostView{up("h", ms(5), ms(5), 0)},
		[]monitor.HostView{down("h", 50)},
	)
	m = advance(m)
	if got := m.flash(0); got != FlashNone {
		t.Errorf("flash = %v for a host going down, want FlashNone", got)
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

	if got := m.flash(0); got != FlashMax {
		t.Errorf("flash = %v on a second recovery, want FlashMax", got)
	}
}

func TestFlashOnlyAffectsTheHostThatChanged(t *testing.T) {
	m, _ := newClockedModel(
		[]monitor.HostView{up("a", ms(5), ms(5), 0), down("b", 100)},
		[]monitor.HostView{up("a", ms(5), ms(5), 0), up("b", ms(9), ms(9), 50)},
	)
	m = advance(m)

	if got := m.flash(0); got != FlashNone {
		t.Errorf("host a flash = %v, want FlashNone — it never changed", got)
	}
	if got := m.flash(1); got != FlashMax {
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
	if got := m.flash(99); got != FlashNone {
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
