// Command pingm shows a live table of ICMP reachability for many hosts.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"golang.org/x/term"

	"github.com/maxajbarlow/pingm/internal/export"
	"github.com/maxajbarlow/pingm/internal/hostlist"
	"github.com/maxajbarlow/pingm/internal/monitor"
	"github.com/maxajbarlow/pingm/internal/ui"
)

const (
	version = "2.1.1"

	// maxHosts is far above the shell version's 256 because probing no longer
	// costs a process per host — the whole list shares one socket.
	maxHosts = 1024

	// confirmThreshold is where a sweep starts to look like a subnet scan to
	// network monitoring, so it asks first.
	confirmThreshold = 50
)

type options struct {
	interval  time.Duration
	timeout   time.Duration
	count     int
	filter    string
	output    string
	assumeYes bool
	showVer   bool
}

// validate checks the flag combination and resolves the effective probe
// timeout, which defaults to the interval so a probe has the whole cycle to be
// answered — capped so a long interval cannot leave a dead host looking merely
// slow for minutes.
func (o options) validate() (ui.Filter, time.Duration, error) {
	filter, err := ui.ParseFilter(o.filter)
	if err != nil {
		return 0, 0, err
	}
	if o.interval <= 0 {
		return 0, 0, errors.New("interval must be positive")
	}
	if o.count < 0 {
		return 0, 0, errors.New("count cannot be negative")
	}
	if o.timeout < 0 {
		return 0, 0, errors.New("timeout cannot be negative")
	}

	// Zero is passed straight through: the monitor then derives the timeout
	// from the interval and keeps the two in step as the interval changes.
	return filter, o.timeout, nil
}

// prober is the part of the monitor that run drives. An interface rather than
// *monitor.Monitor so the setup path can be exercised without opening an ICMP
// socket, which needs privileges a test machine may not grant.
type prober interface {
	ui.Controller
	Run(ctx context.Context, limit int)
	SetRecorder(monitor.Recorder)
	Close() error
}

// session is everything run needs from outside itself: where to read and
// write, how to tell whether there is a terminal, how to open a socket, and
// how to put a table on screen.
//
// Every field is a seam. The interesting decisions in run — refusing to draw
// into a pipe, opening the CSV log before the alt screen swallows any error,
// stopping probing before the log is closed underneath it — all happen around
// calls the process cannot make during a test, so they are injected rather
// than reached for directly.
type session struct {
	out    io.Writer
	errOut io.Writer
	in     io.Reader

	stdoutIsTTY func() bool
	stdinIsTTY  func() bool

	newMonitor func(hosts []string, interval, timeout time.Duration) (prober, error)
	startUI    func(ctx context.Context, m ui.Model) error
}

// realSession wires run to the actual process: real streams, real terminals,
// a real ICMP socket and a real Bubble Tea program.
func realSession() session {
	return session{
		out:    os.Stdout,
		errOut: os.Stderr,
		in:     os.Stdin,

		stdoutIsTTY: func() bool { return term.IsTerminal(int(os.Stdout.Fd())) },

		// Checking for a character device is not enough: /dev/null is one, so
		// `pingm 10.0.0.0/24 < /dev/null` would prompt into the void, read
		// EOF, and abort with a misleading message instead of naming -y.
		stdinIsTTY: func() bool { return term.IsTerminal(int(os.Stdin.Fd())) },

		newMonitor: func(hosts []string, interval, timeout time.Duration) (prober, error) {
			// Assigned in two steps rather than returned directly: a nil
			// *Monitor returned alongside an error would arrive at the caller
			// as a non-nil interface holding a nil pointer.
			m, err := monitor.New(hosts, interval, timeout, nil)
			if err != nil {
				return nil, err
			}
			return m, nil
		},

		startUI: startTable,
	}
}

// startTable runs the interactive table until the user quits.
//
// Mouse tracking is what makes the wheel scroll the table. It costs the
// terminal's own click-to-select, which most terminals hand back if you hold
// Shift (Option on macOS Terminal and iTerm).
func startTable(ctx context.Context, m ui.Model) error {
	program := tea.NewProgram(m,
		tea.WithAltScreen(),
		tea.WithMouseCellMotion(),
		tea.WithContext(ctx))

	if _, err := program.Run(); err != nil && !errors.Is(err, tea.ErrProgramKilled) {
		return err
	}
	return nil
}

