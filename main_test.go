package main

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/maxajbarlow/pingm/internal/monitor"
	"github.com/maxajbarlow/pingm/internal/ui"
)

// An unset -t is passed through as zero, which is how the monitor is told to
// derive the timeout from the interval and keep the two in step as the
// interval changes at runtime. The derivation itself is covered in the
// monitor package.
func TestValidatePassesAnUnsetTimeoutThroughAsAuto(t *testing.T) {
	for _, interval := range []time.Duration{700 * time.Millisecond, time.Minute} {
		_, timeout, err := options{interval: interval, filter: "all"}.validate()
		if err != nil {
			t.Fatalf("validate errored: %v", err)
		}
		if timeout != 0 {
			t.Errorf("timeout = %v at interval %v, want 0 meaning auto", timeout, interval)
		}
	}
}

func TestValidateKeepsAnExplicitTimeout(t *testing.T) {
	_, timeout, err := options{interval: time.Second, timeout: 5 * time.Second, filter: "all"}.validate()
	if err != nil {
		t.Fatalf("validate errored: %v", err)
	}
	if timeout != 5*time.Second {
		t.Errorf("timeout = %v, want the explicit 5s", timeout)
	}
}

func TestValidateResolvesTheFilter(t *testing.T) {
	filter, _, err := options{interval: time.Second, filter: "offline"}.validate()
	if err != nil {
		t.Fatalf("validate errored: %v", err)
	}
	if filter != ui.FilterDown {
		t.Errorf("filter = %v, want FilterDown", filter)
	}
}

func TestValidateRejectsBadInput(t *testing.T) {
	cases := map[string]options{
		"unknown filter":    {interval: time.Second, filter: "sideways"},
		"zero interval":     {interval: 0, filter: "all"},
		"negative interval": {interval: -time.Second, filter: "all"},
		"negative count":    {interval: time.Second, count: -1, filter: "all"},
		"negative timeout":  {interval: time.Second, timeout: -time.Second, filter: "all"},
	}
	for name, o := range cases {
		t.Run(name, func(t *testing.T) {
			if _, _, err := o.validate(); err == nil {
				t.Errorf("validate(%+v) succeeded, want an error", o)
			}
		})
	}
}

func TestConfirmSkipsBelowTheThreshold(t *testing.T) {
	var out bytes.Buffer
	if err := confirm(confirmThreshold-1, false, true, strings.NewReader(""), &out); err != nil {
		t.Errorf("confirm errored below the threshold: %v", err)
	}
	if out.Len() != 0 {
		t.Errorf("confirm prompted below the threshold: %q", out.String())
	}
}

func TestConfirmSkipsWhenAssumeYes(t *testing.T) {
	var out bytes.Buffer
	if err := confirm(500, true, true, strings.NewReader(""), &out); err != nil {
		t.Errorf("confirm errored with -y: %v", err)
	}
	if out.Len() != 0 {
		t.Errorf("confirm prompted despite -y: %q", out.String())
	}
}

func TestConfirmRefusesWithoutATerminal(t *testing.T) {
	var out bytes.Buffer
	err := confirm(100, false, false, strings.NewReader(""), &out)
	if err == nil {
		t.Fatal("confirm succeeded with no terminal, want an error naming -y")
	}
	if !strings.Contains(err.Error(), "-y") {
		t.Errorf("error %q should tell the user about -y", err)
	}
}

func TestConfirmAcceptsAffirmativeAnswers(t *testing.T) {
	for _, answer := range []string{"y\n", "Y\n", "yes\n", " YES \n"} {
		var out bytes.Buffer
		if err := confirm(100, false, true, strings.NewReader(answer), &out); err != nil {
			t.Errorf("confirm(%q) errored: %v", answer, err)
		}
		if !strings.Contains(out.String(), "100 hosts") {
			t.Errorf("prompt did not state the host count: %q", out.String())
		}
	}
}

