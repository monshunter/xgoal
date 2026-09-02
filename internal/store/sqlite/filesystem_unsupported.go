//go:build !darwin && !linux

package sqlite

import "fmt"

func detectFilesystem(path string) (filesystemInfo, error) {
	return filesystemInfo{}, fmt.Errorf("filesystem detection is unsupported on this platform for %s", path)
}
