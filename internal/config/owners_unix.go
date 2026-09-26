//go:build unix

package config

import (
	"os"
	"os/user"
	"strconv"
	"syscall"
)

// ownersOnly holds each path of chains and every directory above it to [trusted], and
// each of links, a link itself, to its owner alone, as the system names its users and
// groups.
func ownersOnly(chains, links []string) error {
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
		Primary: func(uid uint32) (uint32, bool) {
			u, err := user.LookupId(strconv.FormatUint(uint64(uid), 10))
			if err != nil {
				return 0, false
			}
			gid, err := strconv.ParseUint(u.Gid, 10, 32)
			return uint32(gid), err == nil
		},
	}
	euid := uint32(os.Geteuid())
	for _, p := range chains {
		if err := chainTrusted(p, lstatOwner, euid, n); err != nil {
			return err
		}
	}
	for _, l := range links {
		o, err := lstatOwner(l)
		if err != nil {
			return err
		}
		if err := ownedBy(l, "a link ", o, euid, n); err != nil {
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
