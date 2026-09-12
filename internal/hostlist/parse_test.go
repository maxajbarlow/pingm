package hostlist

import (
	"strings"
	"testing"
)

const max = 256

func TestParseSingleHost(t *testing.T) {
	got, err := Parse("8.8.8.8", max)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"8.8.8.8"}
	assertEqual(t, got, want)
}

func TestParseCommaSeparatedListPreservesOrder(t *testing.T) {
	got, err := Parse("8.8.8.8,1.1.1.1,google.com", max)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertEqual(t, got, []string{"8.8.8.8", "1.1.1.1", "google.com"})
}

func TestParseTrimsWhitespaceAndSkipsEmptyTokens(t *testing.T) {
	got, err := Parse("  8.8.8.8 , ,1.1.1.1  ", max)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertEqual(t, got, []string{"8.8.8.8", "1.1.1.1"})
}

func TestParseExpandsInclusiveIPRange(t *testing.T) {
	got, err := Parse("10.0.0.1-10.0.0.4", max)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertEqual(t, got, []string{"10.0.0.1", "10.0.0.2", "10.0.0.3", "10.0.0.4"})
}

func TestParseRangeCrossesOctetBoundary(t *testing.T) {
	got, err := Parse("10.0.0.254-10.0.1.1", max)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertEqual(t, got, []string{"10.0.0.254", "10.0.0.255", "10.0.1.0", "10.0.1.1"})
}

func TestParseRelativeRangeYieldsNConsecutiveAddresses(t *testing.T) {
	got, err := Parse("10.0.0.0+3", max)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertEqual(t, got, []string{"10.0.0.0", "10.0.0.1", "10.0.0.2"})
}

func TestParseCIDRCoversNetworkThroughBroadcast(t *testing.T) {
	got, err := Parse("10.0.0.0/30", max)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertEqual(t, got, []string{"10.0.0.0", "10.0.0.1", "10.0.0.2", "10.0.0.3"})
}

func TestParseCIDRMasksHostBitsFromTheGivenAddress(t *testing.T) {
	// 10.0.0.9/30 must snap down to the 10.0.0.8 network.
	got, err := Parse("10.0.0.9/30", max)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertEqual(t, got, []string{"10.0.0.8", "10.0.0.9", "10.0.0.10", "10.0.0.11"})
}

func TestParseCIDRSlash32IsASingleHost(t *testing.T) {
	got, err := Parse("10.0.0.7/32", max)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertEqual(t, got, []string{"10.0.0.7"})
}

func TestParseCIDRSlash31YieldsBothPointToPointAddresses(t *testing.T) {
	got, err := Parse("10.0.0.4/31", max)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertEqual(t, got, []string{"10.0.0.4", "10.0.0.5"})
}

func TestParseTreatsHyphenatedHostnameAsOneHost(t *testing.T) {
	got, err := Parse("my-server-1.example.com", max)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertEqual(t, got, []string{"my-server-1.example.com"})
}

func TestParseMixesHostnamesRangesAndSubnets(t *testing.T) {
	got, err := Parse("google.com,10.0.0.1-10.0.0.2,10.1.1.0/31", max)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertEqual(t, got, []string{"google.com", "10.0.0.1", "10.0.0.2", "10.1.1.0", "10.1.1.1"})
}

func TestParseRejectsEmptySpecification(t *testing.T) {
	assertErrorContains(t, "", max, "no hosts")
}

func TestParseRejectsReversedRange(t *testing.T) {
	assertErrorContains(t, "10.0.0.10-10.0.0.1", max, "after end")
}

func TestParseRejectsRangeLargerThanLimit(t *testing.T) {
	assertErrorContains(t, "10.0.0.0-10.0.4.0", max, "max 256")
}

func TestParseRejectsTotalLargerThanLimit(t *testing.T) {
	assertErrorContains(t, "10.0.0.0/24,10.0.1.1", max, "maximum is 256")
}

func TestParseRejectsCIDRPrefixAbove32(t *testing.T) {
	assertErrorContains(t, "10.0.0.0/33", max, "invalid CIDR")
}

func TestParseRejectsCIDRWithNonNumericPrefix(t *testing.T) {
	assertErrorContains(t, "10.0.0.0/ab", max, "invalid CIDR")
}

func TestParseRejectsCIDROnAHostname(t *testing.T) {
	assertErrorContains(t, "example.com/24", max, "invalid CIDR")
}

func TestParseRejectsZeroLengthRelativeRange(t *testing.T) {
	assertErrorContains(t, "10.0.0.0+0", max, "invalid range")
}

func TestParseRejectsRelativeRangeOnAHostname(t *testing.T) {
	assertErrorContains(t, "example.com+5", max, "invalid range")
}

func TestParseRejectsMalformedOctet(t *testing.T) {
	// The shell version accepted 999.1.1.1 because it only pattern-matched
	// digits and dots; a malformed address should be a clear error instead.
	assertErrorContains(t, "999.1.1.1-999.1.1.2", max, "invalid")
}

func TestParseRelativeRangeDoesNotWrapPastBroadcast(t *testing.T) {
	assertErrorContains(t, "255.255.255.254+4", max, "past 255.255.255.255")
}

func assertEqual(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d hosts %v, want %d %v", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("host %d: got %q, want %q", i, got[i], want[i])
		}
	}
}

func assertErrorContains(t *testing.T, spec string, limit int, want string) {
	t.Helper()
	_, err := Parse(spec, limit)
	if err == nil {
		t.Fatalf("Parse(%q) succeeded, want error containing %q", spec, want)
	}
	if !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(want)) {
		t.Errorf("Parse(%q) error = %q, want it to contain %q", spec, err, want)
	}
}

// A /0 must be rejected by the size guard, not enumerated. The count is held
// in int64 because int(0xFFFFFFFF)+1 wraps to 0 on a 32-bit build.
func TestParseRejectsEntireAddressSpace(t *testing.T) {
	assertErrorContains(t, "0.0.0.0/0", max, "4294967296 hosts")
}

func TestParseRejectsVeryLargeCIDR(t *testing.T) {
	assertErrorContains(t, "10.0.0.0/1", max, "max 256")
}
