package stack

import (
	"fmt"
	"net/url"
	"path"
	"strings"

	"github.com/qoryai/qory/internal/exports"
)

// ReferenceForm is the form of a stack reference, as an error line prints it.
const ReferenceForm = "<https URL>#<ref>:<folder>"

// ParseReference reads value, what -f is set to, as a stack reference: a folder of a git
// repository at a ref, written <https URL>#<ref>:<folder>, such as
// https://github.com/example-org/harness#v1.2.0:stacks/web. The folder is the one that
// contains the stack's [FileName], as a path from the repository's root; "." is the
// root. ok is false for a value without "://", which is a file on this machine.
//
// The URL is https with a host, and the ref and the folder are both required. A folder
// that is absolute or has a ".." segment is an error. The source comes back with the
// folder cleaned, "" for the root, as [Source.Path] of a git source has it.
func ParseReference(value string) (s Source, ok bool, err error) {
	if !strings.Contains(value, "://") {
		return Source{}, false, nil
	}
	shown := exports.URL(value)
	raw, rest, hasRef := strings.Cut(value, "#")
	if u, err := url.Parse(raw); err != nil || !strings.EqualFold(u.Scheme, "https") || u.Host == "" {
		return Source{}, true, fmt.Errorf("use an https URL in -f %s, as %s: qory fetches a stack reference only over https, and %s is not an https URL with a host", shown, ReferenceForm, exports.URL(raw))
	}
	ref, folder, hasFolder := strings.Cut(rest, ":")
	if !hasRef || ref == "" {
		return Source{}, true, fmt.Errorf("add the ref to -f %s, as %s: the stack reference has no branch, tag or commit after #", shown, ReferenceForm)
	}
	if !hasFolder || folder == "" {
		return Source{}, true, fmt.Errorf("add the folder to -f %s, as %s: the stack reference has no folder after the ref; the folder is the one that contains the %s", shown, ReferenceForm, FileName)
	}
	if strings.HasPrefix(folder, "/") {
		return Source{}, true, fmt.Errorf("write the folder in -f %s as a path from the repository's root: %s is an absolute path", shown, exports.Value(folder, false))
	}
	for _, seg := range strings.Split(folder, "/") {
		if seg == ".." {
			return Source{}, true, fmt.Errorf("write the folder in -f %s without ..: %s has a .. segment, and the folder is a path from the repository's root", shown, exports.Value(folder, false))
		}
	}
	clean := path.Clean(folder)
	if clean == "." {
		clean = ""
	}
	return Source{Git: raw, Ref: ref, Path: clean}, true, nil
}