func TestConfirmTreatsAnythingElseAsNo(t *testing.T) {
	for _, answer := range []string{"\n", "n\n", "no\n", "maybe\n", ""} {
		var out bytes.Buffer
		if err := confirm(100, false, true, strings.NewReader(answer), &out); err == nil {
			t.Errorf("confirm(%q) succeeded, want it to abort", answer)
		}
	}
}

// --- run ------------------------------------------------------------------

// fakeProber stands in for the monitor so the setup path can be exercised
// without an ICMP socket, which a test machine may not grant.
type fakeProber struct {
	mu       sync.Mutex
	recorder monitor.Recorder
	hosts    []string
	probes   int
	running  bool
	stopped  bool
	closed   bool

	// started is closed once Run has actually begun probing, so a test can
	// wait for that instead of racing it.
	started chan struct{}
	once    sync.Once
}

func newFakeProber() *fakeProber { return &fakeProber{started: make(chan struct{})} }

func (p *fakeProber) Snapshot() []monitor.HostView {
	p.mu.Lock()
	defer p.mu.Unlock()
	views := make([]monitor.HostView, len(p.hosts))
	for i, h := range p.hosts {
		views[i] = monitor.HostView{Display: h, State: monitor.StateWaiting}
	}
	return views
}

func (p *fakeProber) SetInterval(time.Duration) {}
func (p *fakeProber) Privileged() bool          { return false }

func (p *fakeProber) SetRecorder(r monitor.Recorder) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.recorder = r
}

// Run records probes until it is cancelled, like the real monitor, so that
// anything depending on probing having actually stopped can be observed.
func (p *fakeProber) Run(ctx context.Context, _ int) {
	p.mu.Lock()
	p.running = true
	rec := p.recorder
	p.mu.Unlock()

	for probe := 1; ; probe++ {
		if rec != nil {
			_ = rec.Record(monitor.Result{Probe: probe, OK: true, RTT: time.Millisecond}, time.Now(), "h")
			p.mu.Lock()
			p.probes++
			p.mu.Unlock()
		}
		// Announced after the first pass, so a test waiting on this knows a
		// probe has been recorded rather than merely that Run was entered.
		p.once.Do(func() { close(p.started) })

		select {
		case <-ctx.Done():
			p.mu.Lock()
			p.running, p.stopped = false, true
			p.mu.Unlock()
			return
		case <-time.After(time.Millisecond):
		}
	}
}

func (p *fakeProber) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closed = true
	return nil
}

func (p *fakeProber) didStop() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.stopped
}

func (p *fakeProber) probeCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.probes
}

// harness assembles a session whose every seam is observable.
type harness struct {
	s      session
	prober *fakeProber
	out    bytes.Buffer
	errOut bytes.Buffer

	stdoutTTY, stdinTTY bool
	answer              string
	monitorErr          error
	uiErr               error

	monitors, uiStarts int
}

func newHarness() *harness {
	h := &harness{prober: newFakeProber(), stdoutTTY: true, stdinTTY: true}
	h.s = session{
		out:         &h.out,
		errOut:      &h.errOut,
		in:          strings.NewReader(""),
		stdoutIsTTY: func() bool { return h.stdoutTTY },
		stdinIsTTY:  func() bool { return h.stdinTTY },
		newMonitor: func(hosts []string, _, _ time.Duration) (prober, error) {
			h.monitors++
			if h.monitorErr != nil {
				return nil, h.monitorErr
			}
			h.prober.mu.Lock()
			h.prober.hosts = hosts
			h.prober.mu.Unlock()
			return h.prober, nil
		},
		// A real table stays up for as long as someone is looking at it. The
		// stand-in waits for probing to be properly under way, so that what
		// happens on the way down is not racing what happens on the way up.
		startUI: func(context.Context, ui.Model) error {
			h.uiStarts++
			select {
			case <-h.prober.started:
			case <-time.After(2 * time.Second):
			}
			return h.uiErr
		},
	}
	return h
}

func (h *harness) reply(s string) { h.s.in = strings.NewReader(s) }

