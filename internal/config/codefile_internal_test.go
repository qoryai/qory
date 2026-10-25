package config

import (
	"errors"
	"maps"
	"os"
	"strings"
	"testing"
)

// TestWritersRefusalStartsWithTheFix is each error of the rule of [CheckWriters] in its
// words: another user's file, a group that may write it, and every user; any other
// error is returned as it is. The integrations' texts of the same errors are unchanged.
func TestWritersRefusalStartsWithTheFix(t *testing.T) {
	const setting, rule = "gateway.server.enrolment_code_file", ", and only root and the user running qory may write gateway.server.enrolment_code_file and its folders, except a folder that root owns with the sticky bit set"
	for _, c := range []struct {
		u           *untrusted
		want, today string
	}{
		{&untrusted{path: "/run/code", owner: "nobody (65534)"}, "make root or the user running qory the owner of /run/code: it is owned by nobody (65534)" + rule, "/run/code is owned by nobody (65534), neither root nor the user running qory"},
		{&untrusted{path: "/run", group: "builders (1002)"}, "remove the group's write permission from /run: the group builders (1002) may write it" + rule, "/run may be written by the group builders (1002), which is neither root's, wheel, admin nor its owner's primary group named as the owner"},
		{&untrusted{path: "/run", every: true}, "remove the write permission of other users from /run: every user may write it" + rule, "/run may be written by every user; qory runs a program only root and its owner may change"},
	} {
		if err := writersRefusal(setting, c.u); err == nil || err.Error() != c.want {
			t.Errorf("%v, want %q", err, c.want)
		}
		if c.u.Error() != c.today {
			t.Errorf("the integrations' text is %q, want %q", c.u.Error(), c.today)
		}
	}
	other := errors.New("lstat /run/code: permission denied")
	if err := writersRefusal(setting, other); err != other {
		t.Errorf("%v, want the error as it is", err)
	}
	if err := writersRefusal(setting, nil); err != nil {
		t.Errorf("%v, want nil", err)
	}
}

// TestTheCodeFileRuleTakesTheDeployLayout holds the rule of [CheckWriters] to synthetic
// owners and modes: the deploy's layout, /, /run and /run/qory root's 0755, the folder
// 65532's 0700 and the file 65532's 0400, read by the user 65532, passes, and so does a
// sticky /tmp root owns; a folder or a file its group may write is an error, root's
// group and the owner's private group included, which the rule of an integration's
// program accepts.
func TestTheCodeFileRuleTakesTheDeployLayout(t *testing.T) {
	const gw = 65532
	n := names{
		User:    func(uid uint32) string { return map[uint32]string{0: "root", gw: "nonroot"}[uid] },
		Group:   func(gid uint32) string { return map[uint32]string{0: "root", gw: "nonroot"}[gid] },
		Primary: func(uid uint32) (uint32, bool) { return map[uint32]uint32{0: 0, gw: gw}[uid], true },
	}
	dir := func(perm os.FileMode) os.FileMode { return os.ModeDir | perm }
	deploy := map[string]fileOwner{
		"/":                        {0, 0, dir(0o755)},
		"/run":                     {0, 0, dir(0o755)},
		"/run/qory":                {0, 0, dir(0o755)},
		"/run/qory/enrolment":      {gw, gw, dir(0o700)},
		"/run/qory/enrolment/code": {gw, gw, 0o400},
	}
	check := func(layout map[string]fileOwner) error {
		stat := func(p string) (fileOwner, error) { return layout[p], nil }
		err := chainTrusted("/run/qory/enrolment/code", stat, gw, n)
		if err == nil {
			err = groupWriteChain("/run/qory/enrolment/code", stat, n)
		}
		return writersRefusal("gateway.server.enrolment_code_file", err)
	}
	if err := check(deploy); err != nil {
		t.Errorf("the deploy's layout: %v", err)
	}
	for _, c := range []struct {
		what string
		path string
		o    fileOwner
		want string
	}{
		{"a sticky /run root owns", "/run", fileOwner{0, 0, dir(0o1777) | os.ModeSticky}, ""},
		{"/run/qory its group, root's, may write", "/run/qory", fileOwner{0, 0, dir(0o775)}, "remove the group's write permission from /run/qory: the group root (0) may write it"},
		{"the folder its group, the owner's private one, may write", "/run/qory/enrolment", fileOwner{gw, gw, dir(0o770)}, "remove the group's write permission from /run/qory/enrolment: the group nonroot (65532) may write it"},
		{"the file its group may write", "/run/qory/enrolment/code", fileOwner{gw, gw, 0o460}, "remove the group's write permission from /run/qory/enrolment/code: the group nonroot (65532) may write it"},
		{"the file every user may write", "/run/qory/enrolment/code", fileOwner{gw, gw, 0o406}, "remove the write permission of other users from /run/qory/enrolment/code"},
		{"the file another user owns", "/run/qory/enrolment/code", fileOwner{1000, gw, 0o400}, "make root or the user running qory the owner of /run/qory/enrolment/code: it is owned by 1000"},
	} {
		layout := maps.Clone(deploy)
		layout[c.path] = c.o
		err := check(layout)
		if c.want == "" && err != nil || c.want != "" && (err == nil || !strings.HasPrefix(err.Error(), c.want)) {
			t.Errorf("%s: %v, want %q", c.what, err, c.want)
		}
		if c.want != "" && c.o.Mode.Perm()&0o020 != 0 {
			stat := func(p string) (fileOwner, error) { return layout[p], nil }
			if err := chainTrusted("/run/qory/enrolment/code", stat, gw, n); err != nil {
				t.Errorf("%s: the rule of an integration's program returns an error too: %v", c.what, err)
			}
		}
	}
}
