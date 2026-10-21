//go:build !unix

package hostgit

// openFlags and noWaitFlags are no flags on a system without O_NOFOLLOW and O_NONBLOCK:
// the check of the path before the open stands alone.
const (
	openFlags   = 0
	noWaitFlags = 0
)

// searchable is true on a system without access(2): the folder's kind and mode stand
// alone.
func searchable(string) bool { return true }

// owner is "" on a system without user ids of owners.
func owner(string) string { return "" }
