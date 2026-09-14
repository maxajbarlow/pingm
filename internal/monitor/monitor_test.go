package monitor

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"
)

func fixedResolver(ip string) Resolver {
	return func(string) (net.IP, error) { return net.ParseIP(ip), nil }
}

func failingResolver(err error) Resolver {
	return func(string) (net.IP, error) { return nil, err }
}

func newTestMonitor(t *testing.T, hosts []string, resolve Resolver) *Monitor {
	t.Helper()
	m, err := New(hosts, 100*time.Millisecond, 200*time.Millisecond, resolve)
	if err != nil {
		t.Skipf("cannot open a probe socket here: %v", err)
	}
	return m
}

func TestSnapshotStartsWaitingForEveryHost(t *testing.T) {
	m := newTestMonitor(t, []string{"a", "b", "c"}, fixedResolver("127.0.0.1"))
	defer m.Close()

	views := m.Snapshot()
	if len(views) != 3 {
		t.Fatalf("got %d views, want 3", len(views))
	}
	for i, v := range views {
		if v.State != StateWaiting {
			t.Errorf("view %d State = %v, want StateWaiting", i, v.State)
		}
		if v.HasLatency || v.HasHistory {
			t.Errorf("view %d claims data before any probe", i)
		}
	}
}

func TestSnapshotLabelsRowsWithWhatTheUserTyped(t *testing.T) {
	m := newTestMonitor(t, []string{"router.local", "10.0.0.1"}, fixedResolver("127.0.0.1"))
	defer m.Close()

	views := m.Snapshot()
	if views[0].Display != "router.local" || views[1].Display != "10.0.0.1" {
		t.Errorf("displays = %q/%q, want the original spellings", views[0].Display, views[1].Display)
	}
}

func TestUnresolvableHostIsKeptAndCarriesItsError(t *testing.T) {
	boom := errors.New("no such host")
	m := newTestMonitor(t, []string{"nope.invalid"}, failingResolver(boom))
	defer m.Close()

	views := m.Snapshot()
	if len(views) != 1 {
		t.Fatalf("got %d views, want the unresolvable host kept", len(views))
	}
	if !errors.Is(views[0].ResolveErr, boom) {
		t.Errorf("ResolveErr = %v, want %v", views[0].ResolveErr, boom)
	}
}

func TestSnapshotIsACopyNotALiveView(t *testing.T) {
	m := newTestMonitor(t, []string{"a"}, fixedResolver("127.0.0.1"))
	defer m.Close()

	first := m.Snapshot()
	first[0].Display = "mutated"

	if second := m.Snapshot(); second[0].Display == "mutated" {
		t.Error("mutating a snapshot changed the monitor's own state")
	}
}

func TestDoneReportsWhetherEveryHostHasAVerdict(t *testing.T) {
	m := newTestMonitor(t, []string{"a", "b"}, fixedResolver("127.0.0.1"))
	defer m.Close()

	if m.Done() {
		t.Error("Done() = true before any probe")
	}
	m.stats[0].Record(Result{Probe: 1, OK: true, RTT: time.Millisecond})
	if m.Done() {
		t.Error("Done() = true with one host still waiting")
	}
	m.stats[1].Record(Result{Probe: 1, OK: false})
	if !m.Done() {
		t.Error("Done() = false once every host has a verdict")
	}
}

func TestRunFoldsResultsIntoSnapshots(t *testing.T) {
	m := newTestMonitor(t, []string{"127.0.0.1"}, fixedResolver("127.0.0.1"))
	defer m.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	m.Run(ctx, 2) // Returns once both rounds are accounted for.

	v := m.Snapshot()[0]
	if v.State == StateWaiting {
		t.Fatal("host is still waiting after two completed rounds")
	}
	if v.State == StateUp && !v.HasLatency {
		t.Error("an up host reports no latency")
	}
}

