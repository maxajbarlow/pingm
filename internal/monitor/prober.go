package monitor

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
)

// sweepInterval is how often unanswered probes are checked for expiry. It is
// well under any sane timeout so a lost probe is reported promptly without
// spinning.
const sweepInterval = 50 * time.Millisecond

// magic tags our echo requests, and the two identifiers after it let a reply
// be matched even if the 16-bit wire sequence number has wrapped and been
// handed out again.
const magic = "pingm"

const payloadLen = len(magic) + 8

func makePayload(target, number int) []byte {
	b := make([]byte, payloadLen)
	copy(b, magic)
	binary.BigEndian.PutUint32(b[len(magic):], uint32(target))
	binary.BigEndian.PutUint32(b[len(magic)+4:], uint32(number))
	return b
}

func readPayload(b []byte) (target, number int, ok bool) {
	if len(b) < payloadLen || string(b[:len(magic)]) != magic {
		return 0, 0, false
	}
	return int(binary.BigEndian.Uint32(b[len(magic):])),
		int(binary.BigEndian.Uint32(b[len(magic)+4:])), true
}

// Target is one host to probe, paired with the text the user typed so the
// table can label the row with what they asked for rather than a resolved IP.
type Target struct {
	Display string
	IP      net.IP
	Err     error // Non-nil when the name could not be resolved.
}

// destination wraps the address in the form this socket expects: a datagram
// socket addresses peers as UDP, a raw socket as plain IP.
func (p *Prober) destination(ip net.IP) net.Addr {
	if p.raw {
		return &net.IPAddr{IP: ip}
	}
	return &net.UDPAddr{IP: ip}
}

// Prober sends ICMP echo requests to every target over a single socket and
// reports each outcome exactly once.
//
// The shell version forked a `ping` process per host per interval — about 770
// processes a second across a /24. One socket and two goroutines replace all
// of it, which is the main reason this tool is no longer a shell script.
type Prober struct {
	conn    *icmp.PacketConn
	raw     bool // true when using raw sockets (privileged) rather than datagram
	id      int
	targets []Target
	results chan Result

	// Both are adjustable while probing is under way, so they are held as
	// atomics rather than under the mutex: the sender reads the interval and
	// the sweeper reads the timeout, and neither should have to contend with
	// the receive path for them.
	interval atomic.Int64 // nanoseconds
	timeout  atomic.Int64 // nanoseconds
	reset    chan struct{}

	// mu guards inflight, which the sender, receiver and sweeper all touch.
	// seq and counts are only ever written by the sender, but are kept under
	// the same lock rather than reasoning about two regimes.
	mu       sync.Mutex
	seq      uint16
	inflight map[uint16]probe
	counts   []int // Per-target probe number, for ordering results.
}

type probe struct {
	target int
	number int
	sentAt time.Time
}

// NewProber opens the ICMP socket and prepares to probe targets.
//
// It prefers a datagram ICMP socket, which needs no privileges on macOS and on
// Linux where net.ipv4.ping_group_range permits it, and falls back to a raw
// socket (which does require root) only if that is unavailable.
func NewProber(targets []Target, interval, timeout time.Duration) (*Prober, error) {
	conn, raw, err := listen()
	if err != nil {
		return nil, err
	}
	p := &Prober{
		conn:     conn,
		raw:      raw,
		id:       os.Getpid() & 0xffff,
		targets:  targets,
		results:  make(chan Result, len(targets)*4),
		reset:    make(chan struct{}, 1),
		inflight: make(map[uint16]probe, len(targets)*4),
		counts:   make([]int, len(targets)),
	}
	p.interval.Store(int64(interval))
	p.timeout.Store(int64(timeout))
	return p, nil
}

// Interval is the current gap between probes for a given host.
func (p *Prober) Interval() time.Duration { return time.Duration(p.interval.Load()) }

// SetInterval changes the probe cadence while running. The change takes
// effect immediately: a wait already under way is cut short rather than
// running out at the old interval, so speeding up feels instant.
func (p *Prober) SetInterval(d time.Duration) {
	if d <= 0 {
		return
	}
	p.interval.Store(int64(d))
	select {
	case p.reset <- struct{}{}:
	default: // A reset is already pending; one is enough.
	}
}

