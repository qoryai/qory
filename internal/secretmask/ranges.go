package secretmask

import (
	"bytes"

	"github.com/qoryai/forager/mask"
)

// Range is a piece of a text that [Set.Replace] replaces: the bytes from Start to End become
// Put.
type Range struct {
	Start, End int
	Put        []byte
}

// Ranges is what [Set.Replace] replaces in text, in order, in its two steps: known, the values
// and the key shapes of Forager's masker, in text; and query, the query values, in text with
// known replaced. Text with known replaced is the masker's Replace of text, byte for byte.
func (s *Set) Ranges(text []byte) (known, query []Range) {
	out := text
	if s != nil {
		s.mu.RLock()
		out = []byte(s.m.Replace(string(text)))
		known = knownRanges(s.m, text, out)
		s.mu.RUnlock()
	}
	for _, r := range queryRanges(out) {
		query = append(query, Range{r[0], r[1], []byte(Placeholder)})
	}
	return known, query
}

// knownRanges is the ranges that m replaces in text, from its scan done again, with the puts of
// out, m's Replace of text. If the two differ, as when m changed between the scans, one range
// covers text from the first byte that out changes.
func knownRanges(m *mask.Masker, text, out []byte) []Range {
	if bytes.Equal(text, out) {
		return nil
	}
	if rs, ok := withPuts(text, out, scanRanges(m.Finds(), text)); ok {
		return rs
	}
	n := 0
	for n < len(text) && n < len(out) && text[n] == out[n] {
		n++
	}
	return []Range{{n, len(text), out[n:]}}
}

// withPuts is spans, the ranges of text that out replaces, with the put of each from out; ok is
// false when text with those ranges replaced is not out. A put is [mask.Secret] or
// [mask.ProxyPassword], or any text for the last range.
func withPuts(text, out []byte, spans [][2]int) (rs []Range, ok bool) {
	at, from := 0, 0
	for k, sp := range spans {
		if !bytes.HasPrefix(out[at:], text[from:sp[0]]) {
			return nil, false
		}
		at += sp[0] - from
		put := -1
		if k == len(spans)-1 {
			if rest := text[sp[1]:]; len(out)-at >= len(rest) && bytes.HasSuffix(out, rest) {
				put = len(out) - at - len(rest)
			}
		} else {
			next := text[sp[1]:spans[k+1][0]]
			for _, p := range []string{mask.Secret, mask.ProxyPassword} {
				if bytes.HasPrefix(out[at:], []byte(p)) && bytes.HasPrefix(out[at+len(p):], next) {
					put = len(p)
					break
				}
			}
		}
		if put < 0 {
			return nil, false
		}
		rs = append(rs, Range{sp[0], sp[1], out[at : at+put]})
		at, from = at+put, sp[1]
	}
	return rs, len(spans) > 0
}

// shapeFind is a [mask.Shape] as scanRanges looks for it.
type shapeFind struct {
	mask.Shape
	alpha [256]bool
}

// shapeFinds is [mask.Shapes], the shapes of each masker that [mask.New] makes.
var shapeFinds = func() []shapeFind {
	var out []shapeFind
	for _, s := range mask.Shapes() {
		f := shapeFind{Shape: s}
		for i := 0; i < len(s.Alphabet); i++ {
			f.alpha[s.Alphabet[i]] = true
		}
		out = append(out, f)
	}
	return out
}()

// index is the first place in in where the shape's prefix starts, -1 for none.
func (f *shapeFind) index(in []byte) int {
	if !f.AnyCase {
		return bytes.Index(in, []byte(f.Prefix))
	}
	for i := 0; i+len(f.Prefix) <= len(in); i++ {
		if bytes.EqualFold(in[i:i+len(f.Prefix)], []byte(f.Prefix)) {
			return i
		}
	}
	return -1
}