func main() {
	if err := run(realSession(), os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
}

func run(s session, argv []string) error {
	opts, args, err := parseFlags(argv, s.errOut)
	if err != nil {
		// -h has already printed the help; asking for it is not a failure.
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}

	if opts.showVer {
		fmt.Fprintf(s.out, "pingm v%s\n", version)
		return nil
	}
	if len(args) < 1 {
		return errors.New("no hosts specified. Run 'pingm -h' for help")
	}

	filter, timeout, err := opts.validate()
	if err != nil {
		return err
	}

	// The table is a full-screen TUI, so it needs somewhere to draw. Say so
	// plainly rather than letting the terminal library's own error surface.
	if !s.stdoutIsTTY() {
		return errors.New("pingm draws an interactive table and needs a terminal; its output cannot be piped or redirected")
	}

	hosts, err := hostlist.Parse(strings.Join(args, ","), maxHosts)
	if err != nil {
		return err
	}
	if err := confirm(len(hosts), opts.assumeYes, s.stdinIsTTY(), s.in, s.out); err != nil {
		return err
	}

	mon, err := s.newMonitor(hosts, opts.interval, timeout)
	if err != nil {
		return err
	}
	defer func() { _ = mon.Close() }() // Nothing useful to do with a close error here.

	// The log is opened before the alt screen takes over, so a bad path is
	// reported as a plain error rather than flashing past behind the table.
	if opts.output != "" {
		log, err := export.Create(opts.output)
		if err != nil {
			return err
		}
		mon.SetRecorder(log)
		// Unlike the socket, a failed close here means probe outcomes were
		// lost, so it is always worth surfacing — the table has been torn
		// down by this point, so the message is the only sign of it.
		defer func() {
			if cerr := log.Close(); cerr != nil {
				fmt.Fprintln(s.errOut, "Error:", cerr)
			}
		}()
	}

	return table(s, mon, opts, filter)
}

// table probes and draws until the user quits, the round limit is reached, or
// a signal arrives.
//
// Shutdown is deferred rather than written out after the UI returns, so that
// an error from the UI still stops probing and drains every outstanding result
// first. Returning early instead would leave the probe loop running while the
// caller closes the log it is writing to.
func table(s session, mon prober, opts options, filter ui.Filter) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)

	finished := make(chan struct{})
	go func() {
		mon.Run(ctx, opts.count)
		close(finished)
	}()
	defer func() {
		stop()
		<-finished
	}()

	return s.startUI(ctx, ui.NewModel(mon, filter, opts.interval, opts.count, finished))
}

// parseFlags reads the command line into options.
//
// It builds its own FlagSet rather than using the package-level one, which
// keeps the global flag state out of it: the default set can only be parsed
// once per process, which would make this function untestable and leave every
// decision it feeds impossible to exercise.
func parseFlags(argv []string, errOut io.Writer) (options, []string, error) {
	var o options
	fs := flag.NewFlagSet("pingm", flag.ContinueOnError)

	// The flag package would otherwise print its own terse error followed by
	// the whole usage block; this prints one message, and the custom help only
	// when it is actually asked for.
	fs.SetOutput(io.Discard)
	fs.Usage = func() {}

	fs.DurationVar(&o.interval, "i", time.Second, "interval between probes per host")
	fs.IntVar(&o.count, "c", 0, "stop after this many probes per host (0 = unlimited)")
	fs.StringVar(&o.filter, "f", "all", "only show hosts in this state: up, down, or all")
	fs.DurationVar(&o.timeout, "t", 0, "how long to wait for a reply (default: the interval, capped at 2s)")
	fs.StringVar(&o.output, "o", "", "also append every probe outcome to this file as CSV")
	fs.BoolVar(&o.assumeYes, "y", false, "skip the confirmation prompt for large host counts")
	fs.BoolVar(&o.showVer, "v", false, "show version")

	if err := fs.Parse(argv); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			usage(errOut)
			return o, nil, err
		}
		return o, nil, fmt.Errorf("%w — run 'pingm -h' for help", err)
	}
	return o, fs.Args(), nil
}

// confirm guards against accidentally lighting up a whole subnet, which looks
// like a scan to anything watching the network.
func confirm(count int, assumeYes, interactive bool, in io.Reader, out io.Writer) error {
	if count < confirmThreshold || assumeYes {
		return nil
	}
	if !interactive {
		return fmt.Errorf("about to ping %d hosts at once — re-run with -y to skip this check (no terminal to confirm on)", count)
	}

	fmt.Fprintf(out, "About to ping %d hosts simultaneously. Continue? [y/N] ", count)
	answer, _ := bufio.NewReader(in).ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(answer)) {
	case "y", "yes":
		return nil
	default:
		return errors.New("aborted")
	}
}

func usage(w io.Writer) {
	fmt.Fprintf(w, `pingm v%s — a live table of ICMP reachability for many hosts

Usage: pingm [options] <hosts>

Hosts is a comma-separated list of any mix of:
  8.8.8.8                 an address
  google.com              a hostname
  10.0.0.1-10.0.0.20      an inclusive range
  10.0.0.0+16             a relative range, starting at 10.0.0.0
  10.0.0.0/24             a CIDR subnet, network through broadcast

Options:
  -i DURATION   interval between probes per host (default 1s)
  -c N          stop after N probes per host (default: unlimited)
  -f STATE      only show hosts in STATE: up (or online), down (or
                offline), or all (default). Every host is still probed;
                the filter only changes what the table displays.
  -t DURATION   how long to wait for a reply (default: the interval,
                capped at 2s)
  -o FILE       also write every probe outcome to FILE as CSV
  -y            skip the confirmation prompt for large host counts
  -v            show version
  -h            show this help

Keys while running:
  a / u / d     show all hosts, only up, or only down
  wheel         scroll the table (arrows, j/k, PgUp/PgDn, g/G too)
  + / -         probe slower or faster, stepping 100ms .. 10s
  p             pause the display; probing carries on underneath
  b             ring the bell when a host changes state
  ?             show every key
  q             quit

Examples:
  pingm 192.168.0.1,192.168.0.40
  pingm -f down 10.0.0.0/24
  pingm -i 250ms 8.8.8.8,1.1.1.1
  pingm -c 5 google.com
  pingm -o soak.csv -i 5s 10.0.0.0/24

Pinging %d or more hosts at once asks for confirmation first; -y skips it.
`, version, confirmThreshold)
}