func TestVersionPrintsAndOpensNothing(t *testing.T) {
	h := newHarness()
	if err := run(h.s, []string{"-v"}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := h.out.String(); !strings.Contains(got, "pingm v"+version) {
		t.Errorf("output = %q, want the version", got)
	}
	if h.monitors != 0 || h.uiStarts != 0 {
		t.Errorf("-v opened a socket (%d) or a table (%d)", h.monitors, h.uiStarts)
	}
}

func TestHelpIsNotAFailure(t *testing.T) {
	h := newHarness()
	if err := run(h.s, []string{"-h"}); err != nil {
		t.Fatalf("-h returned an error: %v", err)
	}
	if got := h.errOut.String(); !strings.Contains(got, "Usage: pingm") {
		t.Errorf("-h did not print the usage:\n%s", got)
	}
	if h.monitors != 0 {
		t.Error("-h opened a socket")
	}
}

func TestUnknownFlagIsReportedOncePointingAtHelp(t *testing.T) {
	h := newHarness()
	err := run(h.s, []string{"-nope", "10.0.0.1"})
	if err == nil {
		t.Fatal("an unknown flag was accepted")
	}
	if !strings.Contains(err.Error(), "nope") {
		t.Errorf("error %q does not name the offending flag", err)
	}
	if !strings.Contains(err.Error(), "-h") {
		t.Errorf("error %q does not point at the help", err)
	}
	// The flag package's own usage dump would be a second, louder message.
	if got := h.errOut.String(); strings.Contains(got, "Usage: pingm") {
		t.Errorf("a bad flag printed the whole usage block as well:\n%s", got)
	}
}

func TestNoHostsIsAnError(t *testing.T) {
	h := newHarness()
	err := run(h.s, nil)
	if err == nil || !strings.Contains(err.Error(), "no hosts") {
		t.Errorf("err = %v, want a complaint about missing hosts", err)
	}
	if h.monitors != 0 {
		t.Error("a hostless run opened a socket")
	}
}

func TestInvalidOptionsAreRejectedBeforeAnySocket(t *testing.T) {
	cases := map[string][]string{
		"bad filter":       {"-f", "sideways", "10.0.0.1"},
		"zero interval":    {"-i", "0s", "10.0.0.1"},
		"negative count":   {"-c", "-1", "10.0.0.1"},
		"negative timeout": {"-t", "-1s", "10.0.0.1"},
		"bad host list":    {"10.0.0.1-"},
	}
	for name, argv := range cases {
		t.Run(name, func(t *testing.T) {
			h := newHarness()
			if err := run(h.s, argv); err == nil {
				t.Fatal("run accepted it")
			}
			if h.monitors != 0 {
				t.Error("a socket was opened despite the bad options")
			}
		})
	}
}

// Drawing a full-screen table into a pipe produces nothing useful, so it is
// refused in terms the user can act on.
func TestPipedOutputIsRefusedInPlainTerms(t *testing.T) {
	h := newHarness()
	h.stdoutTTY = false

	err := run(h.s, []string{"10.0.0.1"})
	if err == nil {
		t.Fatal("run drew a table into a pipe")
	}
	if !strings.Contains(err.Error(), "needs a terminal") {
		t.Errorf("error %q does not explain the problem", err)
	}
	if h.monitors != 0 {
		t.Error("a socket was opened before the terminal check")
	}
}

func TestLargeSweepIsConfirmedBeforeAnySocket(t *testing.T) {
	h := newHarness()
	h.reply("n\n")

	err := run(h.s, []string{"10.0.0.0/24"})
	if err == nil || !strings.Contains(err.Error(), "aborted") {
		t.Fatalf("err = %v, want the sweep to be aborted", err)
	}
	if h.monitors != 0 {
		t.Error("a declined sweep still opened a socket")
	}
	if got := h.out.String(); !strings.Contains(got, "Continue?") {
		t.Errorf("no prompt was shown:\n%s", got)
	}
}

func TestLargeSweepProceedsWithYes(t *testing.T) {
	h := newHarness()
	h.reply("y\n")

	if err := run(h.s, []string{"10.0.0.0/24"}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if h.uiStarts != 1 {
		t.Errorf("the table started %d times, want 1", h.uiStarts)
	}
}

func TestAssumeYesSkipsThePrompt(t *testing.T) {
	h := newHarness()
	h.stdinTTY = false // No human to answer, so -y is the only way through.

	if err := run(h.s, []string{"-y", "10.0.0.0/24"}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := h.out.String(); strings.Contains(got, "Continue?") {
		t.Error("-y still prompted")
	}
}

func TestSocketFailureIsReportedNotDrawn(t *testing.T) {
	h := newHarness()
	h.monitorErr = errors.New("cannot open an ICMP socket")

	err := run(h.s, []string{"10.0.0.1"})
	if err == nil || !strings.Contains(err.Error(), "ICMP socket") {
		t.Fatalf("err = %v, want the socket failure", err)
	}
	if h.uiStarts != 0 {
		t.Error("the table started despite having nothing to probe with")
	}
}

// --- The CSV log ----------------------------------------------------------

// A bad path must surface as a plain error, not flash past behind the table.
func TestUnwritableLogIsReportedBeforeTheTableStarts(t *testing.T) {
	h := newHarness()
	bad := filepath.Join(t.TempDir(), "no-such-dir", "out.csv")

	err := run(h.s, []string{"-o", bad, "10.0.0.1"})
	if err == nil {
		t.Fatal("an unwritable log path was accepted")
	}
	if !strings.Contains(err.Error(), "out.csv") {
		t.Errorf("error %q does not name the file", err)
	}
	if h.uiStarts != 0 {
		t.Error("the table started before the log path was checked")
	}
	if !h.prober.closed {
		t.Error("the socket was leaked when the log could not be opened")
	}
}

func TestLogIsWrittenAndFlushedByTheTimeRunReturns(t *testing.T) {
	h := newHarness()
	path := filepath.Join(t.TempDir(), "soak.csv")

	if err := run(h.s, []string{"-o", path, "10.0.0.1"}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if h.prober.probeCount() == 0 {
		t.Fatal("the recorder was never attached; nothing was logged")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the log back: %v", err)
	}
	rows, err := csv.NewReader(bytes.NewReader(data)).ReadAll()
	if err != nil {
		t.Fatalf("the log is not valid CSV: %v\n%s", err, data)
	}
	if len(rows) < 2 {
		t.Errorf("the log has %d rows, want a header and at least one probe", len(rows))
	}
	if rows[0][0] != "timestamp" {
		t.Errorf("first row = %v, want the header", rows[0])
	}
}

func TestNoLogIsWrittenWithoutTheFlag(t *testing.T) {
	h := newHarness()
	if err := run(h.s, []string{"10.0.0.1"}); err != nil {
		t.Fatalf("run: %v", err)
	}
	h.prober.mu.Lock()
	defer h.prober.mu.Unlock()
	if h.prober.recorder != nil {
		t.Error("a recorder was attached without -o")
	}
}

// --- Shutdown -------------------------------------------------------------

// Probing must have stopped and drained before run returns, or the probe loop
// would still be writing into a log the caller is about to close.
func TestProbingIsStoppedBeforeRunReturns(t *testing.T) {
	h := newHarness()
	if err := run(h.s, []string{"10.0.0.1"}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if !h.prober.didStop() {
		t.Error("run returned while probing was still under way")
	}
	if !h.prober.closed {
		t.Error("the socket was not closed")
	}
}

// The same must hold when the table fails rather than the user quitting.
func TestProbingIsStoppedEvenWhenTheTableFails(t *testing.T) {
	h := newHarness()
	h.uiErr = errors.New("terminal exploded")

	err := run(h.s, []string{"10.0.0.1"})
	if err == nil || !strings.Contains(err.Error(), "exploded") {
		t.Fatalf("err = %v, want the table's own error", err)
	}
	if !h.prober.didStop() {
		t.Error("a failed table left probing running")
	}
	if !h.prober.closed {
		t.Error("a failed table leaked the socket")
	}
}
