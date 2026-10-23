package source

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/qoryai/forager/accesskey"

	"github.com/qoryai/qory/internal/exports"
	"github.com/qoryai/qory/internal/stack"
)

// Named is the compose of a named stack: a stack Qory Apiary sets by its name, whose
// address, the URL of its repository with a ref and a folder, comes in the run's
// documents and is printed in no text. A git source resolved with a Named in [Git] is
// fetched into a folder of the cache named by a hash of its URL alone, its refs file is
// named by a hash of its ref, and each text of this package shows [Options.Name] in
// place of the source's URL and ref, and no output of git. [Named.Hide] does the same
// for any other text, such as a compose's error, for each source resolved so far. Make
// one with [NewNamed]; it is safe for use by several goroutines.
type Named struct {
	// Stack is the stack's name, such as core.
	Stack string

	// folder is the stack's folder in its repository, "" for the root, and stackDir the
	// name of the stack's cache folder: a path of the stack's tree shows from folder.
	folder, stackDir string
	// inStack matches a source in the stack's repository at the stack's ref with a path,
	// <URL>#<ref>:<path>; its first group is the path.
	inStack *regexp.Regexp

	mu sync.Mutex
	// forms are the texts of each source's address that [Named.Hide] replaces, each with
	// the source's phrase, longest first.
	forms []namedForm
	// dirs are the cache folder of each source resolved, by its name, with the
	// source's phrase.
	dirs map[string]string
	// paths matches a path in a folder of dirs; nil until [Named.Hide] makes it, and
	// again each time a folder is added.
	paths *regexp.Regexp
}

// namedForm is one text of a source's address and the phrase that takes its place.
type namedForm struct{ text, phrase string }

// NewNamed returns the Named of the stack name, whose address is reference, the stack
// reference <https URL>#<ref>:<folder> as the documents have it, read into ref by
// [stack.ParseReference]: the reference, the URL with the ref, and the URL with the ref
// and a path under the folder, show as [Named.Phrase] from the start, the path from the
// folder, so no text shows the folder.
func NewNamed(name, reference string, ref stack.Source) *Named {
	n := &Named{Stack: name, folder: ref.Path, stackDir: hashedName(ref.Git), dirs: map[string]string{}}
	at := []string{
		regexp.QuoteMeta(ref.Git + "#" + ref.Ref),
		regexp.QuoteMeta(exports.URL(ref.Git) + "#" + exports.Value(ref.Ref, false)),
	}
	n.inStack = regexp.MustCompile("(?:" + strings.Join(at, "|") + "):([^\\s\"'`:,;()]*)")
	n.add(reference, n.Phrase())
	n.add(ref.Shown(), n.Phrase())
	n.Add(ref, n.Phrase())
	return n
}

// inTree is p, a path in the stack's repository, as a text shows it: the path from the
// stack's folder in <phrase>, and the phrase alone for the folder itself and for a path
// outside it.
func (n *Named) inTree(p string) string {
	p = strings.Trim(p, "/")
	if n.folder != "" {
		if p == n.folder {
			return n.Phrase()
		}
		var ok bool
		if p, ok = strings.CutPrefix(p, n.folder+"/"); !ok {
			return n.Phrase()
		}
	}
	if p == "" {
		return n.Phrase()
	}
	return exports.Value(p, false) + " in " + n.Phrase()
}

// Phrase is the stack as a text shows it: the stack core.
func (n *Named) Phrase() string { return "the stack " + accesskey.ShownName(n.Stack, false) }

// Module is the module name, a module of the stack, as a text shows it:
// the module tools, or a module of the stack core for a module listed without a name.
func (n *Named) Module(name string) string {
	if name == "" {
		return "a module of " + n.Phrase()
	}
	return "the module " + accesskey.ShownName(name, false)
}

