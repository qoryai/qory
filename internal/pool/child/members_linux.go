package child

import (
	"os"
	"strconv"
)

// SessionMembers is the id of each process in the session id: each process whose
// field 6 of /proc/<pid>/stat, the session, is id. A process that ends while the list
// is read is left out.
func SessionMembers(id int) ([]int, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	want := strconv.Itoa(id)
	var ids []int
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid <= 0 {
			continue
		}
		b, err := os.ReadFile("/proc/" + e.Name() + "/stat")
		if err != nil {
			continue
		}
		if sid, err := statField(b, 6); err == nil && sid == want {
			ids = append(ids, pid)
		}
	}
	return ids, nil
}
