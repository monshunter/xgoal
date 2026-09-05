//go:build linux

package supervisor

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func InspectProcess(pid int) (ProcessIdentity, error) {
	if pid <= 0 {
		return ProcessIdentity{}, errors.New("invalid process id")
	}
	content, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if errors.Is(err, os.ErrNotExist) {
		return ProcessIdentity{}, os.ErrProcessDone
	}
	if err != nil {
		return ProcessIdentity{}, err
	}
	end := strings.LastIndexByte(string(content), ')')
	if end < 0 {
		return ProcessIdentity{}, errors.New("invalid proc stat")
	}
	fields := strings.Fields(string(content)[end+1:])
	if len(fields) < 20 {
		return ProcessIdentity{}, errors.New("incomplete proc stat")
	}
	if fields[0] == "Z" || fields[0] == "X" {
		return ProcessIdentity{}, os.ErrProcessDone
	}
	pgid, err := strconv.Atoi(fields[2])
	if err != nil {
		return ProcessIdentity{}, err
	}
	if _, err := strconv.ParseUint(fields[19], 10, 64); err != nil {
		return ProcessIdentity{}, err
	}
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return ProcessIdentity{}, err
	}
	return ProcessIdentity{PID: pid, PGID: pgid, StartID: "linux:" + strings.TrimSpace(string(boot)) + ":" + fields[19]}, nil
}

func groupMembers(pgid, exclude int) ([]ProcessIdentity, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	var result []ProcessIdentity
	for _, entry := range entries {
		pid, err := strconv.Atoi(filepath.Base(entry.Name()))
		if err != nil || pid == exclude {
			continue
		}
		identity, err := InspectProcess(pid)
		if errors.Is(err, os.ErrProcessDone) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if identity.PGID == pgid {
			result = append(result, identity)
		}
	}
	return result, nil
}
