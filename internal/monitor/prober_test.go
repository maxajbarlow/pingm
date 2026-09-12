package monitor

import (
	"context"
	"net"
	"testing"
	"time"
)

// probeSocketAvailable reports whether this machine lets the test open an ICMP
// socket at all; sandboxes and locked-down CI often do not.
func probeSocketAvailable(t *testing.T) {
	t.Helper()
	conn, _, err := listen()
	if err != nil {
		t.Skipf("no ICMP socket available here: %v", err)
	}
	conn.Close()
}

func loopbackTarget() Target {
	return Target{Display: "127.0.0.1", IP: net.IPv4(127, 0, 0, 1)}
}

// blackholeTarget is a TEST-NET-1 address (RFC 5737), reserved for
// documentation and not supposed to answer.
func blackholeTarget() Target {
	return Target{Display: "192.0.2.1", IP: net.IPv4(192, 0, 2, 1)}
}

// requireBlackhole skips when the local network answers for TEST-NET-1, which
// some captive networks and sandboxes do. Without this the test is measuring
// the network rather than the prober.
func requireBlackhole(t *testing.T) {
	t.Helper()
	stats := collect(t, []Target{blackholeTarget()}, 3, 300*time.Millisecond)
	if stats[0].Recv > 0 {
		t.Skip("this network answers for 192.0.2.1, so it is not a black hole here")
	}
}

func collect(t *testing.T, targets []Target, rounds int, timeout time.Duration) map[int]*Stats {
	t.Helper()
	p, err := NewProber(targets, 100*time.Millisecond, timeout)
	if err != nil {
		t.Skipf("cannot probe here: %v", err)
	}
	defer p.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	go p.Run(ctx, rounds)

	stats := make(map[int]*Stats, len(targets))
	for i := range targets {
		stats[i] = &Stats{}
	}
	for r := range p.Results() {
		stats[r.Index].Record(r)
	}
	return stats
}

func TestProberReportsLoopbackAsUp(t *testing.T) {
	probeSocketAvailable(t)

	stats := collect(t, []Target{loopbackTarget()}, 3, time.Second)
	s := stats[0]

	if s.Sent != 3 {
		t.Errorf("Sent = %d, want 3", s.Sent)
	}
	if s.Recv == 0 {
		t.Fatal("no replies from loopback")
	}
	if s.State != StateUp {
		t.Errorf("State = %v, want StateUp", s.State)
	}
	if s.Last <= 0 {
		t.Errorf("Last = %v, want a positive RTT", s.Last)
	}
}

func TestProberReportsUnreachableAddressAsDown(t *testing.T) {
	probeSocketAvailable(t)
	requireBlackhole(t)

	stats := collect(t, []Target{blackholeTarget()}, 2, 300*time.Millisecond)
	s := stats[0]

	if s.Sent != 2 {
		t.Errorf("Sent = %d, want 2", s.Sent)
	}
	if s.Recv != 0 {
		t.Errorf("Recv = %d, want 0 from a black hole", s.Recv)
	}
	if s.State != StateDown {
		t.Errorf("State = %v, want StateDown", s.State)
	}
	if s.Loss() != 100 {
		t.Errorf("Loss() = %v, want 100", s.Loss())
	}
}

// Every target must be demultiplexed correctly off the single shared socket.
func TestProberKeepsTargetsSeparateOnOneSocket(t *testing.T) {
	probeSocketAvailable(t)
	requireBlackhole(t)

	targets := []Target{loopbackTarget(), blackholeTarget(), loopbackTarget()}
	stats := collect(t, targets, 3, 300*time.Millisecond)

	for _, i := range []int{0, 2} {
		if stats[i].Recv == 0 {
			t.Errorf("target %d (loopback) got no replies", i)
		}
		if stats[i].State != StateUp {
			t.Errorf("target %d State = %v, want StateUp", i, stats[i].State)
		}
	}
	if stats[1].Recv != 0 {
		t.Errorf("black hole target received %d replies, want 0", stats[1].Recv)
	}
}

func TestProberCountsUnresolvableTargetAsLoss(t *testing.T) {
	probeSocketAvailable(t)

	unresolved := Target{Display: "nope.invalid", Err: &net.DNSError{Err: "no such host"}}
	stats := collect(t, []Target{unresolved}, 2, 200*time.Millisecond)
	s := stats[0]

	if s.Sent != 2 || s.Recv != 0 {
		t.Errorf("counters = %d/%d, want 2/0", s.Sent, s.Recv)
	}
	if s.State != StateDown {
		t.Errorf("State = %v, want StateDown", s.State)
	}
}

