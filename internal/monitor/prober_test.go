package monitor

import (
	"context"
	"net"
	"syscall"
	"testing"
	"time"
)

// probeSocketAvailable reports whether this machine lets the test open an ICMP
// socket at all; sandboxes and locked-down CI often do not.
func probeSocketAvailable(t *testing.T) {
	t.Helper()
	conn, _, err := listen(4)
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

// --- Pacing and buffering -------------------------------------------------
//
// These cover the fix for a bug where a large sweep reported live hosts as
// down. Every probe was sent in one burst, so every reply came back in one
// burst, and the socket's default 8KB receive queue — about thirty packets —
// dropped the rest. Nothing errored: the replies simply never arrived, and
// hosts flapped. Against 1.1.1.0/24 the kernel took 9,728 replies while the
// process read only 4,295 of them, and 252 of 256 hosts flapped.

// pacingProber builds a Prober with no socket, for exercising the send loop's
// timing. Its targets have no address, so every probe short-circuits into a
// reported loss instead of touching the network.
func pacingProber(targets int, interval time.Duration) *Prober {
	p := &Prober{
		targets:  make([]Target, targets),
		counts:   make([]int, targets),
		results:  make(chan Result, targets*4),
		inflight: make(map[uint16]probe),
		reset:    make(chan struct{}, 1),
	}
	p.interval.Store(int64(interval))
	return p
}

func TestSendGapSpreadsARoundAcrossTheInterval(t *testing.T) {
	p := pacingProber(250, time.Second)

	gap := p.sendGap()
	if gap <= 0 {
		t.Fatalf("sendGap() = %v for 250 targets, want the round spread out", gap)
	}

	// The whole round has to fit inside the interval with room to spare, or
	// rounds would overlap and probes would pile up.
	round := gap * time.Duration(len(p.targets))
	if round >= p.Interval() {
		t.Errorf("a round takes %v, which does not fit in a %v interval", round, p.Interval())
	}
}

// A handful of hosts have no burst worth spreading, and dribbling them out
// across a long interval would only make the table confusing.
func TestSendGapDoesNotDribbleOutAShortHostList(t *testing.T) {
	p := pacingProber(3, 30*time.Second)

	if got := p.sendGap(); got > maxSendGap {
		t.Errorf("sendGap() = %v for 3 targets at 30s, want at most %v", got, maxSendGap)
	}
}

func TestSingleTargetIsNotPaced(t *testing.T) {
	p := pacingProber(1, time.Second)

	if got := p.sendGap(); got != 0 {
		t.Errorf("sendGap() = %v for one target, want 0 — there is nothing to spread", got)
	}
}

// The gap has to follow the interval, or speeding up with - would leave the
// round still paced for the old, slower cadence.
func TestSendGapFollowsTheInterval(t *testing.T) {
	p := pacingProber(500, 10*time.Second)

	p.interval.Store(int64(10 * time.Second))
	slow := p.sendGap()
	p.interval.Store(int64(500 * time.Millisecond))
	fast := p.sendGap()

	if !(fast < slow) {
		t.Errorf("gap at 500ms (%v) is not shorter than at 10s (%v)", fast, slow)
	}
}

// Pacing costs nothing if the round is then charged the full interval on top:
// the effective rate would halve. The wait has to be measured from when the
// round started.
func TestWaitForNextRoundMeasuresFromTheRoundStart(t *testing.T) {
	p := pacingProber(0, 200*time.Millisecond)

	// A round that already used up most of its interval should barely wait.
	started := time.Now().Add(-190 * time.Millisecond)
	begin := time.Now()
	if !p.waitForNextRound(context.Background(), started) {
		t.Fatal("waitForNextRound reported cancellation")
	}
	if waited := time.Since(begin); waited > 100*time.Millisecond {
		t.Errorf("waited a further %v after a round that had already run 190ms", waited)
	}
}

// A round that overran its interval means the next one is due immediately.
func TestOverrunningRoundDoesNotWaitAtAll(t *testing.T) {
	p := pacingProber(0, 100*time.Millisecond)

	begin := time.Now()
	if !p.waitForNextRound(context.Background(), time.Now().Add(-time.Second)) {
		t.Fatal("waitForNextRound reported cancellation")
	}
	if waited := time.Since(begin); waited > 20*time.Millisecond {
		t.Errorf("waited %v after a round that had already overrun", waited)
	}
}

// The whole point is that a round takes time; it must still abandon promptly
// when the run is cancelled rather than paying out the rest of its pacing.
func TestSendRoundActuallySpreadsTheSendsOut(t *testing.T) {
	p := pacingProber(50, time.Second)

	start := time.Now()
	p.sendRound(context.Background())

	// 50 targets, each waiting up to maxSendGap before the next. A burst
	// would have finished in microseconds.
	if elapsed := time.Since(start); elapsed < 20*time.Millisecond {
		t.Errorf("a 50-target round finished in %v — the sends were not spread out", elapsed)
	}
}

func TestSendRoundStopsWhenCancelled(t *testing.T) {
	p := pacingProber(100, 10*time.Second)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if p.sendRound(ctx) {
		t.Error("sendRound reported success on a cancelled run")
	}
}

// The receive queue has to hold a burst, because anything it cannot hold is
// dropped by the kernel silently and read as a host being down.
func TestReceiveBufferGrowsWithTheHostCount(t *testing.T) {
	small, large := receiveBuffer(1), receiveBuffer(1024)
	if !(large > small) {
		t.Errorf("buffer for 1024 hosts (%d) is not larger than for one (%d)", large, small)
	}

	// A /24 in flight is the case that broke: several rounds of 254 replies
	// must fit comfortably, where the 8KB default held about thirty packets.
	if got := receiveBuffer(254); got < 254*256 {
		t.Errorf("buffer for a /24 is %d bytes, too small to hold a round of replies", got)
	}
}

func TestReceiveBufferStaysWithinSaneBounds(t *testing.T) {
	if got := receiveBuffer(0); got < 64<<10 {
		t.Errorf("buffer for no targets = %d, want a sensible floor", got)
	}
	if got := receiveBuffer(1 << 20); got > 8<<20 {
		t.Errorf("buffer for a huge host list = %d, want it capped", got)
	}
}

// The socket the prober actually opens must carry the enlarged buffer, or the
// fix only exists in theory.
func TestTheOpenedSocketHasAnEnlargedReceiveBuffer(t *testing.T) {
	conn, _, err := listen(254)
	if err != nil {
		t.Skipf("cannot open a probe socket here: %v", err)
	}
	defer conn.Close()

	sc, ok := conn.(interface {
		SyscallConn() (syscall.RawConn, error)
	})
	if !ok {
		t.Skip("this socket does not expose its file descriptor")
	}
	raw, err := sc.SyscallConn()
	if err != nil {
		t.Skipf("cannot reach the descriptor: %v", err)
	}

	var size int
	var opErr error
	if err := raw.Control(func(fd uintptr) {
		size, opErr = syscall.GetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_RCVBUF)
	}); err != nil {
		t.Skipf("cannot read the socket option: %v", err)
	}
	if opErr != nil {
		t.Skipf("cannot read SO_RCVBUF: %v", opErr)
	}

	// The kernel clamps to kern.ipc.maxsockbuf, so this asserts "much bigger
	// than the 8KB default" rather than the exact figure asked for.
	if size < 64<<10 {
		t.Errorf("SO_RCVBUF = %d, want well above the 8KB default that dropped replies", size)
	}
}