// Timeout is how long a probe may go unanswered before it counts as lost.
func (p *Prober) Timeout() time.Duration { return time.Duration(p.timeout.Load()) }

// SetTimeout changes the reply deadline while running. The sweeper reads it
// afresh each tick, so no wake-up is needed.
func (p *Prober) SetTimeout(d time.Duration) {
	if d > 0 {
		p.timeout.Store(int64(d))
	}
}

func listen() (*icmp.PacketConn, bool, error) {
	if conn, err := icmp.ListenPacket("udp4", "0.0.0.0"); err == nil {
		return conn, false, nil
	}
	conn, err := icmp.ListenPacket("ip4:icmp", "0.0.0.0")
	if err != nil {
		return nil, false, fmt.Errorf("cannot open an ICMP socket (try running as root): %w", err)
	}
	return conn, true, nil
}

// Results delivers exactly one Result per probe sent, and is closed once
// probing has stopped. Callers must drain it until it closes: delivery is
// blocking, so an abandoned consumer would stall the probe loops.
func (p *Prober) Results() <-chan Result { return p.results }

// Privileged reports whether a raw socket was needed, which is worth surfacing
// because it means the binary is running with elevated rights.
func (p *Prober) Privileged() bool { return p.raw }

// Close releases the socket.
func (p *Prober) Close() error { return p.conn.Close() }

// Run probes every target on the interval until ctx is cancelled. If limit is
// positive it stops after that many probes per target and returns.
//
// The results channel is closed only once both workers have stopped, so no
// send can race the close.
func (p *Prober) Run(ctx context.Context, limit int) {
	defer close(p.results)

	// A derived context so the workers can be stopped when the round limit is
	// reached, not only when the caller cancels.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); p.receive(ctx) }()
	go func() { defer wg.Done(); p.sweep(ctx) }()

loop:
	for rounds := 0; ; {
		p.sendRound()
		rounds++

		if limit > 0 && rounds >= limit {
			// Give the last round a chance to be answered or expire.
			p.drain(ctx)
			break loop
		}
		if !p.waitForNextRound(ctx) {
			break loop
		}
	}

	cancel()       // Stops the sweeper.
	p.conn.Close() // Unblocks the receiver.
	wg.Wait()

	// Whatever is still outstanding was never answered. Retire it here so
	// that every probe sent yields exactly one result, including the final
	// round, rather than depending on a last sweeper tick landing in time.
	p.expireAll()
}

// waitForNextRound sleeps until the next round is due, returning false if the
// run was cancelled. A timer is built per wait rather than a long-lived
// ticker so that a change of interval is picked up at once: SetInterval wakes
// the wait, and it restarts against the new value.
func (p *Prober) waitForNextRound(ctx context.Context) bool {
	for {
		timer := time.NewTimer(p.Interval())
		select {
		case <-ctx.Done():
			timer.Stop()
			return false
		case <-timer.C:
			return true
		case <-p.reset:
			timer.Stop() // The interval changed; wait again on the new one.
		}
	}
}

// drain waits out the final round so the last probes are reported rather than
// being discarded when the socket closes.
func (p *Prober) drain(ctx context.Context) {
	deadline := time.NewTimer(p.Timeout() + sweepInterval)
	defer deadline.Stop()
	select {
	case <-ctx.Done():
	case <-deadline.C:
	}
}

func (p *Prober) sendRound() {
	for i := range p.targets {
		p.send(i)
	}
}

func (p *Prober) send(index int) {
	target := p.targets[index]

	p.mu.Lock()
	p.counts[index]++
	number := p.counts[index]
	p.mu.Unlock()

	// A name that would not resolve can never answer; report it as a loss
	// rather than silently omitting the host from the table.
	if target.Err != nil || target.IP == nil {
		p.report(Result{Index: index, Probe: number, OK: false})
		return
	}

	p.mu.Lock()
	p.seq++
	seq := p.seq
	// The sequence space is only 16 bits, so with many hosts, a short
	// interval and a long -t it can come round again while its previous
	// owner is still outstanding. Retire the displaced probe as lost rather
	// than letting it vanish and quietly undercount that host's losses.
	displaced, collided := p.inflight[seq]
	p.inflight[seq] = probe{target: index, number: number, sentAt: time.Now()}
	p.mu.Unlock()

	if collided {
		p.report(Result{Index: displaced.target, Probe: displaced.number, OK: false})
	}

	msg := icmp.Message{
		Type: ipv4.ICMPTypeEcho,
		Code: 0,
		Body: &icmp.Echo{
			ID:   p.id,
			Seq:  int(seq),
			Data: makePayload(index, number),
		},
	}
	encoded, err := msg.Marshal(nil)
	if err == nil {
		_, err = p.conn.WriteTo(encoded, p.destination(target.IP))
	}
	if err != nil {
		// The probe never left; retire it now instead of waiting for the
		// sweeper, so loss is attributed at the moment it happened.
		p.mu.Lock()
		delete(p.inflight, seq)
		p.mu.Unlock()
		p.report(Result{Index: index, Probe: number, OK: false})
	}
}

