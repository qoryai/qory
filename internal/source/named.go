package source

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/qoryai/forager/accesskey"

	"github.com/qoryai/qory/internal/exports"
	"github.com/qoryai/qory/internal/stack"
)

// Named is the compose of a harness whose addresses no text shows: the URL, the ref and
// the folder of each of its git sources. Each source is recorded with its phrase, such
// as the base of the workspace's harness or the module tools, before or as it resolves.
// A git source resolved with a Named in [Git] is fetched into a folder of the cache
// named by a hash of its URL alone, its refs file is named by a hash of its ref, and
// each text of this package shows [Options.Name] in place of the source's URL and ref,
// and no output of git. [Named.Hide] does the same for any other text, such as a
// compose's error, for each source recorded so far. Make one with [NewNamed]; it is
// safe for use by several goroutines.
type Named struct {
	// harness is the harness as a text shows it, such as the workspace's harness.
	harness string

	mu sync.Mutex
	// forms are the texts of each source's address that [Named.Hide] replaces, each with
	// the source's phrase, longest first.
	forms []namedForm
	// at are the texts of each source's URL with its ref, <URL>#<ref>, each with the
	// name of the source's cache folder in place of a phrase, longest first.
	at []namedForm
	// repos are the sources recorded, by the name of the cache folder of their URL.
	repos map[string]*namedRepo
	// paths matches a path in a folder of repos, and inRepo a URL and ref of at followed
	// by :<path>; nil until [Named.Hide] makes them, and again each time a source is
	// added.
	paths, inRepo *regexp.Regexp
}

// namedForm is one text of a source's address and the phrase that takes its place.
type namedForm struct{ text, phrase string }

// namedRepo is the sources recorded of one URL: the first phrase recorded for it, and
// the folder of each source in the repository, "" for its root, with the source's
// phrase.
type namedRepo struct {
	phrase  string
	folders []namedForm
}

// NewNamed returns the Named of the harness that a text shows as harness, such as the
// workspace's harness.
func NewNamed(harness string) *Named {
	return &Named{harness: harness, repos: map[string]*namedRepo{}}
}

// Phrase is the harness as a text shows it: the workspace's harness.
func (n *Named) Phrase() string { return n.harness }

// Base is the stack the harness extends as a text shows it: the base of the workspace's
// harness.
func (n *Named) Base() string { return "the base of " + n.harness }

// Module is the module name, a module of the harness, as a text shows it: the module
// tools, or a module of the workspace's harness for a module listed without a name.
func (n *Named) Module(name string) string {
	if name == "" {
		return "a module of " + n.harness
	}
	return "the module " + accesskey.ShownName(name, false)
}

// inTree is p, a path in the repository r, as a text shows it: the path from the folder
// of the source of r whose folder contains p, the longest such folder, in that source's
// phrase; the phrase alone for the folder itself; and the first phrase of r alone for a
// path in no folder of a source.
func (r *namedRepo) inTree(p string) string {
	p = strings.Trim(p, "/")
	best := -1
	for i, f := range r.folders {
		if (f.text == "" || p == f.text || strings.HasPrefix(p, f.text+"/")) && (best < 0 || len(f.text) > len(r.folders[best].text)) {
			best = i
		}
	}
	if best < 0 {
		return r.phrase
	}
	f := r.folders[best]
	rest := strings.Trim(strings.TrimPrefix(p, f.text), "/")
	if rest == "" {
		return f.phrase
	}
	return exports.Value(rest, false) + " in " + f.phrase
}

