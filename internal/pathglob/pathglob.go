// Package pathglob matches the globs of an agent's paths against a changed file, as the
// harness contract defines them (contracts/harness/v1/README.md, the paths key). The
// harness's JavaScript matcher and this package share the cases of
// contracts/harness/v1/path-cases.json.
package pathglob

import "strings"

// kind is the kind of a token in a list of path parts.
type kind int

const (
	// literal is a part that matches one part of a path, by [matchPart].
	literal kind = iota
	// anyParts is a ** part: it matches any number of parts.
	anyParts
	// oneUp is the part below a last ** part: it matches exactly one part.
	oneUp
)

// token is one part of a glob.
type token struct {
	kind  kind
	chars []rune
}

// Match reports whether pattern matches file, a path from the checkout root with
// forward slashes. `**` as a whole part matches any number of directories, `*` and `?`
// match within one part, and every other character is literal, case-sensitive. A glob
// without a slash matches only at the top. The time is at most the product of the
// lengths of pattern and file, since the match goes back to one point only.
//
// shortcut: a last ** part needs one part or more (src/** does not match src), and **
// inside a part matches as *; the README is silent on both, and the harness matcher does
// the same. Replace when the README decides.
func Match(pattern, file string) bool {
	parts := strings.Split(pattern, "/")
	var tokens []token
	for i, part := range parts {
		switch {
		case part != "**":
			tokens = append(tokens, token{kind: literal, chars: []rune(part)})
		case i > 0 && parts[i-1] == "**":
			// Two ** parts in a row match what one does.
		case i == len(parts)-1 && i > 0:
			tokens = append(tokens, token{kind: oneUp}, token{kind: anyParts})
		default:
			tokens = append(tokens, token{kind: anyParts})
		}
	}
	items := strings.Split(file, "/")
	return wild(tokens, items,
		func(t token) bool { return t.kind == anyParts },
		func(t token) bool { return t.kind == oneUp },
		func(t token, item string) bool { return matchPart(t.chars, item) })
}

// matchPart reports whether the characters of a glob part, with * and ?, match one part
// of a path in full.
func matchPart(chars []rune, part string) bool {
	return wild(chars, []rune(part),
		func(c rune) bool { return c == '*' },
		func(c rune) bool { return c == '?' },
		func(c, item rune) bool { return c == item })
}

// wild reports whether tokens match items in full. isStar picks the token that matches
// any number of items, isOne the token that matches exactly one item, and same matches
// any other token against one item. On a mismatch it goes back to the last star and lets
// that star take one more item, which is correct for one kind of star.
func wild[T, I any](tokens []T, items []I, isStar, isOne func(T) bool, same func(T, I) bool) bool {
	at, from, back, mark := 0, 0, -1, 0
	for from < len(items) {
		switch {
		case at < len(tokens) && !isStar(tokens[at]) && (isOne(tokens[at]) || same(tokens[at], items[from])):
			at++
			from++
		case at < len(tokens) && isStar(tokens[at]):
			back = at
			at++
			mark = from
		case back != -1:
			at = back + 1
			mark++
			from = mark
		default:
			return false
		}
	}
	for at < len(tokens) && isStar(tokens[at]) {
		at++
	}
	return at == len(tokens)
}