func (p *Prober) receive(ctx context.Context) {
	buf := make([]byte, 1500)
	for {
		if ctx.Err() != nil {
			return
		}
		// A deadline keeps this loop responsive to cancellation even when no
		// traffic arrives at all.
		_ = p.conn.SetReadDeadline(time.Now().Add(sweepInterval))

		n, _, err := p.conn.ReadFrom(buf)
		if err != nil {
			var netErr net.Error
			if errors.As(err, &netErr) && netErr.Timeout() {
				continue
			}
			return // Socket closed.
		}

		msg, err := icmp.ParseMessage(ipv4.ICMPTypeEchoReply.Protocol(), buf[:n])
		if err != nil || msg.Type != ipv4.ICMPTypeEchoReply {
			continue
		}
		echo, ok := msg.Body.(*icmp.Echo)
		if !ok {
			continue
		}
		// On a datagram socket the kernel rewrites the ID to the socket's own
		// port, so only the sequence number is ours to match on. A raw socket
		// sees every ICMP reply on the host, so there the ID must be checked.
		if p.raw && echo.ID != p.id {
			continue
		}
		target, number, ok := readPayload(echo.Data)
		if !ok {
			continue // Not one of ours.
		}
		p.resolve(uint16(echo.Seq), target, number, time.Now())
	}
}

// resolve retires an outstanding probe as answered. A reply that arrives after
// the sweeper already expired it finds nothing and is ignored, so each probe
// yields exactly one result. The echoed target and probe number must match the
// outstanding entry, so a late reply cannot be credited to whichever probe has
// since inherited that sequence number.
func (p *Prober) resolve(seq uint16, target, number int, at time.Time) {
	p.mu.Lock()
	pending, ok := p.inflight[seq]
	if ok && pending.target == target && pending.number == number {
		delete(p.inflight, seq)
	} else {
		ok = false
	}
	p.mu.Unlock()

	if !ok {
		return
	}
	p.report(Result{
		Index: pending.target,
		Probe: pending.number,
		RTT:   at.Sub(pending.sentAt),
		OK:    true,
	})
}

func (p *Prober) sweep(ctx context.Context) {
	ticker := time.NewTicker(sweepInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			p.expire(now)
		}
	}
}

// expireAll retires every outstanding probe as lost. Safe to call only once
// the sender, receiver and sweeper have stopped.
func (p *Prober) expireAll() {
	p.mu.Lock()
	lost := make([]Result, 0, len(p.inflight))
	for seq, pending := range p.inflight {
		delete(p.inflight, seq)
		lost = append(lost, Result{Index: pending.target, Probe: pending.number, OK: false})
	}
	p.mu.Unlock()

	for _, r := range lost {
		p.report(r)
	}
}

func (p *Prober) expire(now time.Time) {
	var lost []Result

	p.mu.Lock()
	for seq, pending := range p.inflight {
		if now.Sub(pending.sentAt) >= p.Timeout() {
			delete(p.inflight, seq)
			lost = append(lost, Result{Index: pending.target, Probe: pending.number, OK: false})
		}
	}
	p.mu.Unlock()

	for _, r := range lost {
		p.report(r)
	}
}

// report delivers one probe outcome.
//
// The send blocks deliberately. Sent, Recv and therefore Loss are derived
// solely from this channel, so silently dropping a result would corrupt a
// host's loss figure for the rest of the run. Back-pressure on the probe
// loops is the lesser evil, and callers of Run are required to drain Results
// until it closes, so a send can always eventually proceed.
func (p *Prober) report(r Result) {
	p.results <- r
}
