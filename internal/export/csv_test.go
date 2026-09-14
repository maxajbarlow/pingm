package export

import (
	"encoding/csv"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/maxajbarlow/pingm/internal/monitor"
)

// nopCloser lets a buffer stand in for a file without being closed twice.
type sink struct{ b *strings.Builder }

func (s sink) Write(p []byte) (int, error) { return s.b.Write(p) }

func at(sec int) time.Time {
	return time.Date(2026, 1, 1, 12, 0, sec, 0, time.UTC)
}

func rows(t *testing.T, text string) [][]string {
	t.Helper()
	recs, err := csv.NewReader(strings.NewReader(text)).ReadAll()
	if err != nil {
		t.Fatalf("output is not valid CSV: %v\n%s", err, text)
	}
	return recs
}

func TestHeaderIsWrittenFirst(t *testing.T) {
	var b strings.Builder
	c := New(sink{&b})
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	recs := rows(t, b.String())
	if len(recs) != 1 {
		t.Fatalf("got %d rows for an empty log, want just the header", len(recs))
	}
	want := []string{"timestamp", "host", "probe", "status", "rtt_ms"}
	for i, col := range want {
		if recs[0][i] != col {
			t.Errorf("header column %d = %q, want %q", i, recs[0][i], col)
		}
	}
}

func TestReplyAndLossAreBothRecorded(t *testing.T) {
	var b strings.Builder
	c := New(sink{&b})
	_ = c.Record(monitor.Result{Index: 0, Probe: 1, OK: true, RTT: 12300 * time.Microsecond}, at(0), "10.0.0.1")
	_ = c.Record(monitor.Result{Index: 0, Probe: 2, OK: false}, at(1), "10.0.0.1")
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	recs := rows(t, b.String())
	if len(recs) != 3 {
		t.Fatalf("got %d rows, want a header and two probes", len(recs))
	}

	reply := recs[1]
	if reply[1] != "10.0.0.1" || reply[2] != "1" || reply[3] != "up" || reply[4] != "12.300" {
		t.Errorf("reply row = %v, want the host, probe 1, up and 12.300", reply)
	}
	// A loss has no round trip, so the column must be empty rather than zero:
	// a zero would average in as an impossibly fast reply.
	loss := recs[2]
	if loss[3] != "down" || loss[4] != "" {
		t.Errorf("loss row = %v, want status down and an empty rtt", loss)
	}
}

func TestTimestampsAreParseable(t *testing.T) {
	var b strings.Builder
	c := New(sink{&b})
	_ = c.Record(monitor.Result{Probe: 1, OK: true, RTT: time.Millisecond}, at(42), "h")
	_ = c.Close()

	recs := rows(t, b.String())
	got, err := time.Parse(time.RFC3339Nano, recs[1][0])
	if err != nil {
		t.Fatalf("timestamp %q does not parse: %v", recs[1][0], err)
	}
	if !got.Equal(at(42)) {
		t.Errorf("timestamp = %v, want %v", got, at(42))
	}
}

func TestCreateWritesToTheNamedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "soak.csv")
	c, err := Create(path)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	_ = c.Record(monitor.Result{Probe: 1, OK: true, RTT: time.Millisecond}, at(0), "h")
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading back: %v", err)
	}
	if recs := rows(t, string(data)); len(recs) != 2 {
		t.Errorf("file has %d rows, want a header and one probe", len(recs))
	}
}

func TestCreateReportsABadPath(t *testing.T) {
	_, err := Create(filepath.Join(t.TempDir(), "no-such-dir", "out.csv"))
	if err == nil {
		t.Fatal("Create succeeded on an unwritable path")
	}
	if !strings.Contains(err.Error(), "out.csv") {
		t.Errorf("error %q does not name the file the user asked for", err)
	}
}

// failingWriter is a destination that cannot be written to at all, standing in
// for a full disk or a revoked permission.
type failingWriter struct{ writes int }

func (w *failingWriter) Write(p []byte) (int, error) {
	w.writes++
	return 0, errors.New("disk full")
}

// The first write error is kept and surfaced once, rather than one message per
// probe for the rest of the run.
func TestWriteErrorSurfacesOnceAtClose(t *testing.T) {
	c := New(&failingWriter{})
	for i := 0; i < 50; i++ {
		_ = c.Record(monitor.Result{Probe: i, OK: false}, at(i), "h")
	}

	err := c.Close()
	if err == nil {
		t.Fatal("Close reported success after the writer failed")
	}
	if !strings.Contains(err.Error(), "disk full") {
		t.Errorf("error %q does not carry the underlying cause", err)
	}
}

// Closing must flush whatever is still buffered, or a short run writes nothing.
func TestCloseFlushesBufferedRows(t *testing.T) {
	var b strings.Builder
	c := New(sink{&b})
	_ = c.Record(monitor.Result{Probe: 1, OK: true, RTT: time.Millisecond}, at(0), "h")

	if strings.Count(b.String(), "\n") > 1 {
		t.Skip("the writer flushed on its own; nothing to prove here")
	}
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if recs := rows(t, b.String()); len(recs) != 2 {
		t.Errorf("got %d rows after Close, want the buffered probe flushed", len(recs))
	}
}
