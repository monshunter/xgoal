//go:build darwin

package supervisor

import (
	"errors"
	"fmt"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

func InspectProcess(pid int) (ProcessIdentity, error) {
	if pid <= 0 {
		return ProcessIdentity{}, errors.New("invalid process id")
	}
	info, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		if errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
			return ProcessIdentity{}, os.ErrProcessDone
		}
		return ProcessIdentity{}, err
	}
	if info.Proc.P_pid != int32(pid) || info.Proc.P_stat == 5 {
		return ProcessIdentity{}, os.ErrProcessDone
	}
	return darwinIdentity(*info), nil
}

func darwinIdentity(info unix.KinfoProc) ProcessIdentity {
	return ProcessIdentity{PID: int(info.Proc.P_pid), PGID: int(info.Eproc.Pgid), StartID: fmt.Sprintf("darwin:%d:%d", info.Proc.P_starttime.Sec, info.Proc.P_starttime.Usec)}
}

func groupMembers(pgid, exclude int) ([]ProcessIdentity, error) {
	processes, err := unix.SysctlKinfoProcSlice("kern.proc.pgrp", pgid)
	if err != nil {
		return nil, err
	}
	var result []ProcessIdentity
	for _, info := range processes {
		if int(info.Eproc.Pgid) == pgid && int(info.Proc.P_pid) != exclude && info.Proc.P_stat != 5 {
			result = append(result, darwinIdentity(info))
		}
	}
	return result, nil
}
