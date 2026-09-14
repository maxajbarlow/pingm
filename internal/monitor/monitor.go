package monitor

import (
	"context"
	"net"
	"sync"
	"time"
)

// HostView is a consistent snapshot of one host, shaped for display. The UI
// never touches live statistics, so a redraw cannot tear across an update.
type HostView struct {
	Display    string
	State      State
	Last       time.Duration
	Min        time.Duration
	Max        time.Duration
	Avg        time.Duration
	Jitter     time.Duration
	Loss       float64
	HasLatency bool
	HasHistory bool
	HasJitter  bool
	Recent     []Sample
	ResolveErr error
}

// MaxAutoTimeout caps the timeout derived from the interval, so a long
// interval cannot leave a dead host looking merely slow for minutes.
const MaxAutoTimeout = 2 * time.Second

// Monitor probes a set of hosts and maintains their running statistics.
type Monitor struct {
	prober  *Prober
	targets []Target

	// autoTimeout means the reply deadline follows the interval. It is false
	// when the caller pinned a timeout explicitly, which must then survive an
	// interval change.
	autoTimeout bool

	// recorder, when set, is handed every probe outcome as it is folded in.
	// It is written once before Run and only read inside it, so it needs no
	// lock of its own.
	recorder Recorder

	mu    sync.RWMutex
	stats []Stats
}

// Resolver turns a hostname into an IPv4 address. It is a field so tests can
// avoid depending on DNS.
type Resolver func(host string) (net.IP, error)

// New resolves every host and opens the probe socket. A timeout of zero means
// derive it from the interval and keep it in step as the interval changes.
func New(hosts []string, interval, timeout time.Duration, resolve Resolver) (*Monitor, error) {
	auto := timeout <= 0
	if auto {
		timeout = autoTimeoutFor(interval)
	}
	if resolve == nil {
		resolve = resolveIPv4
	}

	targets := make([]Target, len(hosts))
	for i, h := range hosts {
		ip, err := resolve(h)
		// A name that will not resolve is kept in the table and reported as
		// down, which is more useful than dropping the row the user asked for.
		targets[i] = Target{Display: h, IP: ip, Err: err}
	}

	prober, err := NewProber(targets, interval, timeout)
	if err != nil {
		return nil, err
	}
	return &Monitor{
		prober:      prober,
		targets:     targets,
		autoTimeout: auto,
		stats:       make([]Stats, len(hosts)),
	}, nil
}

func autoTimeoutFor(interval time.Duration) time.Duration {
	if interval < MaxAutoTimeout {
		return interval
	}
	return MaxAutoTimeout
}

// Recorder receives every probe outcome, in the order the monitor folds them
// in, for callers that want a durable log alongside the live table.
// Implementations must not block for long: the probe loops push results
// synchronously, so a slow recorder slows probing.
type Recorder interface {
	Record(r Result, at time.Time, host string) error
}

// SetRecorder attaches a recorder. It must be called before Run.
func (m *Monitor) SetRecorder(r Recorder) { m.recorder = r }

// Hosts returns the display name of every target, in table order.
func (m *Monitor) Hosts() []string {
	names := make([]string, len(m.targets))
	for i, t := range m.targets {
		names[i] = t.Display
	}
	return names
}

// Interval is the current gap between probes for a given host.
func (m *Monitor) Interval() time.Duration { return m.prober.Interval() }

// SetInterval changes the probe cadence while running, carrying the reply
// deadline with it unless the caller pinned one.
func (m *Monitor) SetInterval(d time.Duration) {
	if d <= 0 {
		return
	}
	m.prober.SetInterval(d)
	if m.autoTimeout {
		m.prober.SetTimeout(autoTimeoutFor(d))
	}
}

func resolveIPv4(host string) (net.IP, error) {
	addr, err := net.ResolveIPAddr("ip4", host)
	if err != nil {
		return nil, err
	}
	return addr.IP, nil
}

// Privileged reports whether a raw socket was required.
func (m *Monitor) Privileged() bool { return m.prober.Privileged() }

// Run probes until ctx is cancelled, or until limit rounds have completed when
// limit is positive. It returns once every result has been folded in.
func (m *Monitor) Run(ctx context.Context, limit int) {
	go m.prober.Run(ctx, limit)

	for r := range m.prober.Results() {
		m.mu.Lock()
		if r.Index >= 0 && r.Index < len(m.stats) {
			m.stats[r.Index].Record(r)
		}
		m.mu.Unlock()

		if m.recorder != nil && r.Index >= 0 && r.Index < len(m.targets) {
			// A failing recorder must not take the table down with it; it
			// keeps its own error for the caller to report at close.
			_ = m.recorder.Record(r, time.Now(), m.targets[r.Index].Display)
		}
	}
}

// Close releases the probe socket.
func (m *Monitor) Close() error { return m.prober.Close() }

// Snapshot copies the current state of every host.
func (m *Monitor) Snapshot() []HostView {
	m.mu.RLock()
	defer m.mu.RUnlock()

	views := make([]HostView, len(m.stats))
	for i := range m.stats {
		s := &m.stats[i]
		// A name that never resolved is reported as such rather than as a
		// plain down host: nothing was ever sent, so "unreachable" would be
		// pointing at the wrong problem.
		state := s.State
		if m.targets[i].Err != nil {
			state = StateUnresolved
		}
		views[i] = HostView{
			Display:    m.targets[i].Display,
			State:      state,
			Last:       s.Last,
			Min:        s.Min,
			Max:        s.Max,
			Avg:        s.Avg(),
			Jitter:     s.Jitter(),
			Loss:       s.Loss(),
			HasLatency: s.HasLatency(),
			HasHistory: s.HasHistory(),
			HasJitter:  s.HasJitter(),
			Recent:     s.Recent(),
			ResolveErr: m.targets[i].Err,
		}
	}
	return views
}

// Done reports whether every host has completed at least one probe.
func (m *Monitor) Done() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for i := range m.stats {
		if m.stats[i].State == StateWaiting {
			return false
		}
	}
	return true
}
