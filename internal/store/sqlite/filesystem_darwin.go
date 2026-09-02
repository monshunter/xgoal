//go:build darwin

package sqlite

import (
	"bytes"
	"fmt"

	"golang.org/x/sys/unix"
)

func detectFilesystem(path string) (filesystemInfo, error) {
	var stat unix.Statfs_t
	if err := unix.Statfs(path, &stat); err != nil {
		return filesystemInfo{}, fmt.Errorf("statfs %s: %w", path, err)
	}
	name := stat.Fstypename[:]
	if index := bytes.IndexByte(name, 0); index >= 0 {
		name = name[:index]
	}
	return filesystemInfo{Name: string(name)}, nil
}
