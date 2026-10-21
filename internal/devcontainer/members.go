package devcontainer

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Member is a member of the configuration [LoadMembers] names: its name as asked for,
// and its value as written, the comments left out.
type Member struct {
	Name  string
	Value json.RawMessage
}

// LoadMembers is [Load], and the members among names the configuration sets, in the
// order of names. A name is a member of the configuration, such as dockerComposeFile,
// or build. and a member of build, such as build.options; it is set when it is present
// and neither null, false, "", [] nor {}. The configuration is read once, so the two
// answers are of one text. It returns nil, nil, nil when the repository keeps none.
func LoadMembers(root string, names []string) (*Config, []Member, error) {
	place, err := Find(root)
	if err != nil || place == "" {
		return nil, nil, err
	}
	data, err := readPlace(root, place)
	if err != nil {
		return nil, nil, err
	}
	c, err := Parse(place, data)
	if err != nil {
		return nil, nil, err
	}
	clean, _ := strip(data)
	top, err := members(clean)
	if err != nil {
		return nil, nil, fmt.Errorf(textNotObject, place)
	}
	var build []member
	for _, m := range top {
		if m.name == "build" && len(m.value) > 0 && m.value[0] == '{' {
			build, _ = members(m.value)
		}
	}
	var out []Member
	for _, name := range names {
		in, n := top, name
		if rest, ok := strings.CutPrefix(name, "build."); ok {
			in, n = build, rest
		}
		for _, m := range in {
			if m.name == n && set(m.value) {
				out = append(out, Member{name, m.value})
				break
			}
		}
	}
	return c, out, nil
}

// readPlace reads the configuration at place, which [Find] found under root, as
// [Load] reads it: through an [os.Root] of root, a regular file of at most [MaxSize]
// bytes.
func readPlace(root, place string) ([]byte, error) {
	r, err := os.OpenRoot(root)
	if err != nil {
		return nil, notRead(place, err)
	}
	defer r.Close()
	f, err := r.Open(filepath.FromSlash(place))
	if err != nil {
		if _, again := check(root, place); again != nil {
			return nil, again
		}
		return nil, notRead(place, err)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, notRead(place, err)
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf(textNotFile, place)
	}
	data, err := io.ReadAll(io.LimitReader(f, MaxSize+1))
	if err != nil {
		return nil, notRead(place, err)
	}
	return data, nil
}
