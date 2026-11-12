package hostgit

import (
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Shown is s as it can be printed: as it is, or quoted as Go quotes a string when it
// carries a rune Go does not print as it is, such as a control character, a mark that
// turns the text's direction or one of no width, or is not UTF-8.
func Shown(s string) string {
	if !utf8.ValidString(s) || strings.ContainsFunc(s, func(r rune) bool { return !strconv.IsPrint(r) }) {
		return strconv.Quote(s)
	}
	return s
}

// written are the keys qory and git write in a repository, without a subsection: git
// init's, with the ones git init writes on a file system without symlinks or one that
// ignores case, on macOS, and for a repository of another object or ref format.
var written = []string{
	"core.repositoryformatversion", "core.filemode", "core.bare", "core.logallrefupdates",
	"core.ignorecase", "core.precomposeunicode", "core.symlinks",
	"extensions.objectformat", "extensions.refstorage",
}

// writtenUnder are the variables of the keys qory and git write under a subsection of
// any name: remote add's and clone's refspec, and the branch keys a worktree add, a
// clone and qory write, qory-base among them.
var writtenUnder = map[string][]string{
	"remote": {"fetch"},
	"branch": {"remote", "merge", "qory-base"},
}

// Written reports whether qory or git writes key, with any value.
func Written(key string) bool {
	if slices.Contains(written, key) {
		return true
	}
	section, rest, ok := strings.Cut(key, ".")
	if !ok {
		return false
	}
	i := strings.LastIndex(rest, ".")
	if i <= 0 {
		return false
	}
	return slices.Contains(writtenUnder[section], rest[i+1:])
}

// Identity reports whether key is one of the two keys of a commit's identity, which an
// agent sets to commit, and which make git run no program.
func Identity(key string) bool { return key == "user.name" || key == "user.email" }
