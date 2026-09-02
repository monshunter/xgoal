//go:build linux

package sqlite

import (
	"fmt"

	"golang.org/x/sys/unix"
)

func detectFilesystem(path string) (filesystemInfo, error) {
	var stat unix.Statfs_t
	if err := unix.Statfs(path, &stat); err != nil {
		return filesystemInfo{}, fmt.Errorf("statfs %s: %w", path, err)
	}
	return filesystemInfo{Magic: stat.Type}, nil
}
