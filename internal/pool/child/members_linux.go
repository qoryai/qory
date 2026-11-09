package child

import (
	"os"
	"strconv"
)

// SessionMembers is each process in the session id: each process whose field 6 of
// /proc/<pid>/stat, the session, is id, with its start stamp, field 22, from the same
// read of the file. A process that ends while the list is read is left out.
func SessionMembers(id int) ([]Member, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	var members []Member
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid <= 0 {
			continue
		}
		if stamp, ok := statInSession(pid, id); ok {
			members = append(members, Member{ID: pid, Stamp: stamp})
		}
	}
	return members, nil
}

// statInSession reads /proc/<id>/stat once, and reports whether field 6, the session,
// is session, with field 22, the start stamp.
func statInSession(id, session int) (string, bool) {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(id) + "/stat")
	if err != nil {
		return "", false
	}
	sid, err := statField(b, 6)
	if err != nil || sid != strconv.Itoa(session) {
		return "", false
	}
	stamp, err := statField(b, 22)
	return stamp, err == nil
}