// at is the length of the shape's find at the start of in, 0 for none.
func (f *shapeFind) at(in []byte) int {
	p := len(f.Prefix)
	if len(in) < p || f.AnyCase && !bytes.EqualFold(in[:p], []byte(f.Prefix)) || !f.AnyCase && string(in[:p]) != f.Prefix {
		return 0
	}
	if f.Blank {
		g := 0
		for g < mask.MaxBlank && p+g < len(in) && (in[p+g] == ' ' || in[p+g] == '\t') {
			g++
		}
		if g == 0 || p+g == len(in) {
			return 0
		}
		p += g
	}
	t := 0
	for t < f.Max && p+t < len(in) && f.alpha[in[p+t]] {
		t++
	}
	if t < f.Min {
		return 0
	}
	return p + t
}

// next is the start and the length of the shape's first find at or after from, start -1 for
// none.
func (f *shapeFind) next(in []byte, from int) (start, n int) {
	for from < len(in) {
		j := f.index(in[from:])
		if j < 0 {
			break
		}
		j += from
		if n := f.at(in[j:]); n > 0 {
			return j, n
		}
		from = j + 1
	}
	return -1, 0
}

// indexFrom is the index in b of f at or after from, or -1 when there is none.
func indexFrom(b []byte, from int, f []byte) int {
	if j := bytes.Index(b[from:], f); j >= 0 {
		return from + j
	}
	return -1
}

// scanRanges is the ranges that a masker with the finds and [shapeFinds] replaces in in, as its
// scan with final set finds them: the first find, the longest of those at one place, widened
// over every find that starts inside it.
func scanRanges(finds [][]byte, in []byte) [][2]int {
	var out [][2]int
	at := make([]int, len(finds))
	for k := range finds {
		at[k] = indexFrom(in, 0, finds[k])
	}
	sat, slen := make([]int, len(shapeFinds)), make([]int, len(shapeFinds))
	for k := range shapeFinds {
		sat[k], slen[k] = shapeFinds[k].next(in, 0)
	}
	for i := 0; ; {
		start, n := -1, 0
		for k := range finds {
			if at[k] >= 0 && at[k] < i {
				at[k] = indexFrom(in, i, finds[k])
			}
			if at[k] >= 0 && (start < 0 || at[k] < start || at[k] == start && len(finds[k]) > n) {
				start, n = at[k], len(finds[k])
			}
		}
		for k := range shapeFinds {
			if sat[k] >= 0 && sat[k] < i {
				sat[k], slen[k] = shapeFinds[k].next(in, i)
			}
			if sat[k] >= 0 && (start < 0 || sat[k] < start || sat[k] == start && slen[k] > n) {
				start, n = sat[k], slen[k]
			}
		}
		if start < 0 {
			return out
		}
		i = widen(finds, in, start, start+n)
		out = append(out, [2]int{start, i})
	}
}

// widen is the end of the range from start to end once every find that starts inside it, at
// start or after it, extends it to its end, until none does.
func widen(finds [][]byte, in []byte, start, end int) int {
	next := make([]int, len(finds))
	snext := make([]int, len(shapeFinds))
	for k := range next {
		next[k] = start
	}
	for k := range snext {
		snext[k] = start
	}
	for grown := true; grown; {
		grown = false
		for k, f := range finds {
			n := len(f)
			for {
				limit := min(len(in), end+n-1)
				if next[k]+n > limit {
					break
				}
				j := bytes.Index(in[next[k]:limit], f)
				if j < 0 {
					next[k] = limit - n + 1
					break
				}
				j += next[k]
				next[k] = j + 1
				if j+n > end {
					end, grown = j+n, true
				}
			}
		}
		for k := range shapeFinds {
			sh := &shapeFinds[k]
			n := len(sh.Prefix)
			for {
				limit := min(len(in), end+n-1)
				if snext[k]+n > limit {
					break
				}
				j := sh.index(in[snext[k]:limit])
				if j < 0 {
					snext[k] = limit - n + 1
					break
				}
				j += snext[k]
				snext[k] = j + 1
				if f := sh.at(in[j:]); f > 0 && j+f > end {
					end, grown = j+f, true
				}
			}
		}
	}
	return end
}
