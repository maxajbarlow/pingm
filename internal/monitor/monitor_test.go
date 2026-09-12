package monitor

import (
	"context"
	"errors"
	"net"
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
