// Package hostlist expands a pingm host specification into concrete targets.
//
// A specification is a comma-separated list of tokens, each of which is a
// hostname, a literal address, an inclusive address range (10.0.0.1-10.0.0.9),
// a relative range (10.0.0.0+10), or a CIDR subnet (10.0.0.0/24).
package hostlist

import (
	"fmt"
	"net/netip"
	"strconv"
	"strings"
)

// Parse expands spec into an ordered list of targets, rejecting anything that
// would exceed limit. Targets are returned as written so the table can label
// rows with what the user typed; resolution happens later.
func Parse(spec string, limit int) ([]string, error) {
	var hosts []string

	for _, token := range strings.Split(spec, ",") {
		token = strings.TrimSpace(token)
		if token == "" {
			continue
		}

		expanded, err := expandToken(token, limit)
		if err != nil {
			return nil, err
		}
		hosts = append(hosts, expanded...)

		if len(hosts) > limit {
			return nil, fmt.Errorf("too many hosts (%d). Maximum is %d", len(hosts), limit)
		}
	}

	if len(hosts) == 0 {
		return nil, fmt.Errorf("no hosts specified")
	}
	return hosts, nil
}

// expandToken decides which notation a token uses. The order matters and
// mirrors the original shell implementation: a '/' means CIDR, then '+' means
// a relative range, and only then is '-' considered — because a hostname may
// legitimately contain hyphens.
func expandToken(token string, limit int) ([]string, error) {
	switch {
	case strings.Contains(token, "/"):
		return expandCIDR(token, limit)
	case strings.Contains(token, "+"):
		return expandRelative(token, limit)
	case strings.Contains(token, "-"):
		return expandHyphenated(token, limit)
	default:
		return []string{token}, nil
	}
}

func expandCIDR(token string, limit int) ([]string, error) {
	malformed := fmt.Errorf("invalid CIDR notation %q (expected e.g. 10.0.0.0/24)", token)

	addrPart, prefixPart, _ := strings.Cut(token, "/")
	addr, err := parseIPv4(addrPart)
	if err != nil {
		return nil, malformed
	}
	bits, err := strconv.Atoi(prefixPart)
	if err != nil || bits < 0 || bits > 32 {
		return nil, malformed
	}

	// Mask the host bits off so 10.0.0.9/30 snaps down to its 10.0.0.8
	// network, then run to the broadcast address. /32 collapses to a single
	// host and /31 yields its two point-to-point addresses with no special
	// casing — the mask arithmetic already handles both.
	mask := ^uint32(0) << (32 - bits)
	if bits == 0 {
		mask = 0
	}
	network := toUint32(addr) & mask
	broadcast := network | ^mask

	return expandRange(network, broadcast, limit)
}

func expandRelative(token string, limit int) ([]string, error) {
	malformed := fmt.Errorf("invalid range notation %q (expected e.g. 10.0.0.0+10)", token)

	addrPart, countPart, _ := strings.Cut(token, "+")
	addr, err := parseIPv4(addrPart)
	if err != nil {
		return nil, malformed
	}
	count, err := strconv.Atoi(countPart)
	if err != nil || count < 1 {
		return nil, malformed
	}

	start := toUint32(addr)
	// Guard the arithmetic before it wraps around to 0.0.0.0.
	if uint64(start)+uint64(count)-1 > uint64(^uint32(0)) {
		return nil, fmt.Errorf("range %q runs past 255.255.255.255", token)
	}
	return expandRange(start, start+uint32(count)-1, limit)
}

// expandHyphenated treats a token as an address range only when both sides are
// addresses; otherwise it is a hostname that happens to contain hyphens, such
// as my-server.example.com.
func expandHyphenated(token string, limit int) ([]string, error) {
	left := token[:strings.Index(token, "-")]
	right := token[strings.LastIndex(token, "-")+1:]

	start, startErr := parseIPv4(left)
	end, endErr := parseIPv4(right)
	if startErr != nil || endErr != nil {
		if looksLikeAddress(left) || looksLikeAddress(right) {
			return nil, fmt.Errorf("invalid address in range %q", token)
		}
		return []string{token}, nil
	}
	return expandRange(toUint32(start), toUint32(end), limit)
}

func expandRange(start, end uint32, limit int) ([]string, error) {
	if start > end {
		return nil, fmt.Errorf("range start (%s) is after end (%s)", fromUint32(start), fromUint32(end))
	}
	// Counted in int64: on a 32-bit build int(0xFFFFFFFF)+1 wraps to 0, which
	// would slip a /0 past this guard and then try to enumerate 4.3 billion
	// addresses.
	size := int64(end-start) + 1
	if size > int64(limit) {
		return nil, fmt.Errorf("range has %d hosts (max %d)", size, limit)
	}

	hosts := make([]string, 0, size)
	for v := start; ; v++ {
		hosts = append(hosts, fromUint32(v).String())
		if v == end {
			break // Compared here so an end of 255.255.255.255 cannot loop forever.
		}
	}
	return hosts, nil
}

func parseIPv4(s string) (netip.Addr, error) {
	addr, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Addr{}, err
	}
	if !addr.Is4() {
		return netip.Addr{}, fmt.Errorf("%q is not an IPv4 address", s)
	}
	return addr, nil
}

// looksLikeAddress reports whether a token was clearly meant to be an address,
// so a typo such as 999.1.1.1 is reported rather than silently accepted as a
// hostname and handed to the resolver.
func looksLikeAddress(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if (r < '0' || r > '9') && r != '.' {
			return false
		}
	}
	return strings.Count(s, ".") == 3
}

func toUint32(a netip.Addr) uint32 {
	b := a.As4()
	return uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
}

func fromUint32(v uint32) netip.Addr {
	return netip.AddrFrom4([4]byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)})
}
