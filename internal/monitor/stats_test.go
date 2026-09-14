package monitor

import (
	"math"
	"testing"
	"time"
)

func ms(n int) time.Duration { return time.Duration(n) * time.Millisecond }

func TestNewStatsStartsWaiting(t *testing.T) {
	var s Stats
	if s.State != StateWaiting {
		t.Errorf("State = %v, want StateWaiting", s.State)
	}
	if s.Sent != 0 || s.Recv != 0 {
		t.Errorf("counters = %d/%d, want 0/0", s.Sent, s.Recv)
	}
}

func TestRecordReplyMarksHostUp(t *testing.T) {
	var s Stats
	s.Record(Result{OK: true, RTT: ms(12)})

	if s.State != StateUp {
		t.Errorf("State = %v, want StateUp", s.State)
	}
	if s.Last != ms(12) || s.Min != ms(12) || s.Max != ms(12) {
		t.Errorf("last/min/max = %v/%v/%v, want all 12ms", s.Last, s.Min, s.Max)
	}
	if s.Sent != 1 || s.Recv != 1 {
		t.Errorf("counters = %d/%d, want 1/1", s.Sent, s.Recv)
	}
}

func TestRecordTimeoutMarksHostDown(t *testing.T) {
	var s Stats
	s.Record(Result{OK: false})

	if s.State != StateDown {
		t.Errorf("State = %v, want StateDown", s.State)
	}
	if s.Sent != 1 || s.Recv != 0 {
		t.Errorf("counters = %d/%d, want 1/0", s.Sent, s.Recv)
	}
}

// The defining behaviour of the tool: a host that replied in the past but has
// just missed a probe is DOWN. Earlier successes must not keep it UP.
func TestHostThatStopsRespondingFlipsToDownImmediately(t *testing.T) {
	var s Stats
	s.Record(Result{OK: true, RTT: ms(10)})
	s.Record(Result{OK: true, RTT: ms(20)})
	if s.State != StateUp {
		t.Fatalf("precondition: State = %v, want StateUp", s.State)
	}

	s.Record(Result{OK: false})

	if s.State != StateDown {
		t.Errorf("State = %v, want StateDown after a single missed reply", s.State)
	}
	if s.HasLatency() {
		t.Error("HasLatency() = true while down; the current latency is unknown")
	}
}

// Lifetime statistics stay meaningful while a host is unreachable, so they
// must survive a timeout rather than being reset.
func TestTimeoutPreservesLifetimeStatistics(t *testing.T) {
	var s Stats
	s.Record(Result{OK: true, RTT: ms(10)})
	s.Record(Result{OK: true, RTT: ms(30)})
	s.Record(Result{OK: false})

	if s.Min != ms(10) {
		t.Errorf("Min = %v, want 10ms", s.Min)
	}
	if s.Max != ms(30) {
		t.Errorf("Max = %v, want 30ms", s.Max)
	}
	if s.Avg() != ms(20) {
		t.Errorf("Avg() = %v, want 20ms", s.Avg())
	}
}

func TestHostThatRecoversFlipsBackToUp(t *testing.T) {
	var s Stats
	s.Record(Result{OK: false})
	s.Record(Result{OK: true, RTT: ms(5)})

	if s.State != StateUp {
		t.Errorf("State = %v, want StateUp", s.State)
	}
	if !s.HasLatency() || s.Last != ms(5) {
		t.Errorf("Last = %v (has=%v), want 5ms", s.Last, s.HasLatency())
	}
}

func TestMinAndMaxTrackExtremes(t *testing.T) {
	var s Stats
	for _, v := range []int{20, 5, 35, 12} {
		s.Record(Result{OK: true, RTT: ms(v)})
	}
	if s.Min != ms(5) {
		t.Errorf("Min = %v, want 5ms", s.Min)
	}
	if s.Max != ms(35) {
		t.Errorf("Max = %v, want 35ms", s.Max)
	}
}

func TestAverageIgnoresLostProbes(t *testing.T) {
	var s Stats
	s.Record(Result{OK: true, RTT: ms(10)})
	s.Record(Result{OK: false})
	s.Record(Result{OK: true, RTT: ms(20)})

	if s.Avg() != ms(15) {
		t.Errorf("Avg() = %v, want 15ms (mean of replies only)", s.Avg())
	}
}

func TestAverageIsZeroBeforeAnyReply(t *testing.T) {
	var s Stats
	s.Record(Result{OK: false})
	if s.Avg() != 0 {
		t.Errorf("Avg() = %v, want 0", s.Avg())
	}
}

