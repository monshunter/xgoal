//go:build darwin

package daemon

import (
	"errors"
	"net"

	"golang.org/x/sys/unix"
)

func peerUID(connection net.Conn) (int, error) {
	unixConnection, ok := connection.(*net.UnixConn)
	if !ok {
		return 0, errors.New("connection is not unix")
	}
	raw, err := unixConnection.SyscallConn()
	if err != nil {
		return 0, err
	}
	var uid int
	var operationErr error
	if err := raw.Control(func(fd uintptr) {
		credential, err := unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
		if err != nil {
			operationErr = err
			return
		}
		uid = int(credential.Uid)
	}); err != nil {
		return 0, err
	}
	return uid, operationErr
}
