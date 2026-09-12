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

	"github.com/maxajbarlow/pingm/internal/hostlist"
	"github.com/maxajbarlow/pingm/internal/monitor"
	"github.com/maxajbarlow/pingm/internal/ui"
)

const (
	version = "2.0.0"

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

	timeout := o.timeout
	if timeout == 0 {
		timeout = min(o.interval, 2*time.Second)
	}
	return filter, timeout, nil
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
}

func run() error {
	opts, args := parseFlags()

	if opts.showVer {
		fmt.Printf("pingm v%s\n", version)
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
	if !term.IsTerminal(int(os.Stdout.Fd())) {
		return errors.New("pingm draws an interactive table and needs a terminal; its output cannot be piped or redirected")
	}

	hosts, err := hostlist.Parse(strings.Join(args, ","), maxHosts)
	if err != nil {
		return err
	}
	if err := confirmLargeSweep(len(hosts), opts.assumeYes); err != nil {
		return err
	}

	mon, err := monitor.New(hosts, opts.interval, timeout, nil)
	if err != nil {
		return err
	}
	defer func() { _ = mon.Close() }() // Nothing useful to do with a close error here.

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	finished := make(chan struct{})
	go func() {
		mon.Run(ctx, opts.count)
		close(finished)
	}()

	model := ui.NewModel(mon, filter, opts.interval, opts.count, finished)
	program := tea.NewProgram(model, tea.WithAltScreen(), tea.WithContext(ctx))
	if _, err := program.Run(); err != nil && !errors.Is(err, tea.ErrProgramKilled) {
		return err
	}

	stop()
	<-finished
	return nil
}

func parseFlags() (options, []string) {
	var o options
	flag.DurationVar(&o.interval, "i", time.Second, "interval between probes per host")
	flag.IntVar(&o.count, "c", 0, "stop after this many probes per host (0 = unlimited)")
	flag.StringVar(&o.filter, "f", "all", "only show hosts in this state: up, down, or all")
	flag.DurationVar(&o.timeout, "t", 0, "how long to wait for a reply (default: the interval, capped at 2s)")
	flag.BoolVar(&o.assumeYes, "y", false, "skip the confirmation prompt for large host counts")
	flag.BoolVar(&o.showVer, "v", false, "show version")
	flag.Usage = usage
	flag.Parse()
	return o, flag.Args()
}

// confirmLargeSweep guards against accidentally lighting up a whole subnet,
// which looks like a scan to anything watching the network.
func confirmLargeSweep(count int, assumeYes bool) error {
	return confirm(count, assumeYes, stdinIsTerminal(), os.Stdin, os.Stdout)
}

// stdinIsTerminal reports whether there is a human to answer a prompt.
//
// Checking for a character device is not enough: /dev/null is one, so
// `pingm 10.0.0.0/24 < /dev/null` would prompt into the void, read EOF, and
// abort with a misleading message instead of naming -y.
func stdinIsTerminal() bool {
	return term.IsTerminal(int(os.Stdin.Fd()))
}

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

func usage() {
	fmt.Fprintf(os.Stderr, `pingm v%s — a live table of ICMP reachability for many hosts

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
  -y            skip the confirmation prompt for large host counts
  -v            show version
  -h            show this help

Keys while running:
  a / u / d     show all hosts, only up, or only down
  q             quit

Examples:
  pingm 192.168.0.1,192.168.0.40
  pingm -f down 10.0.0.0/24
  pingm -i 250ms 8.8.8.8,1.1.1.1
  pingm -c 5 google.com

Pinging %d or more hosts at once asks for confirmation first; -y skips it.
`, version, confirmThreshold)
}