func TestSnapshotDerivesLossAndAverage(t *testing.T) {
	m := newTestMonitor(t, []string{"a"}, fixedResolver("127.0.0.1"))
	defer m.Close()

	m.stats[0].Record(Result{Probe: 1, OK: true, RTT: 10 * time.Millisecond})
	m.stats[0].Record(Result{Probe: 2, OK: true, RTT: 30 * time.Millisecond})
	m.stats[0].Record(Result{Probe: 3, OK: false})

	v := m.Snapshot()[0]
	if v.Avg != 20*time.Millisecond {
		t.Errorf("Avg = %v, want 20ms", v.Avg)
	}
	if v.Loss < 33.3 || v.Loss > 33.4 {
		t.Errorf("Loss = %v, want ~33.3", v.Loss)
	}
	if v.State != StateDown {
		t.Errorf("State = %v, want StateDown from the newest probe", v.State)
	}
	if !v.HasHistory {
		t.Error("HasHistory = false despite earlier replies")
	}
}

func TestStateString(t *testing.T) {
	for state, want := range map[State]string{
		StateUp: "up", StateDown: "down", StateWaiting: "waiting",
	} {
		if got := state.String(); got != want {
			t.Errorf("State(%d).String() = %q, want %q", state, got, want)
		}
	}
}

func TestDefaultResolverHandlesLoopback(t *testing.T) {
	ip, err := resolveIPv4("127.0.0.1")
	if err != nil {
		t.Fatalf("resolveIPv4 errored: %v", err)
	}
	if !ip.Equal(net.IPv4(127, 0, 0, 1)) {
		t.Errorf("resolveIPv4 = %v, want 127.0.0.1", ip)
	}
}

func TestNewUsesTheDefaultResolverWhenNoneGiven(t *testing.T) {
	m, err := New([]string{"127.0.0.1"}, time.Second, time.Second, nil)
	if err != nil {
		t.Skipf("cannot open a probe socket here: %v", err)
	}
	defer m.Close()

	if m.targets[0].IP == nil {
		t.Error("default resolver did not resolve 127.0.0.1")
	}
}

// --- Runtime interval changes ---------------------------------------------

func TestTimeoutDefaultsToTheInterval(t *testing.T) {
	m, err := New([]string{"a"}, 700*time.Millisecond, 0, fixedResolver("127.0.0.1"))
	if err != nil {
		t.Skipf("cannot open a probe socket here: %v", err)
	}
	defer m.Close()

	if got := m.prober.Timeout(); got != 700*time.Millisecond {
		t.Errorf("Timeout = %v, want it derived from the interval", got)
	}
}

func TestDerivedTimeoutIsCapped(t *testing.T) {
	m, err := New([]string{"a"}, time.Minute, 0, fixedResolver("127.0.0.1"))
	if err != nil {
		t.Skipf("cannot open a probe socket here: %v", err)
	}
	defer m.Close()

	if got := m.prober.Timeout(); got != MaxAutoTimeout {
		t.Errorf("Timeout = %v, want it capped at %v", got, MaxAutoTimeout)
	}
}

func TestSetIntervalCarriesTheDerivedTimeout(t *testing.T) {
	m, err := New([]string{"a"}, time.Second, 0, fixedResolver("127.0.0.1"))
	if err != nil {
		t.Skipf("cannot open a probe socket here: %v", err)
	}
	defer m.Close()

	m.SetInterval(200 * time.Millisecond)
	if got := m.Interval(); got != 200*time.Millisecond {
		t.Errorf("Interval = %v, want 200ms", got)
	}
	if got := m.prober.Timeout(); got != 200*time.Millisecond {
		t.Errorf("Timeout = %v, want it to follow the interval", got)
	}
}

// A timeout the user pinned with -t must survive an interval change.
func TestExplicitTimeoutSurvivesAnIntervalChange(t *testing.T) {
	m, err := New([]string{"a"}, time.Second, 750*time.Millisecond, fixedResolver("127.0.0.1"))
	if err != nil {
		t.Skipf("cannot open a probe socket here: %v", err)
	}
	defer m.Close()

	m.SetInterval(5 * time.Second)
	if got := m.prober.Timeout(); got != 750*time.Millisecond {
		t.Errorf("Timeout = %v, want the pinned 750ms to survive", got)
	}
}

func TestSetIntervalIgnoresNonPositiveValues(t *testing.T) {
	m, err := New([]string{"a"}, time.Second, 0, fixedResolver("127.0.0.1"))
	if err != nil {
		t.Skipf("cannot open a probe socket here: %v", err)
	}
	defer m.Close()

	for _, bad := range []time.Duration{0, -time.Second} {
		m.SetInterval(bad)
		if got := m.Interval(); got != time.Second {
			t.Errorf("SetInterval(%v) changed the interval to %v", bad, got)
		}
	}
}