func TestLossPercentage(t *testing.T) {
	cases := []struct {
		name     string
		outcomes []bool
		want     float64
	}{
		{"no probes yet", nil, 0},
		{"all delivered", []bool{true, true, true, true}, 0},
		{"all lost", []bool{false, false}, 100},
		{"one in four lost", []bool{true, true, true, false}, 25},
		{"one in three lost", []bool{true, true, false}, 100.0 / 3.0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var s Stats
			for _, ok := range tc.outcomes {
				s.Record(Result{OK: ok, RTT: ms(1)})
			}
			if math.Abs(s.Loss()-tc.want) > 0.0001 {
				t.Errorf("Loss() = %v, want %v", s.Loss(), tc.want)
			}
		})
	}
}

func TestHasLatencyIsFalseUntilFirstReply(t *testing.T) {
	var s Stats
	if s.HasLatency() {
		t.Error("HasLatency() = true before any probe")
	}
}

func TestHasHistoryReportsWhetherAnyReplyWasSeen(t *testing.T) {
	var s Stats
	if s.HasHistory() {
		t.Error("HasHistory() = true before any reply")
	}
	s.Record(Result{OK: false})
	if s.HasHistory() {
		t.Error("HasHistory() = true after only a timeout")
	}
	s.Record(Result{OK: true, RTT: ms(3)})
	if !s.HasHistory() {
		t.Error("HasHistory() = false after a reply")
	}
}

// Probe outcomes can arrive out of order: a fast reply to a later probe can
// beat the sweeper that is still timing out an earlier one.
func TestStaleTimeoutDoesNotOverrideNewerReply(t *testing.T) {
	var s Stats
	s.Record(Result{Probe: 1, OK: true, RTT: ms(5)})
	s.Record(Result{Probe: 2, OK: false}) // probe 2 timed out, reported late
	s.Record(Result{Probe: 3, OK: true, RTT: ms(7)})
	s.Record(Result{Probe: 2, OK: false}) // duplicate straggler for probe 2

	if s.State != StateUp {
		t.Errorf("State = %v, want StateUp — probe 3 is newer than probe 2", s.State)
	}
	if s.Last != ms(7) {
		t.Errorf("Last = %v, want 7ms from the newest probe", s.Last)
	}
}

func TestStaleResultStillCountsTowardLoss(t *testing.T) {
	var s Stats
	s.Record(Result{Probe: 2, OK: true, RTT: ms(5)})
	s.Record(Result{Probe: 1, OK: false}) // older probe, reported late

	if s.Sent != 2 || s.Recv != 1 {
		t.Errorf("counters = %d/%d, want 2/1", s.Sent, s.Recv)
	}
	if s.Loss() != 50 {
		t.Errorf("Loss() = %v, want 50", s.Loss())
	}
	if s.State != StateUp {
		t.Errorf("State = %v, want StateUp from the newer probe", s.State)
	}
}

func TestNewerTimeoutStillWins(t *testing.T) {
	var s Stats
	s.Record(Result{Probe: 1, OK: true, RTT: ms(5)})
	s.Record(Result{Probe: 2, OK: false})

	if s.State != StateDown {
		t.Errorf("State = %v, want StateDown", s.State)
	}
}

// --- Jitter ---------------------------------------------------------------

func TestJitterNeedsTwoRepliesBeforeItMeansAnything(t *testing.T) {
	var s Stats
	if s.HasJitter() {
		t.Error("HasJitter() = true before any reply")
	}

	s.Record(Result{Probe: 1, OK: true, RTT: 10 * time.Millisecond})
	if s.HasJitter() {
		t.Error("HasJitter() = true after one reply, which has no spread")
	}
	if got := s.Jitter(); got != 0 {
		t.Errorf("Jitter() = %v after one reply, want 0", got)
	}

	s.Record(Result{Probe: 2, OK: true, RTT: 12 * time.Millisecond})
	if !s.HasJitter() {
		t.Error("HasJitter() = false after two replies")
	}
}

func TestJitterIsTheSpreadOfTheReplies(t *testing.T) {
	var s Stats
	for i, rtt := range []time.Duration{10, 20, 30} {
		s.Record(Result{Probe: i + 1, OK: true, RTT: rtt * time.Millisecond})
	}

	// Population standard deviation of 10/20/30 is sqrt(200/3) ≈ 8.165 ms,
	// the same figure ping reports as mdev.
	want := 8165 * time.Microsecond
	got := s.Jitter()
	if diff := got - want; diff > 50*time.Microsecond || diff < -50*time.Microsecond {
		t.Errorf("Jitter() = %v, want about %v", got, want)
	}
}

