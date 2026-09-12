package monitor

import "time"

// State is a host's condition as of its most recent probe.
type State int

const (
	// StateWaiting means no probe has completed yet.
	StateWaiting State = iota
	// StateUp means the most recent probe was answered.
	StateUp
	// StateDown means the most recent probe went unanswered.
	StateDown
)

func (s State) String() string {
	switch s {
	case StateUp:
		return "up"
	case StateDown:
		return "down"
	default:
		return "waiting"
	}
}

// Result is the outcome of a single probe.
type Result struct {
	Index int           // Which target this belongs to.
	Probe int           // Per-target probe number, increasing.
	RTT   time.Duration // Round-trip time; meaningless unless OK.
	OK    bool          // Whether a reply arrived before the timeout.
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
	lastProbe int           // Newest probe number folded into State.
	seen      bool          // Whether any probe has been recorded.
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
		if s.Recv == 1 || r.RTT < s.Min {
			s.Min = r.RTT
		}
		if r.RTT > s.Max {
			s.Max = r.RTT
		}
	}

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

// Avg is the mean RTT across replies only; lost probes do not drag it down.
func (s *Stats) Avg() time.Duration {
	if s.Recv == 0 {
		return 0
	}
	return s.sum / time.Duration(s.Recv)
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
