//go:build unix

// End-to-end tests that run the real binary against a real terminal.
//
// Everything else in this package tests run() with its seams filled by fakes,
// which is where the decisions are. What that cannot reach is the wiring in
// realSession and startTable: whether a terminal is detected, whether Bubble
// Tea gets a usable size, and — the reason this file exists — whether mouse
// wheel events actually arrive from a terminal and move the table. None of
// that can be asserted without a terminal on the other end of the pipe, so
// these allocate a pty and drive the binary through it.
//
// What they cover does not show up in `go test -cover`, because it happens in
// a child process the test binary cannot see into. To measure it:
//
//	go build -cover -coverpkg=github.com/maxajbarlow/pingm -o /tmp/pingm-cov .
//	mkdir -p /tmp/covdata
//	PINGM_TEST_BINARY=/tmp/pingm-cov GOCOVERDIR=/tmp/covdata go test -run RealTerminal .
//	go tool covdata func -i=/tmp/covdata
package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/creack/pty"
)

const (
	// The table needs room to be worth scrolling: 20 rows leaves 12 for hosts
	// once the header, footer and scroll indicator have taken theirs.
	ptyCols, ptyRows = 110, 20

	// Generous, because this waits on a real process doing real ICMP.
	ptyTimeout = 15 * time.Second

	// hostRange is 60 loopback addresses: enough to overflow the window, and
	// no traffic leaves the machine.
	hostRange = "127.0.0.1-127.0.0.60"
	lastHost  = "127.0.0.60"
)

var (
	buildOnce  sync.Once
	binaryPath string
	buildErr   error
)

// binary builds the command under test once per run and returns its path.
//
// PINGM_TEST_BINARY overrides the build, so these tests can be pointed at a
// coverage-instrumented binary: what they exercise runs in a child process,
// which `go test -cover` cannot see into on its own.
func binary(t *testing.T) string {
	t.Helper()
	if path := os.Getenv("PINGM_TEST_BINARY"); path != "" {
		return path
	}
	buildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "pingm-pty")
		if err != nil {
			buildErr = err
			return
		}
		binaryPath = filepath.Join(dir, "pingm")
		out, err := exec.Command("go", "build", "-o", binaryPath, ".").CombinedOutput()
		if err != nil {
			buildErr = errors.New(string(out))
		}
	})
	if buildErr != nil {
		t.Fatalf("building the binary: %v", buildErr)
	}
	return binaryPath
}

// ansi matches CSI and OSC escape sequences, which have to come out before the
// output can be matched as text.
var ansi = regexp.MustCompile(`\x1b\[[0-9;?<>=]*[A-Za-z]|\x1b\][^\x07\x1b]*(\x07|\x1b\\)|\x1b[()][A-Za-z0-9]|\x1b[=><]`)

// terminal is a running pingm with a pty on the other end of its streams.
type terminal struct {
	t    *testing.T
	cmd  *exec.Cmd
	file *os.File

	mu   sync.Mutex
	seen strings.Builder
	done chan error

	answeredBackground bool
	answeredCursor     bool
}

// start launches the binary attached to a pty of a known size.
func start(t *testing.T, args ...string) *terminal {
	t.Helper()

	cmd := exec.Command(binary(t), args...)
	f, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: ptyCols, Rows: ptyRows})
	if err != nil {
		t.Skipf("cannot allocate a pty here: %v", err)
	}

	term := &terminal{t: t, cmd: cmd, file: f, done: make(chan error, 1)}
	go term.drain()
	t.Cleanup(term.stop)
	return term
}

// Queries a real terminal answers, and the replies this one gives.
//
// Bubble Tea v1 asks for the background colour from its own package init, to
// get the answer before a Program seizes the terminal. termenv then blocks for
// termenv.OSCTimeout — five seconds — waiting for a reply. A real terminal
// answers in microseconds; a bare pty with nothing behind it never answers at
// all, so without this every test here would cost five seconds before the
// program reached main.
//
// The cursor-position report is part of the same exchange: termenv sends it
// alongside the colour query as a sentinel, because a terminal that does not
// understand OSC 11 will still answer CSI 6n. Both have to come back.
const (
	queryBackground = "\x1b]11;?"
	queryCursor     = "\x1b[6n"

	replyBackground = "\x1b]11;rgb:1c1c/1c1c/1c1c\x1b\\"
	replyCursor     = "\x1b[1;1R"
)

