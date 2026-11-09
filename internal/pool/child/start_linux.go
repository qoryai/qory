package child

import (
	"bytes"
	"errors"
	"os"
	"strconv"
	"strings"
	"time"
)

// userHZ is the unit of the times of /proc/<pid>/stat, in ticks per second: USER_HZ,
// which the kernel keeps at 100 on the architectures qory is built for, amd64 and
// arm64.
const userHZ = 100

// startTime is when the process id started: the boot time btime of /proc/stat, in
// whole seconds, and starttime, field 22 of /proc/<id>/stat, in ticks since boot. Both
// are rounded down, so the time is at most a second before the process's start.
func startTime(id int) (time.Time, error) {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(id) + "/stat")
	if err != nil {
		return time.Time{}, err
	}
	// The command's name, field 2, is in parentheses and may contain spaces and
	// parentheses: the fields after it start after its last ")".
	i := bytes.LastIndexByte(b, ')')
	if i < 0 {
		return time.Time{}, errors.New("/proc/<pid>/stat has no command name")
	}
	fields := strings.Fields(string(b[i+1:]))
	// fields[0] is field 3, so field 22 is fields[19].
	if len(fields) < 20 {
		return time.Time{}, errors.New("/proc/<pid>/stat has no starttime")
	}
	ticks, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil {
		return time.Time{}, err
	}
	boot, err := bootTime()
	if err != nil {
		return time.Time{}, err
	}
	return boot.Add(time.Duration(ticks) * time.Second / userHZ), nil
}

// bootTime is the line btime of /proc/stat: when the machine booted, in whole seconds.
func bootTime() (time.Time, error) {
	b, err := os.ReadFile("/proc/stat")
	if err != nil {
		return time.Time{}, err
	}
	for line := range strings.SplitSeq(string(b), "\n") {
		if v, ok := strings.CutPrefix(line, "btime "); ok {
			s, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
			if err != nil {
				return time.Time{}, err
			}
			return time.Unix(s, 0), nil
		}
	}
	return time.Time{}, errors.New("/proc/stat has no btime")
}