// Add records s, a git source of the harness, under phrase, the source as a text shows
// it: from then on [Named.Hide] shows phrase in place of its URL, with or without its
// ref, and a path in its cache folder from its folder. The first phrase recorded for a
// URL, for a URL and ref, and for a folder of a URL, is the one kept.
func (n *Named) Add(s stack.Source, phrase string) {
	if s.Git == "" {
		return
	}
	shownURL, shownRef := exports.URL(s.Git), exports.Value(s.Ref, false)
	for _, text := range []string{
		s.Git + "#" + s.Ref, shownURL + "#" + shownRef,
		s.Git + " at " + s.Ref, shownURL + " at " + shownRef,
		s.Git, shownURL,
	} {
		n.add(text, phrase)
	}
	dir := hashedName(s.Git)
	folder := ""
	if s.Path != "" {
		folder = strings.Trim(path.Clean("/"+s.Path), "/")
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	for _, text := range []string{s.Git + "#" + s.Ref, shownURL + "#" + shownRef} {
		if !hasForm(n.at, text) {
			n.at = sortedForms(append(n.at, namedForm{text, dir}))
		}
	}
	r, ok := n.repos[dir]
	if !ok {
		r = &namedRepo{phrase: phrase}
		n.repos[dir] = r
	}
	if !hasForm(r.folders, folder) {
		r.folders = append(r.folders, namedForm{folder, phrase})
	}
	n.paths, n.inRepo = nil, nil
}

// add records text, a text of an address, under phrase, unless it is recorded.
func (n *Named) add(text, phrase string) {
	if text == "" {
		return
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if !hasForm(n.forms, text) {
		n.forms = sortedForms(append(n.forms, namedForm{text, phrase}))
	}
}

// hasForm reports whether forms has text.
func hasForm(forms []namedForm, text string) bool {
	for _, f := range forms {
		if f.text == text {
			return true
		}
	}
	return false
}

// sortedForms is forms, the longest text first.
func sortedForms(forms []namedForm) []namedForm {
	sort.SliceStable(forms, func(i, j int) bool { return len(forms[i].text) > len(forms[j].text) })
	return forms
}

// Of is the phrase of src, a git source as [stack.Source.String] writes it, such as a
// module's source in a compose's result: the phrase recorded for its URL and ref; ok is
// false for a source not recorded.
func (n *Named) Of(src string) (phrase string, ok bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	for _, f := range n.forms {
		if src == f.text || strings.HasPrefix(src, f.text+":") || strings.HasPrefix(src, f.text+" ") {
			return f.phrase, true
		}
	}
	return "", false
}

// commitSegment is a clone's folder in a source's cache folder: a full commit id.
var commitSegment = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)

// Hide is text with each address recorded, and each path in a recorded source's cache
// folder, replaced by the source's phrase: a URL with its ref, as url#ref or url at ref,
// and a URL alone, longest first, each where nothing that could continue the URL or the
// path follows it; a path in a clone, and a path after a URL and ref as
// <URL>#<ref>:<path>, as <path from the source's folder> in <phrase>, the phrase alone
// for the folder itself and for a path in no source's folder; and any other path in a
// cache folder as the phrase.
func (n *Named) Hide(text string) string {
	n.mu.Lock()
	forms := append([]namedForm(nil), n.forms...)
	at := append([]namedForm(nil), n.at...)
	repos := make(map[string]*namedRepo, len(n.repos))
	for d, r := range n.repos {
		repos[d] = &namedRepo{phrase: r.phrase, folders: append([]namedForm(nil), r.folders...)}
	}
	if n.paths == nil && len(n.repos) > 0 {
		dirs := make([]string, 0, len(n.repos))
		for d := range n.repos {
			dirs = append(dirs, d)
		}
		sort.Strings(dirs)
		n.paths = regexp.MustCompile("[^\\s\"'`(=]*/(" + strings.Join(dirs, "|") + ")((?:/[^\\s\"'`:,;()]*)?)")
		quoted := make([]string, len(n.at))
		for i, f := range n.at {
			quoted[i] = regexp.QuoteMeta(f.text)
		}
		n.inRepo = regexp.MustCompile("(?:" + strings.Join(quoted, "|") + "):([^\\s\"'`:,;()]+)")
	}
	paths, inRepo := n.paths, n.inRepo
	n.mu.Unlock()
	if paths == nil {
		return text
	}
	text = paths.ReplaceAllStringFunc(text, func(m string) string {
		sub := paths.FindStringSubmatch(m)
		trimmed := strings.TrimRight(sub[2], ".")
		dots := sub[2][len(trimmed):]
		r, rest := repos[sub[1]], strings.TrimPrefix(trimmed, "/")
		clone, inClone, _ := strings.Cut(rest, "/")
		if !commitSegment.MatchString(clone) || strings.Trim(inClone, "/") == "" {
			return r.phrase + dots
		}
		return r.inTree(inClone) + dots
	})
	text = inRepo.ReplaceAllStringFunc(text, func(m string) string {
		for _, f := range at {
			if p, ok := strings.CutPrefix(m, f.text+":"); ok {
				trimmed := strings.TrimRight(p, ".")
				return repos[f.phrase].inTree(trimmed) + p[len(trimmed):]
			}
		}
		return m
	})
	for _, f := range forms {
		text = replaceBounded(text, f.text, f.phrase)
	}
	return text
}

// replaceBounded is text with each old replaced by phrase where nothing that could
// continue a URL or a path follows it: the end of the text, or a character other than
// a letter, a digit and _~%+-/, or a dot followed by one of those. An old followed by
// .git, by a path that starts with /, or by both, is replaced with them, as they are a
// part of the same address.
func replaceBounded(text, old, phrase string) string {
	var b strings.Builder
	i := 0
	for {
		j := strings.Index(text[i:], old)
		if j < 0 {
			break
		}
		end := i + j + len(old)
		b.WriteString(text[i : i+j])
		switch rest, ok := extension(text, end); {
		case ok:
			b.WriteString(phrase)
			end = rest
		case continues(text, end):
			b.WriteString(old)
		default:
			b.WriteString(phrase)
		}
		i = end
	}
	b.WriteString(text[i:])
	return b.String()
}

// extension is the end of what continues an address at i in text as a part of the same
// address: .git, unless a letter, a digit or one of _~%+- follows it, then a path that
// starts with /, up to a space, a quote or one of :,;(), without dots at its end; ok is
// false when neither is there.
func extension(text string, i int) (end int, ok bool) {
	end = i
	if strings.HasPrefix(text[end:], ".git") && (end+4 == len(text) || text[end+4] == '/' || !continues(text, end+4)) {
		end += 4
	}
	if end < len(text) && text[end] == '/' {
		for end < len(text) && !strings.ContainsRune(" \t\r\n\"'`:,;()", rune(text[end])) {
			end++
		}
		for end > i && text[end-1] == '.' {
			end--
		}
	}
	return end, end > i
}

// continues reports whether text at i continues a URL or a path: a letter, a digit or
// one of _~%+-/, or a dot followed by one of them.
func continues(text string, i int) bool {
	if i >= len(text) {
		return false
	}
	if text[i] == '.' {
		return i+1 < len(text) && continuing(text[i+1])
	}
	return continuing(text[i])
}

// continuing reports whether c is a letter, a digit or one of _~%+-/.
func continuing(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte("_~%+-/", c) >= 0
}

// HideError is err with its text as [Named.Hide] prints it, and with the text of a
// [stack.SourcePathError], a [stack.BaseExtendsError] or an [exports.DirError] under it
// as its Hidden method prints it, without the path or the address; it unwraps to err, so a caller still
// matches the error under it. nil, and an error whose text has nothing to hide, are
// returned as they are.
func (n *Named) HideError(err error) error {
	if err == nil {
		return nil
	}
	shown := err.Error()
	var sp *stack.SourcePathError
	if errors.As(err, &sp) {
		shown = strings.Replace(shown, sp.Error(), sp.Hidden(), 1)
	}
	var be *stack.BaseExtendsError
	if errors.As(err, &be) {
		shown = strings.Replace(shown, be.Error(), be.Hidden(), 1)
	}
	var de *exports.DirError
	if errors.As(err, &de) {
		shown = strings.Replace(shown, de.Error(), de.Hidden(), 1)
	}
	text := n.Hide(shown)
	if text == err.Error() {
		return err
	}
	return namedError{text, err}
}

// namedError is an error with its text as [Named.Hide] prints it.
type namedError struct {
	text string
	err  error
}

func (e namedError) Error() string { return e.text }
func (e namedError) Unwrap() error { return e.err }

// hashedName is the name of a cache folder or a refs file of a source v of a [Named], a
// URL or a ref: 32 hexadecimal characters of v's SHA-256, so no path contains v.
func hashedName(v string) string {
	sum := sha256.Sum256([]byte(v))
	return hex.EncodeToString(sum[:16])
}

// named is the Named of opts, nil for a compose whose addresses show.
func named(opts Options) *Named {
	if opts.Git == nil {
		return nil
	}
	return opts.Git.Named
}

// shownAs is s as a text of a resolve with opts shows it: [Options.Name] for a source of
// a [Named], a source of the workspace's harness when it is empty, and otherwise s's URL
// as [exports.URL] prints it.
func shownAs(s stack.Source, opts Options) string {
	n := named(opts)
	switch {
	case n == nil:
		return exports.URL(s.Git)
	case opts.Name != "":
		return opts.Name
	}
	return "a source of " + n.Phrase()
}