// drain copies the pty to the buffer until the process exits, answering
// terminal queries as they appear. The read has to keep running for the whole
// test: a full pty buffer would block the program mid-frame and deadlock
// everything waiting on it.
func (term *terminal) drain() {
	buf := make([]byte, 4096)
	for {
		n, err := term.file.Read(buf)
		if n > 0 {
			term.mu.Lock()
			term.seen.Write(buf[:n])
			term.mu.Unlock()
			term.answerQueries()
		}
		if err != nil {
			break
		}
	}
	term.done <- term.cmd.Wait()
}

// answerQueries replies to anything the program has asked the terminal, once
// each. It reads the whole buffer rather than the latest chunk so a query
// split across two reads is still recognised.
func (term *terminal) answerQueries() {
	term.mu.Lock()
	seen := term.seen.String()
	background, cursor := term.answeredBackground, term.answeredCursor
	term.mu.Unlock()

	var reply string
	if !background && strings.Contains(seen, queryBackground) {
		reply += replyBackground
		background = true
	}
	if !cursor && strings.Contains(seen, queryCursor) {
		reply += replyCursor
		cursor = true
	}
	if reply == "" {
		return
	}

	term.mu.Lock()
	term.answeredBackground, term.answeredCursor = background, cursor
	term.mu.Unlock()
	_, _ = term.file.WriteString(reply)
}

// text is everything drawn since the last reset, with escapes stripped.
func (term *terminal) text() string {
	term.mu.Lock()
	defer term.mu.Unlock()
	return ansi.ReplaceAllString(term.seen.String(), "")
}

// reset forgets what has been drawn, so a later wait cannot be satisfied by a
// frame from before the action under test.
func (term *terminal) reset() {
	term.mu.Lock()
	defer term.mu.Unlock()
	term.seen.Reset()
}

func (term *terminal) send(s string) {
	term.t.Helper()
	if _, err := term.file.WriteString(s); err != nil {
		term.t.Fatalf("writing %q to the terminal: %v", s, err)
	}
}