func TestJitterIsZeroForAPerfectlySteadyLink(t *testing.T) {
	var s Stats
	for i := 0; i < 5; i++ {
		s.Record(Result{Probe: i + 1, OK: true, RTT: 7 * time.Millisecond})
	}
	// Floating-point rounding must not turn a flat link into a tiny negative
	// variance and then a NaN.
	if got := s.Jitter(); got != 0 {
		t.Errorf("Jitter() = %v for identical replies, want 0", got)
	}
}

func TestJitterIgnoresLostProbes(t *testing.T) {
	var steady, lossy Stats
	for i := 0; i < 4; i++ {
		steady.Record(Result{Probe: i + 1, OK: true, RTT: 10 * time.Millisecond})
	}
	lossy.Record(Result{Probe: 1, OK: true, RTT: 10 * time.Millisecond})
	lossy.Record(Result{Probe: 2, OK: false})
	lossy.Record(Result{Probe: 3, OK: true, RTT: 10 * time.Millisecond})
	lossy.Record(Result{Probe: 4, OK: true, RTT: 10 * time.Millisecond})

	if lossy.Jitter() != steady.Jitter() {
		t.Errorf("Jitter() = %v with losses, want %v — a lost probe has no RTT to vary",
			lossy.Jitter(), steady.Jitter())
	}
}

// A slow link must not overflow the squared accumulator. In nanoseconds the
// square of a 3s round trip is already past what an int64 holds.
func TestJitterSurvivesVerySlowLinks(t *testing.T) {
	var s Stats
	s.Record(Result{Probe: 1, OK: true, RTT: 4 * time.Second})
	s.Record(Result{Probe: 2, OK: true, RTT: 6 * time.Second})

	got := s.Jitter()
	if got <= 0 || got > 2*time.Second {
		t.Errorf("Jitter() = %v for 4s/6s replies, want about 1s", got)
	}
}

// --- Recent samples -------------------------------------------------------

func TestRecentStartsEmpty(t *testing.T) {
	var s Stats
	if got := s.Recent(); len(got) != 0 {
		t.Errorf("Recent() = %v before any probe, want empty", got)
	}
}

func TestRecentKeepsProbesInOrderOldestFirst(t *testing.T) {
	var s Stats
	for i := 1; i <= 3; i++ {
		s.Record(Result{Probe: i, OK: true, RTT: time.Duration(i) * time.Millisecond})
	}

	got := s.Recent()
	if len(got) != 3 {
		t.Fatalf("Recent() has %d samples, want 3", len(got))
	}
	for i, sample := range got {
		if want := time.Duration(i+1) * time.Millisecond; sample.RTT != want {
			t.Errorf("sample %d RTT = %v, want %v", i, sample.RTT, want)
		}
	}
}

func TestRecentRemembersLosses(t *testing.T) {
	var s Stats
	s.Record(Result{Probe: 1, OK: true, RTT: time.Millisecond})
	s.Record(Result{Probe: 2, OK: false})

	got := s.Recent()
	if len(got) != 2 {
		t.Fatalf("Recent() has %d samples, want 2", len(got))
	}
	if got[1].OK {
		t.Error("a lost probe was remembered as a reply")
	}
}

// The ring must drop the oldest rather than growing without bound.
func TestRecentIsCappedAndKeepsTheNewest(t *testing.T) {
	var s Stats
	total := RecentSamples + 7
	for i := 1; i <= total; i++ {
		s.Record(Result{Probe: i, OK: true, RTT: time.Duration(i) * time.Millisecond})
	}

	got := s.Recent()
	if len(got) != RecentSamples {
		t.Fatalf("Recent() has %d samples, want %d", len(got), RecentSamples)
	}
	if want := time.Duration(total) * time.Millisecond; got[len(got)-1].RTT != want {
		t.Errorf("newest sample = %v, want %v", got[len(got)-1].RTT, want)
	}
	if want := time.Duration(total-RecentSamples+1) * time.Millisecond; got[0].RTT != want {
		t.Errorf("oldest sample = %v, want %v", got[0].RTT, want)
	}
}

func TestRecentReturnsACopy(t *testing.T) {
	var s Stats
	s.Record(Result{Probe: 1, OK: true, RTT: time.Millisecond})

	first := s.Recent()
	first[0].RTT = time.Hour

	if second := s.Recent(); second[0].RTT == time.Hour {
		t.Error("mutating the returned slice changed the stored samples")
	}
}

func TestUnresolvedStateHasAName(t *testing.T) {
	if got := StateUnresolved.String(); got != "unresolved" {
		t.Errorf("StateUnresolved.String() = %q, want %q", got, "unresolved")
	}
	if StateUnresolved.Reachable() {
		t.Error("an unresolved host reported itself as reachable")
	}
	if !StateUp.Reachable() {
		t.Error("an up host reported itself as unreachable")
	}
}