// Speeding up must take effect at once rather than after the old, slower
// interval has elapsed — that is the whole point of the shortcut.
func TestSpeedingUpTakesEffectImmediately(t *testing.T) {
	m, err := New([]string{"127.0.0.1"}, 30*time.Second, 500*time.Millisecond, fixedResolver("127.0.0.1"))
	if err != nil {
		t.Skipf("cannot open a probe socket here: %v", err)
	}
	defer m.Close()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { m.Run(ctx, 0); close(done) }()

	// First round goes out immediately; the next would be 30s away.
	time.Sleep(300 * time.Millisecond)
	m.SetInterval(100 * time.Millisecond)
	time.Sleep(700 * time.Millisecond)

	sent := m.Snapshot()[0]
	cancel()
	<-done

	// At 30s we would have exactly one round; at 100ms, several.
	if sent.State == StateWaiting {
		t.Fatal("no probe completed at all")
	}
	m.mu.RLock()
	total := m.stats[0].Sent
	m.mu.RUnlock()
	if total < 3 {
		t.Errorf("only %d probes sent, want several — the new interval did not take effect", total)
	}
}

func TestUnresolvableHostIsReportedAsUnresolvedNotDown(t *testing.T) {
	m := newTestMonitor(t, []string{"nope.invalid"}, failingResolver(errors.New("no such host")))
	defer m.Close()

	// Even after the probe loop has written it off as a loss, the view must
	// keep saying why: the name is wrong, not the host unreachable.
	m.stats[0].Record(Result{Probe: 1, OK: false})

	if got := m.Snapshot()[0].State; got != StateUnresolved {
		t.Errorf("State = %v, want StateUnresolved", got)
	}
}

func TestResolvableHostIsNeverReportedAsUnresolved(t *testing.T) {
	m := newTestMonitor(t, []string{"a"}, fixedResolver("127.0.0.1"))
	defer m.Close()

	m.stats[0].Record(Result{Probe: 1, OK: false})
	if got := m.Snapshot()[0].State; got != StateDown {
		t.Errorf("State = %v, want StateDown", got)
	}
}

func TestHostsReturnsTheDisplayNamesInOrder(t *testing.T) {
	m := newTestMonitor(t, []string{"a", "b", "c"}, fixedResolver("127.0.0.1"))
	defer m.Close()

	got := m.Hosts()
	if len(got) != 3 || got[0] != "a" || got[1] != "b" || got[2] != "c" {
		t.Errorf("Hosts() = %v, want [a b c]", got)
	}
}

// recordingRecorder captures what the monitor hands it.
type recordingRecorder struct {
	mu    sync.Mutex
	rows  []Result
	hosts []string
	err   error
}

func (r *recordingRecorder) Record(res Result, _ time.Time, host string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rows = append(r.rows, res)
	r.hosts = append(r.hosts, host)
	return r.err
}

func (r *recordingRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.rows)
}

func TestRecorderSeesEveryProbeWithItsHostName(t *testing.T) {
	m := newTestMonitor(t, []string{"a", "b"}, fixedResolver("127.0.0.1"))
	defer m.Close()

	rec := &recordingRecorder{}
	m.SetRecorder(rec)
	m.Run(context.Background(), 3)

	// Two hosts, three rounds each: every probe sent yields exactly one row.
	if got := rec.count(); got != 6 {
		t.Errorf("recorder saw %d probes, want 6", got)
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	for i, host := range rec.hosts {
		if host != "a" && host != "b" {
			t.Errorf("row %d recorded host %q, want one of the targets", i, host)
		}
	}
}

// A recorder that cannot write must not take the table down with it.
func TestFailingRecorderDoesNotStopProbing(t *testing.T) {
	m := newTestMonitor(t, []string{"a"}, fixedResolver("127.0.0.1"))
	defer m.Close()

	m.SetRecorder(&recordingRecorder{err: errors.New("disk full")})
	m.Run(context.Background(), 2)

	if got := m.stats[0].Sent; got != 2 {
		t.Errorf("Sent = %d after a failing recorder, want 2", got)
	}
}