// Add records s, a git source of the stack, under phrase, the source as a text shows
// it: from then on [Named.Hide] shows phrase in place of its URL, with or without its
// ref, and in place of a path in its cache folder. The first phrase recorded for a URL,
// or for a URL and ref, is the one kept.
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
	n.mu.Lock()
	defer n.mu.Unlock()
	if _, ok := n.dirs[hashedName(s.Git)]; !ok {
		n.dirs[hashedName(s.Git)] = phrase
		n.paths = nil
	}
}

// add records text, a text of an address, under phrase, unless it is recorded.
func (n *Named) add(text, phrase string) {
	if text == "" {
		return
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	for _, f := range n.forms {
		if f.text == text {
			return
		}
	}
	n.forms = append(n.forms, namedForm{text, phrase})
	sort.SliceStable(n.forms, func(i, j int) bool { return len(n.forms[i].text) > len(n.forms[j].text) })
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
// folder, replaced by the source's phrase: the stack reference, a URL with its ref, as
// url#ref or url at ref, and a URL alone, longest first, each where nothing that could
// continue the URL or the path follows it; and a path in a clone as <path in the
// repository> in <phrase>, any other path in the folder as the phrase. A path in the
// stack's clone, or after the stack's URL and ref as <URL>#<ref>:<path>, shows from the
// stack's folder, and one outside the folder as the phrase.
func (n *Named) Hide(text string) string {
	n.mu.Lock()
	forms := append([]namedForm(nil), n.forms...)
	phrases := map[string]string{}
	for d, p := range n.dirs {
		phrases[d] = p
	}
	if n.paths == nil && len(n.dirs) > 0 {
		dirs := make([]string, 0, len(n.dirs))
		for d := range n.dirs {
			dirs = append(dirs, d)
		}
		sort.Strings(dirs)
		n.paths = regexp.MustCompile("[^\\s\"'`(=]*/(" + strings.Join(dirs, "|") + ")((?:/[^\\s\"'`:,;()]*)?)")
	}
	paths := n.paths
	n.mu.Unlock()
	if paths != nil {
		text = paths.ReplaceAllStringFunc(text, func(m string) string {
			sub := paths.FindStringSubmatch(m)
			trimmed := strings.TrimRight(sub[2], ".")
			dots := sub[2][len(trimmed):]
			phrase, rest := phrases[sub[1]], strings.TrimPrefix(trimmed, "/")
			clone, inRepo, _ := strings.Cut(rest, "/")
			inRepo = strings.Trim(inRepo, "/")
			switch {
			case !commitSegment.MatchString(clone) || inRepo == "":
				return phrase + dots
			case sub[1] == n.stackDir:
				return n.inTree(inRepo) + dots
			}
			return exports.Value(inRepo, false) + " in " + phrase + dots
		})
	}
	text = n.inStack.ReplaceAllStringFunc(text, func(m string) string {
		p := n.inStack.FindStringSubmatch(m)[1]
		trimmed := strings.TrimRight(p, ".")
		return n.inTree(trimmed) + p[len(trimmed):]
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

// HideError is err with its text as [Named.Hide] prints it; it unwraps to err, so a
// caller still matches the error under it. nil, and an error whose text has nothing to
// hide, are returned as they are.
func (n *Named) HideError(err error) error {
	if err == nil {
		return nil
	}
	text := n.Hide(err.Error())
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

// hashedName is the name of a cache folder or a refs file of a named stack's source v,
// a URL or a ref: 32 hexadecimal characters of v's SHA-256, so no path contains v.
func hashedName(v string) string {
	sum := sha256.Sum256([]byte(v))
	return hex.EncodeToString(sum[:16])
}

// named is the Named of opts, nil for a compose of no named stack.
func named(opts Options) *Named {
	if opts.Git == nil {
		return nil
	}
	return opts.Git.Named
}

// shownAs is s as a text of a resolve with opts shows it: [Options.Name] for a named
// stack's source, a source of the stack core when it is empty, and otherwise s's URL
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
