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
	frames [][]monitor.HostView
	at     int
}

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
