package child

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
)

// statField is field n, counted from 1, of b, the contents of /proc/<pid>/stat on
// Linux, for n of 3 or more: the fields after the command's name. The name, field 2, is
// in parentheses and may contain spaces and parentheses, so the fields after it start
// after the last ")" of b.
func statField(b []byte, n int) (string, error) {
	if n < 3 {
		return "", fmt.Errorf("field %d of /proc/<pid>/stat is not after the command's name", n)
	}
	i := bytes.LastIndexByte(b, ')')
	if i < 0 {
		return "", errors.New("/proc/<pid>/stat has no command name")
	}
	fields := strings.Fields(string(b[i+1:]))
	if len(fields) < n-2 {
		return "", fmt.Errorf("/proc/<pid>/stat has no field %d", n)
	}
	return fields[n-3], nil
}
