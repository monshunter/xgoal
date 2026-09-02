package sqlite

import (
	"fmt"
	"strings"
)

type filesystemInfo struct {
	Name  string
	Magic int64
}

func (f filesystemInfo) describe() string {
	if f.Name != "" {
		return f.Name
	}
	return fmt.Sprintf("magic=0x%x", f.Magic)
}

func isKnownNetworkFilesystem(info filesystemInfo) bool {
	name := strings.ToLower(strings.TrimSpace(info.Name))
	switch name {
	case "9p", "afpfs", "ceph", "cifs", "nfs", "nfs4", "smb", "smbfs", "webdav":
		return true
	}
	switch uint64(info.Magic) {
	case 0x6969, // NFS
		0xff534d42, // CIFS/SMB2
		0x00c36400, // Ceph
		0x01021997, // 9P
		0x73757245, // Coda
		0x5346414f: // AFS
		return true
	default:
		return false
	}
}