// Run must return rather than deadlock once the round limit is reached.
func TestProberRunReturnsAfterRoundLimit(t *testing.T) {
	probeSocketAvailable(t)

	p, err := NewProber([]Target{loopbackTarget()}, 50*time.Millisecond, 200*time.Millisecond)
	if err != nil {
		t.Skipf("cannot probe here: %v", err)
	}
	defer p.Close()

	done := make(chan struct{})
	go func() {
		p.Run(context.Background(), 2)
		close(done)
	}()
	for range p.Results() {
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after reaching the round limit")
	}
}

func TestProberStopsOnContextCancel(t *testing.T) {
	probeSocketAvailable(t)

	p, err := NewProber([]Target{loopbackTarget()}, 50*time.Millisecond, 200*time.Millisecond)
	if err != nil {
		t.Skipf("cannot probe here: %v", err)
	}
	defer p.Close()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		p.Run(ctx, 0)
		close(done)
	}()

	time.Sleep(150 * time.Millisecond)
	cancel()
	for range p.Results() {
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancellation")
	}
}

// --- Deterministic tests, no network involved -----------------------------

func newOfflineProber(t *testing.T, targets int) *Prober {
	t.Helper()
	list := make([]Target, targets)
	for i := range list {
		list[i] = Target{Display: "h", IP: net.IPv4(127, 0, 0, 1)}
	}
	p, err := NewProber(list, time.Second, time.Second)
	if err != nil {
		t.Skipf("cannot open a probe socket here: %v", err)
	}
	return p
}

// Every probe must yield exactly one result, even when the answer never comes.
func TestExpireRetiresProbesPastTheTimeout(t *testing.T) {
	p := newOfflineProber(t, 2)
	defer p.Close()

	now := time.Now()
	p.inflight[1] = probe{target: 0, number: 1, sentAt: now.Add(-2 * time.Second)}
	p.inflight[2] = probe{target: 1, number: 1, sentAt: now} // still fresh

	go p.expire(now)

	r := <-p.results
	if r.Index != 0 || r.OK {
		t.Errorf("got %+v, want a loss for target 0", r)
	}
	if len(p.inflight) != 1 {
		t.Errorf("inflight has %d entries, want the fresh probe left alone", len(p.inflight))
	}
}

func TestExpireAllRetiresEverythingOutstanding(t *testing.T) {
	p := newOfflineProber(t, 3)
	defer p.Close()

	for i := 0; i < 3; i++ {
		p.inflight[uint16(i+1)] = probe{target: i, number: 1, sentAt: time.Now()}
	}
	go p.expireAll()

	seen := map[int]bool{}
	for i := 0; i < 3; i++ {
		r := <-p.results
		if r.OK {
			t.Errorf("target %d reported OK, want a loss", r.Index)
		}
		seen[r.Index] = true
	}
	if len(seen) != 3 {
		t.Errorf("retired %d distinct targets, want 3", len(seen))
	}
	if len(p.inflight) != 0 {
		t.Errorf("inflight still holds %d entries", len(p.inflight))
	}
}

// A reply must only be credited to the probe that actually sent it, even when
// the 16-bit sequence number has been recycled.
func TestResolveIgnoresAReplyForARecycledSequence(t *testing.T) {
	p := newOfflineProber(t, 2)
	defer p.Close()

	// Sequence 7 now belongs to target 1 probe 4; a late reply for the
	// previous occupant (target 0 probe 1) must not be credited to it.
	p.inflight[7] = probe{target: 1, number: 4, sentAt: time.Now()}
	p.resolve(7, 0, 1, time.Now())

	select {
	case r := <-p.results:
		t.Fatalf("stale reply produced %+v, want it ignored", r)
	default:
	}
	if len(p.inflight) != 1 {
		t.Error("stale reply consumed the outstanding probe")
	}
}

func TestResolveCreditsAMatchingReply(t *testing.T) {
	p := newOfflineProber(t, 1)
	defer p.Close()

	p.inflight[9] = probe{target: 0, number: 2, sentAt: time.Now().Add(-5 * time.Millisecond)}
	p.resolve(9, 0, 2, time.Now())

	r := <-p.results
	if !r.OK || r.Index != 0 || r.Probe != 2 {
		t.Errorf("got %+v, want a successful result for target 0 probe 2", r)
	}
	if r.RTT <= 0 {
		t.Errorf("RTT = %v, want positive", r.RTT)
	}
}

func TestPayloadRoundTrips(t *testing.T) {
	data := makePayload(517, 90210)
	target, number, ok := readPayload(data)
	if !ok {
		t.Fatal("readPayload rejected our own payload")
	}
	if target != 517 || number != 90210 {
		t.Errorf("got target=%d number=%d, want 517/90210", target, number)
	}
}

func TestReadPayloadRejectsForeignTraffic(t *testing.T) {
	for _, junk := range [][]byte{nil, []byte("x"), []byte("nope-not-ours"), []byte("pingm")} {
		if _, _, ok := readPayload(junk); ok {
			t.Errorf("readPayload(%q) accepted foreign data", junk)
		}
	}
}
