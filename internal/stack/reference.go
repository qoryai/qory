package stack

import (
	"net/url"
	"path"
	"strings"

	"github.com/qoryai/qory/internal/exports"
)

// ReferenceForm is the form of a stack reference, as an error line prints it.
const ReferenceForm = "<https URL>#<ref>:<folder>"

// AtBeforePath reports whether u, a URL with "://", has an @ between "://" and the first
// / after it, a ? or # before the @ included: url.Parse ends the authority at a ? or #,
// while another URL parser may read what is before the @ as a user.
func AtBeforePath(u string) bool {
	_, rest, ok := strings.Cut(u, "://")
	authority, _, _ := strings.Cut(rest, "/")
	return ok && strings.Contains(authority, "@")
}

// NonASCIIHost reports whether u parses as a URL whose host, its percent-encodings
// decoded, has a byte of 0x80 or above. Such a host can pass [AtBeforePath] and
// url.Parse with no user, while curl built with IDN maps a fullwidth @ or : in it to an
// ASCII one, so the host that curl sends to the proxy has a user part. It returns false
// for a URL that does not parse: each caller stops such a URL before it calls this.
func NonASCIIHost(u string) bool {
	p, err := url.Parse(u)
	if err != nil {
		return false
	}
	host := p.Hostname()
	for i := 0; i < len(host); i++ {
		if host[i] >= 0x80 {
			return true
		}
	}
	return false
}

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
		return Source{}, true, exports.Refuse("Set -f to an https URL, as "+ReferenceForm+".", "qory fetches a stack reference only over https, and "+exports.URL(raw)+" is not an https URL with a host.", "-f is "+shown+".")
	}
	ref, folder, hasFolder := strings.Cut(rest, ":")
	if !hasRef || ref == "" {
		return Source{}, true, exports.Refuse("Add the ref to -f, as "+ReferenceForm+".", "The stack reference has no branch, tag, or commit after #.", "-f is "+shown+".")
	}
	if !hasFolder || folder == "" {
		return Source{}, true, exports.Refuse("Add the folder to -f, as "+ReferenceForm+".", "The stack reference has no folder after the ref. The folder is the one that contains the "+FileName+".", "-f is "+shown+".")
	}
	if strings.HasPrefix(folder, "/") {
		return Source{}, true, exports.Refuse("Write the folder in -f as a path from the repository's root.", "The folder "+exports.Value(folder, false)+" is an absolute path.", "-f is "+shown+".")
	}
	for _, seg := range strings.Split(folder, "/") {
		if seg == ".." {
			return Source{}, true, exports.Refuse("Remove the .. segment from the folder in -f.", "The folder "+exports.Value(folder, false)+" has a .. segment, and the folder is a path from the repository's root.", "-f is "+shown+".")
		}
	}
	clean := path.Clean(folder)
	if clean == "." {
		clean = ""
	}
	return Source{Git: raw, Ref: ref, Path: clean}, true, nil
}
