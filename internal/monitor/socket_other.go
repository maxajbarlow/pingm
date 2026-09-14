//go:build !(darwin || linux || dragonfly || freebsd || netbsd || openbsd)

package monitor

import (
	"errors"
	"net"
)

// listenDatagram is unix-only; elsewhere the caller falls back to the
// library's own listener, which works but cannot have its receive buffer
// sized for a large sweep.
func listenDatagram(int) (net.PacketConn, error) {
	return nil, errors.New("unsupported platform")
}

func tune(conn net.PacketConn, targets int) {
	if c, ok := conn.(interface{ SetReadBuffer(int) error }); ok {
		_ = c.SetReadBuffer(receiveBuffer(targets))
	}
}
