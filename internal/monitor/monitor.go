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
	Loss       float64
	HasLatency bool
	HasHistory bool
	ResolveErr error
}

// Monitor probes a set of hosts and maintains their running statistics.
type Monitor struct {
	prober  *Prober
	targets []Target

	mu    sync.RWMutex
	stats []Stats
}

// Resolver turns a hostname into an IPv4 address. It is a field so tests can
// avoid depending on DNS.
type Resolver func(host string) (net.IP, error)

// New resolves every host and opens the probe socket.
func New(hosts []string, interval, timeout time.Duration, resolve Resolver) (*Monitor, error) {
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
		prober:  prober,
		targets: targets,
		stats:   make([]Stats, len(hosts)),
	}, nil
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
		views[i] = HostView{
			Display:    m.targets[i].Display,
			State:      s.State,
			Last:       s.Last,
			Min:        s.Min,
			Max:        s.Max,
			Avg:        s.Avg(),
			Loss:       s.Loss(),
			HasLatency: s.HasLatency(),
			HasHistory: s.HasHistory(),
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
