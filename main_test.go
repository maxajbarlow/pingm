package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

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
