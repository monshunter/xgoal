//go:build !darwin && !linux

package daemon

import (
	"errors"
	"net"
)

func peerUID(net.Conn) (int, error) {
	return 0, errors.New("unix peer credentials are unsupported on this platform")
}