// wheel sends one notch as an SGR mouse report. Bit 6 marks a wheel event, so
// 64 is up and 65 is down; the coordinates are arbitrary but must be on screen.
func (term *terminal) wheel(down bool, notches int) {
	button := 64
	if down {
		button = 65
	}
	for i := 0; i < notches; i++ {
		term.send("\x1b[<" + itoa(button) + ";10;10M")
		time.Sleep(20 * time.Millisecond)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// waitFor blocks until want has been drawn, or fails the test. A process that
// has already exited is reported as such rather than timing out slowly.
func (term *terminal) waitFor(want string) {
	term.t.Helper()
	deadline := time.After(ptyTimeout)
	for {
		if strings.Contains(term.text(), want) {
			return
		}
		select {
		case err := <-term.done:
			term.done <- err
			// The process may well have drawn what was wanted on its way out,
			// so the exit is only a failure once the final output has been
			// read and still does not contain it.
			if strings.Contains(term.text(), want) {
				return
			}
			term.t.Fatalf("the process exited (%v) before drawing %q:\n%s", err, want, term.text())
		case <-deadline:
			term.t.Fatalf("timed out waiting for %q:\n%s", want, term.text())
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// waitForExit returns the process's exit error, if any.
func (term *terminal) waitForExit() error {
	term.t.Helper()
	select {
	case err := <-term.done:
		return err
	case <-time.After(ptyTimeout):
		term.t.Fatalf("the process did not exit:\n%s", term.text())
		return nil
	}
}

func (term *terminal) stop() {
	if term.cmd.Process != nil {
		_ = term.cmd.Process.Kill()
	}
	_ = term.file.Close()
}

// skipWithoutICMP bails out where the machine will not grant a probe socket,
// which is normal on a locked-down CI runner.
func (term *terminal) skipWithoutICMP() {
	term.t.Helper()
	if strings.Contains(term.text(), "cannot open an ICMP socket") {
		term.t.Skip("no ICMP socket available here")
	}
}

func TestTableDrawsOnARealTerminal(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs the binary")
	}
	term := start(t, "-y", "-i", "200ms", hostRange)

	term.waitFor("HOST")
	term.skipWithoutICMP()

	for _, want := range []string{"pingm", "STATUS", "LATENCY", "127.0.0.1", "Interval: 200ms"} {
		if !strings.Contains(term.text(), want) {
			t.Errorf("the table is missing %q:\n%s", want, term.text())
		}
	}
	// Sixty hosts in a twenty-row window: the rest must be reported as being
	// off screen rather than silently dropped.
	if !strings.Contains(term.text(), "below") {
		t.Errorf("no scroll indicator for a table that overflows:\n%s", term.text())
	}
}

// The point of the exercise: wheel events have to survive the terminal, the
// pty and Bubble Tea's parser, and actually move the table.
func TestMouseWheelScrollsOnARealTerminal(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs the binary")
	}
	term := start(t, "-y", "-i", "200ms", hostRange)

	term.waitFor("127.0.0.1")
	term.skipWithoutICMP()
	if strings.Contains(term.text(), lastHost) {
		t.Fatalf("%s was visible before scrolling; the window is too tall to test with", lastHost)
	}

	// Far enough to reach the bottom from anywhere in a sixty-row list.
	term.reset()
	term.wheel(true, 25)

	// The last host can only be on screen if the wheel moved the viewport.
	term.waitFor(lastHost)
	if !strings.Contains(term.text(), "above") {
		t.Errorf("no indicator for the rows scrolled past:\n%s", term.text())
	}

	term.reset()
	term.wheel(false, 25)
	term.waitFor("127.0.0.1")
}

func TestKeyboardScrollingOnARealTerminal(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs the binary")
	}
	term := start(t, "-y", "-i", "200ms", hostRange)

	term.waitFor("127.0.0.1")
	term.skipWithoutICMP()

	term.reset()
	term.send("G") // Jump to the bottom.
	term.waitFor(lastHost)

	term.reset()
	term.send("g") // And back to the top.
	term.waitFor("127.0.0.1")
}

func TestHelpOverlayOnARealTerminal(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs the binary")
	}
	term := start(t, "-y", "-i", "200ms", hostRange)

	term.waitFor("HOST")
	term.skipWithoutICMP()

	term.reset()
	term.send("?")
	term.waitFor("press any key to go back")

	term.reset()
	term.send("x")
	term.waitFor("STATUS")
}

func TestQuitKeyExitsCleanly(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs the binary")
	}
	term := start(t, "-y", "-i", "200ms", hostRange)

	term.waitFor("HOST")
	term.skipWithoutICMP()

	term.send("q")
	if err := term.waitForExit(); err != nil {
		t.Errorf("quitting was not clean: %v\n%s", err, term.text())
	}
}

// The log has to be flushed on the way out, which only the real shutdown path
// does — the alt screen is already gone by the time it happens.
func TestLogSurvivesQuittingOnARealTerminal(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs the binary")
	}
	path := filepath.Join(t.TempDir(), "soak.csv")
	term := start(t, "-y", "-i", "200ms", "-o", path, "127.0.0.1")

	term.waitFor("HOST")
	term.skipWithoutICMP()
	term.waitFor("UP")

	term.send("q")
	if err := term.waitForExit(); err != nil {
		t.Fatalf("quitting was not clean: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the log back: %v", err)
	}
	lines := strings.Count(strings.TrimSpace(string(data)), "\n") + 1
	if lines < 2 {
		t.Errorf("the log has %d lines, want a header and at least one probe:\n%s", lines, data)
	}
	if !strings.HasPrefix(string(data), "timestamp,host,probe,status,rtt_ms") {
		t.Errorf("the log is missing its header:\n%s", data)
	}
}

func TestVersionOnARealTerminal(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs the binary")
	}
	term := start(t, "-v")

	term.waitFor("pingm v" + version)
	if err := term.waitForExit(); err != nil {
		t.Errorf("-v exited with %v", err)
	}
}

// Without a pty there is nowhere to draw, and the refusal has to be a plain
// message on stderr rather than a terminal library's own complaint.
func TestPipedOutputIsRefusedByTheRealBinary(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs the binary")
	}
	out, err := exec.Command(binary(t), "127.0.0.1").CombinedOutput()

	if err == nil {
		t.Fatal("the binary drew a table into a pipe")
	}
	if !strings.Contains(string(out), "needs a terminal") {
		t.Errorf("output %q does not explain the problem", out)
	}
}
