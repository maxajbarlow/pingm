//go:build darwin || linux || dragonfly || freebsd || netbsd || openbsd

package monitor

import (
	"fmt"
	"net"
	"os"
	"runtime"
	"syscall"
)

// sysIPStripHdr is darwin's IP_STRIPHDR, which makes a datagram ICMP socket
// hand back the ICMP message without the IP header in front of it. Linux
// already does that; only darwin needs asking.
const sysIPStripHdr = 0x17

// protoICMP is IPPROTO_ICMP.
const protoICMP = 1

// listenDatagram opens the unprivileged ICMP socket, sized to hold a burst.
//
// It builds the socket by hand rather than calling icmp.ListenPacket, which
// does the same thing but leaves the receive buffer at the system default —
// net.inet.raw.recvspace on darwin, which is 8KB, or roughly thirty queued
// packets. Sweeping a /24 puts far more than thirty replies in flight, and
// everything past the thirtieth is dropped by the kernel with no error
// anywhere: the probes simply go unanswered and the table calls live hosts
// down.
func listenDatagram(targets int) (net.PacketConn, error) {
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_DGRAM, protoICMP)
	if err != nil {
		return nil, os.NewSyscallError("socket", err)
	}

	// Anything that fails from here has to close the raw descriptor itself;
	// it is not owned by an *os.File until the very end.
	fail := func(stage string, err error) (net.PacketConn, error) {
		syscall.Close(fd)
		return nil, os.NewSyscallError(stage, err)
	}

	if runtime.GOOS == "darwin" || runtime.GOOS == "ios" {
		if err := syscall.SetsockoptInt(fd, protoIP, sysIPStripHdr, 1); err != nil {
			return fail("setsockopt", err)
		}
	}

	// Best effort: the kernel silently clamps this to kern.ipc.maxsockbuf, and
	// a smaller buffer than asked for is still far better than the default. A
	// failure here is not worth refusing to run over.
	_ = syscall.SetsockoptInt(fd, syscall.SOL_SOCKET, syscall.SO_RCVBUF, receiveBuffer(targets))

	if err := syscall.Bind(fd, &syscall.SockaddrInet4{}); err != nil {
		return fail("bind", err)
	}

	f := os.NewFile(uintptr(fd), "datagram-oriented icmp")
	defer f.Close() // FilePacketConn dups the descriptor; this closes ours.

	conn, err := net.FilePacketConn(f)
	if err != nil {
		return nil, fmt.Errorf("wrapping the ICMP socket: %w", err)
	}
	return conn, nil
}

// protoIP is IPPROTO_IP, the level darwin's IP_STRIPHDR is set at.
const protoIP = 0

// tune raises the receive buffer on a socket the caller already opened, for
// the raw fallback where the socket is not ours to build.
func tune(conn net.PacketConn, targets int) {
	if c, ok := conn.(interface{ SetReadBuffer(int) error }); ok {
		_ = c.SetReadBuffer(receiveBuffer(targets))
	}
}
