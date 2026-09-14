package monitor

import (
	"math"
	"time"
)

// State is a host's condition as of its most recent probe.
type State int

const (
	// StateWaiting means no probe has completed yet.
	StateWaiting State = iota
	// StateUp means the most recent probe was answered.
	StateUp
	// StateDown means the most recent probe went unanswered.
	StateDown
	// StateUnresolved means the name never resolved, so nothing was ever
	// sent. Kept distinct from StateDown because the fix is different: a
	// typo or a dead DNS entry, not an unreachable host.
	StateUnresolved
)

func (s State) String() string {
	switch s {
	case StateUp:
		return "up"
	case StateDown:
		return "down"
	case StateUnresolved:
		return "unresolved"
	default:
		return "waiting"
	}
}

// Reachable reports whether this state means the host answered. Unresolved
// and down are both "not answering", which is what a down filter wants.
func (s State) Reachable() bool { return s == StateUp }

// Result is the outcome of a single probe.
type Result struct {
	Index int           // Which target this belongs to.
	Probe int           // Per-target probe number, increasing.
	RTT   time.Duration // Round-trip time; meaningless unless OK.
	OK    bool          // Whether a reply arrived before the timeout.
}

// RecentSamples is how many probe outcomes the sparkline remembers per host.
// Sixteen is enough to show a trend developing without widening the table
// past what a normal terminal can hold.
const RecentSamples = 16

// Sample is one remembered probe outcome, for the sparkline.
type Sample struct {
	RTT time.Duration
	OK  bool
}

// Stats accumulates probe outcomes for one host.
//
// State deliberately reflects only the most recent probe. A host that answered
// a hundred times and has just missed one is down right now, and saying
// otherwise — the bug that prompted this rewrite — hides exactly the event the
// tool exists to surface. Lifetime figures (Min, Max, Avg) are kept across a
// timeout, because they stay meaningful while a host is unreachable.
type Stats struct {
	State State
	Sent  int
	Recv  int

	Last time.Duration // RTT of the most recent reply.
	Min  time.Duration
	Max  time.Duration

	sum       time.Duration // Running total of replies, for the mean.
	sumSqMs   float64       // Running total of squared replies, for jitter.
	lastProbe int           // Newest probe number folded into State.
	seen      bool          // Whether any probe has been recorded.

	// recent is a ring of the last RecentSamples outcomes, in arrival order.
	// head is where the next one goes; written counts everything ever added,
	// so it doubles as a "has the ring wrapped" test.
	recent  [RecentSamples]Sample
	head    int
	written int
}

// Record folds one probe outcome into the running statistics.
//
// Counters and lifetime extremes take every result, but State and Last follow
// only the newest probe seen. Results can genuinely arrive out of order: a
// fast reply to probe N+1 can land before the sweeper times out probe N, and
// letting that stale timeout win would show a live host as down.
func (s *Stats) Record(r Result) {
	s.Sent++

	if r.OK {
		s.Recv++
		s.sum += r.RTT
		msec := float64(r.RTT) / float64(time.Millisecond)
		// Squared in milliseconds rather than nanoseconds: ns² overflows
		// int64 at about a 3s round trip, which a satellite link can reach.
		s.sumSqMs += msec * msec
		if s.Recv == 1 || r.RTT < s.Min {
			s.Min = r.RTT
		}
		if r.RTT > s.Max {
			s.Max = r.RTT
		}
	}

	// The ring records arrival order rather than probe order. Out-of-order
	// results are rare and the sparkline is impressionistic, so reordering
	// them would cost more than the smudge it would remove.
	s.recent[s.head] = Sample{RTT: r.RTT, OK: r.OK}
	s.head = (s.head + 1) % RecentSamples
	s.written++

	if s.seen && r.Probe < s.lastProbe {
		return // Stale outcome; it counted, but it does not define "now".
	}
	s.seen = true
	s.lastProbe = r.Probe

	if r.OK {
		s.State = StateUp
		s.Last = r.RTT
	} else {
		s.State = StateDown
	}
}

// Recent returns the remembered outcomes oldest first, as a fresh slice the
// caller may keep.
func (s *Stats) Recent() []Sample {
	n := s.written
	if n > RecentSamples {
		n = RecentSamples
	}
	out := make([]Sample, n)
	// The oldest entry sits just past head once the ring has wrapped, and at
	// zero before then.
	start := 0
	if s.written > RecentSamples {
		start = s.head
	}
	for i := 0; i < n; i++ {
		out[i] = s.recent[(start+i)%RecentSamples]
	}
	return out
}

// Avg is the mean RTT across replies only; lost probes do not drag it down.
func (s *Stats) Avg() time.Duration {
	if s.Recv == 0 {
		return 0
	}
	return s.sum / time.Duration(s.Recv)
}

// Jitter is the standard deviation of the replies — the same figure ping
// reports as mdev. It answers a question avg cannot: whether a link is
// steadily slow or wildly uneven, which is what makes a call stutter.
func (s *Stats) Jitter() time.Duration {
	if !s.HasJitter() {
		return 0
	}
	n := float64(s.Recv)
	mean := float64(s.sum) / float64(time.Millisecond) / n
	variance := s.sumSqMs/n - mean*mean
	if variance < 0 {
		variance = 0 // Rounding noise when every reply took the same time.
	}
	return time.Duration(math.Sqrt(variance) * float64(time.Millisecond))
}

// Loss is the percentage of probes that went unanswered.
func (s *Stats) Loss() float64 {
	if s.Sent == 0 {
		return 0
	}
	return float64(s.Sent-s.Recv) / float64(s.Sent) * 100
}

// HasLatency reports whether a current latency reading exists, which is true
// only while the host is up.
func (s *Stats) HasLatency() bool { return s.State == StateUp && s.Recv > 0 }

// HasHistory reports whether the host has ever replied, and therefore whether
// Min, Max and Avg hold anything worth showing.
func (s *Stats) HasHistory() bool { return s.Recv > 0 }

// HasJitter reports whether there are enough replies for a spread to mean
// anything. One reply has no spread, so it shows nothing rather than zero.
func (s *Stats) HasJitter() bool { return s.Recv >= 2 }
