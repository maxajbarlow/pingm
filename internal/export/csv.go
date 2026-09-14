// Package export writes a durable record of probe outcomes alongside the live
// table, so a long soak can be analysed after the fact.
package export

import (
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/maxajbarlow/pingm/internal/monitor"
)

// flushInterval bounds how long a result can sit in the buffer before it
// reaches the file. Without it a table left running for an hour would show
// nothing on disk to anything tailing the file.
const flushInterval = time.Second

// CSV records every probe outcome as one row. It satisfies monitor.Recorder.
//
// The row is deliberately one probe rather than one refresh: raw outcomes can
// be re-aggregated any way later, whereas a sampled average cannot be taken
// apart again.
type CSV struct {
	mu        sync.Mutex
	w         *csv.Writer
	closer    io.Closer
	err       error // First write error; reported once at Close.
	lastFlush time.Time
}

// Create opens path for writing and emits the header row.
func Create(path string) (*CSV, error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, fmt.Errorf("cannot open %s for writing: %w", path, err)
	}
	c := New(f)
	if c.err != nil {
		_ = f.Close()
		return nil, c.err
	}
	return c, nil
}

// New writes to an already-open destination, which it closes if it can.
func New(w io.Writer) *CSV {
	c := &CSV{w: csv.NewWriter(w)}
	if closer, ok := w.(io.Closer); ok {
		c.closer = closer
	}
	c.write([]string{"timestamp", "host", "probe", "status", "rtt_ms"})
	return c
}

// Record appends one probe outcome.
func (c *CSV) Record(r monitor.Result, at time.Time, host string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	status, rtt := "down", ""
	if r.OK {
		status = "up"
		rtt = strconv.FormatFloat(float64(r.RTT)/float64(time.Millisecond), 'f', 3, 64)
	}
	c.write([]string{
		at.Format(time.RFC3339Nano),
		host,
		strconv.Itoa(r.Probe),
		status,
		rtt,
	})

	// Flushing on a timer rather than every row keeps the cost off the probe
	// path while still making the file useful to `tail -f`.
	//
	// The baseline comes from the first recorded probe, not from the wall
	// clock at construction: the two are the same clock in practice, but
	// mixing them means a clock that steps backwards stalls flushing for as
	// long as the step, and the file silently stops growing.
	if c.lastFlush.IsZero() || at.Sub(c.lastFlush) >= flushInterval {
		c.w.Flush()
		c.lastFlush = at
	}
	return c.err
}

// write records the first error and then stops trying, so a full disk
// produces one message rather than one per probe.
//
// Both the row write and the buffer are checked: csv.Writer buffers, so a
// failing file reports nothing from Write and only surfaces the error through
// Error() once something has actually been flushed.
func (c *CSV) write(row []string) {
	if c.err != nil {
		return
	}
	if err := c.w.Write(row); err != nil {
		c.err = fmt.Errorf("writing CSV: %w", err)
		return
	}
	if err := c.w.Error(); err != nil {
		c.err = fmt.Errorf("writing CSV: %w", err)
	}
}

// Close flushes and releases the file, reporting the first write error seen.
func (c *CSV) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.w.Flush()
	if err := c.w.Error(); err != nil && c.err == nil {
		c.err = fmt.Errorf("writing CSV: %w", err)
	}
	if c.closer != nil {
		if err := c.closer.Close(); err != nil && c.err == nil {
			c.err = err
		}
	}
	return c.err
}
