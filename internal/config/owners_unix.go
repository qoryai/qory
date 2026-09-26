//go:build unix

package config

import (
	"os"
	"os/user"
	"strconv"
	"syscall"
)

// ownersOnly holds each path and every directory above it to [trusted], as the system
// names its users and groups.
func ownersOnly(paths []string) error {
	n := names{
		User: func(uid uint32) string {
			if u, err := user.LookupId(strconv.FormatUint(uint64(uid), 10)); err == nil {
				return u.Username
			}
			return ""
		},
		Group: func(gid uint32) string {
			if g, err := user.LookupGroupId(strconv.FormatUint(uint64(gid), 10)); err == nil {
				return g.Name
			}
			return ""
		},
	}
	euid := uint32(os.Geteuid())
	for _, p := range paths {
		if err := chainTrusted(p, lstatOwner, euid, n); err != nil {
			return err
		}
	}
	return nil
}

// lstatOwner reads a path's owner, group and mode, of a link itself and not its target.
func lstatOwner(path string) (fileOwner, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return fileOwner{}, err
	}
	o := fileOwner{Mode: info.Mode()}
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		o.UID, o.GID = st.Uid, st.Gid
	}
	return o, nil
}
