package prebuild

import (
	"bytes"
	"regexp"
	"slices"
	"strings"
)

// PublicHosts are the hosts of public images whose copies the registry keeps under
// upstream/<host>/<path>: Docker Hub, GitHub's, Microsoft's and Quay's registries.
var PublicHosts = []string{"docker.io", "ghcr.io", "mcr.microsoft.com", "quay.io"}

// dockerHubNames are the names of Docker Hub's registry in a reference or a credential.
var dockerHubNames = []string{"docker.io", "index.docker.io", "registry-1.docker.io"}

// hostOf is a host as the build compares hosts: in lower case, without the default
// port :443, and each of Docker Hub's names as docker.io.
func hostOf(h string) string {
	h = strings.TrimSuffix(strings.ToLower(h), ":443")
	if slices.Contains(dockerHubNames, h) {
		return "docker.io"
	}
	return h
}

// imageRef is an image reference, split: its registry's host, as [hostOf] has it, its
// path, and the rest as written, a tag, a digest, both or nothing.
type imageRef struct {
	host, path, rest string
}

// name is the reference without its rest: the host and the path.
func (r imageRef) name() string { return r.host + "/" + r.path }

// digest is the rest's @sha256:... part, "" when the reference has no digest.
func (r imageRef) digest() string {
	if i := strings.IndexByte(r.rest, '@'); i >= 0 {
		return r.rest[i:]
	}
	return ""
}

// parseRef splits an image reference as the engine reads one: a first part with no dot,
// no colon and other than localhost is a path on Docker Hub, and a path of one part on
// Docker Hub is under library/. ok is false for a reference whose name, before its tag
// or digest, contains a variable, $, or is empty; a variable in the tag is kept in rest.
func parseRef(ref string) (imageRef, bool) {
	name, rest, ok := cutName(ref)
	if !ok {
		return imageRef{}, false
	}
	host, path, found := strings.Cut(name, "/")
	if !found || (!strings.ContainsAny(host, ".:") && host != "localhost") {
		host, path = "docker.io", name
	}
	host = hostOf(host)
	if host == "docker.io" && !strings.Contains(path, "/") {
		path = "library/" + path
	}
	return imageRef{host: host, path: path, rest: rest}, true
}

// cutName cuts ref before its tag or digest: at the first : or @ after its last /. ok is
// false when the name is empty or contains a $.
func cutName(ref string) (name, rest string, ok bool) {
	head := ref
	if i := strings.IndexByte(ref, '$'); i >= 0 {
		head = ref[:i]
	}
	slash := strings.LastIndexByte(head, '/')
	switch s := strings.IndexAny(head[slash+1:], ":@"); {
	case s >= 0:
		name, rest = ref[:slash+1+s], ref[slash+1+s:]
	case len(head) == len(ref):
		name = ref
	default:
		return "", "", false
	}
	return name, rest, name != "" && !strings.HasPrefix(name, "/")
}

// upstream rewrites image references to the registry's copies of public images.
type upstream struct {
	// registry is the registry's address, a host and a port or none; "" rewrites
	// nothing.
	registry string
	// credentialed are the hosts that have a credential in the build's run
	// configuration, as [hostOf] has them.
	credentialed []string
}

// rewrite is r as the registry's copy, <registry>/upstream/<host>/<path> and r's rest;
// ok is false for a host that is not one of [PublicHosts], that has a credential, or that
// is the registry's.
func (u upstream) rewrite(r imageRef) (string, bool) {
	if u.registry == "" || !slices.Contains(PublicHosts, r.host) || slices.Contains(u.credentialed, r.host) || hostOf(u.registry) == r.host {
		return "", false
	}
	return u.registry + "/upstream/" + r.host + "/" + r.path + r.rest, true
}

// image is ref, the image of devcontainer.json or of a Dockerfile, as [upstream.rewrite]
// has it, or ref.
func (u upstream) image(ref string) string {
	if r, ok := parseRef(ref); ok {
		if out, ok := u.rewrite(r); ok {
			return out
		}
	}
	return ref
}

// dfWord is a word of a Dockerfile's instruction, and where it is in the Dockerfile.
type dfWord struct {
	text       string
	start, end int
}

// heredocStart is a word that starts a here-document in an instruction, as BuildKit
// reads one: a file descriptor or none, <<, or <<- which strips the tabs before each
// line, and a word, quoted or not, that ends it.
var heredocStart = regexp.MustCompile(`^\d*<<(-?)(["']?)([A-Za-z0-9_.-]+)(["']?)$`)

// dockerfileInstructions are the instructions of data, a Dockerfile, each as its words,
// its keyword first, as BuildKit reads the lines (buildkit v0.34.0
// frontend/dockerfile/parser/parser.go, Parse): an instruction runs on past each line
// that ends with the escape character, \ or the one the escape directive sets, and
// spaces or tabs, and the next line is joined to it in the escape character's place. A
// comment line or an empty line among them is left out. The bodies of here-documents in
// RUN, COPY and ADD are skipped. A word runs to the next space or tab; quotes are not
// read, which no image reference in FROM or COPY --from needs.
func dockerfileInstructions(data []byte) [][]dfWord {
	escape := byte('\\')
	for line := range bytes.Lines(data) {
		rest, ok := bytes.CutPrefix(bytes.TrimLeft(line, " \t"), []byte("#"))
		if !ok {
			break
		}
		m := directiveLine.FindSubmatch(bytes.TrimSpace(rest))
		if m == nil {
			break
		}
		if strings.EqualFold(string(m[1]), "escape") && (string(m[2]) == "`" || string(m[2]) == `\`) {
			escape = m[2][0]
		}
	}
	var (
		out       [][]dfWord
		joined    []byte
		at        []int
		going     bool
		heredocs  []string
		stripTabs []bool
	)
	for off := 0; off < len(data); {
		end := bytes.IndexByte(data[off:], '\n')
		next := off + end + 1
		if end < 0 {
			end, next = len(data)-off, len(data)
		}
		line := bytes.TrimSuffix(data[off:off+end], []byte("\r"))
		start := off
		off = next
		if !going && len(heredocs) > 0 {
			body := string(line)
			if stripTabs[0] {
				body = strings.TrimLeft(body, "\t")
			}
			if body == heredocs[0] {
				heredocs, stripTabs = heredocs[1:], stripTabs[1:]
			}
			continue
		}
		trimmed := bytes.TrimLeft(line, " \t")
		if len(trimmed) == 0 || trimmed[0] == '#' {
			continue
		}
		if !going {
			start += len(line) - len(trimmed)
			line = trimmed
		}
		body := bytes.TrimRight(line, " \t")
		going = len(body) > 0 && body[len(body)-1] == escape
		if going {
			line = body[:len(body)-1]
		}
		joined = append(joined, line...)
		for k := range line {
			at = append(at, start+k)
		}
		if going {
			continue
		}
		if cur := words(joined, at); len(cur) > 0 {
			switch strings.ToUpper(cur[0].text) {
			case "RUN", "COPY", "ADD":
				for _, w := range cur[1:] {
					if m := heredocStart.FindStringSubmatch(w.text); m != nil && m[2] == m[4] {
						heredocs = append(heredocs, m[3])
						stripTabs = append(stripTabs, m[1] == "-")
					}
				}
			}
			out = append(out, cur)
		}
		joined, at = nil, nil
	}
	if cur := words(joined, at); len(cur) > 0 {
		out = append(out, cur)
	}
	return out
}

// words are the words of line, cut at spaces and tabs, where at is the offset in the
// Dockerfile of each byte of line. A word on two lines runs from its first byte to its
// last, the escape character and the line break between them included.
func words(line []byte, at []int) []dfWord {
	var out []dfWord
	for i := 0; i < len(line); {
		if line[i] == ' ' || line[i] == '\t' {
			i++
			continue
		}
		j := i
		for j < len(line) && line[j] != ' ' && line[j] != '\t' {
			j++
		}
		out = append(out, dfWord{text: string(line[i:j]), start: at[i], end: at[j-1] + 1})
		i = j
	}
	return out
}

// dfStage is a stage of a Dockerfile: the word its FROM starts from, its name in lower
// case, "" when it has none, and the value of its FROM's --platform, "" for none.
type dfStage struct {
	from     dfWord
	name     string
	platform string
}

// dockerfileStages are the stages of a Dockerfile's instructions, and the words of the
// ARG instructions before the first FROM.
func dockerfileStages(ins [][]dfWord) (stages []dfStage, globals []dfWord) {
	for _, in := range ins {
		switch strings.ToUpper(in[0].text) {
		case "ARG":
			if len(stages) == 0 {
				globals = append(globals, in[1:]...)
			}
		case "FROM":
			s := dfStage{}
			rest := in[1:]
			for len(rest) > 0 && strings.HasPrefix(rest[0].text, "--") {
				if v, ok := strings.CutPrefix(rest[0].text, "--platform="); ok {
					s.platform = v
				}
				rest = rest[1:]
			}
			if len(rest) == 0 {
				continue
			}
			s.from = rest[0]
			if len(rest) >= 3 && strings.EqualFold(rest[1].text, "AS") {
				s.name = strings.ToLower(rest[2].text)
			}
			stages = append(stages, s)
		}
	}
	return stages, globals
}

// isStage reports whether ref, written in FROM or COPY --from, is the name of one of
// stages, or scratch, which no registry serves.
func isStage(ref string, stages []dfStage) bool {
	lower := strings.ToLower(ref)
	return lower == "scratch" || slices.ContainsFunc(stages, func(s dfStage) bool { return s.name != "" && s.name == lower })
}

// rewriteDockerfile is data, a Dockerfile, with the image of each FROM and of each COPY
// --from as [upstream.rewrite] has it, and each rewrite made, the reference as written
// and the copy, once, in order. The name of a stage defined before a FROM, the name of
// any stage in COPY --from, a stage's index, scratch and an image whose name contains a
// variable are left as they are.
func (u upstream) rewriteDockerfile(data []byte) ([]byte, [][2]string) {
	ins := dockerfileInstructions(data)
	stages, _ := dockerfileStages(ins)
	type place struct {
		w      dfWord
		stages []dfStage
	}
	var at []place
	for i, s := range stages {
		at = append(at, place{s.from, stages[:i]})
	}
	for _, in := range ins {
		if !strings.EqualFold(in[0].text, "COPY") {
			continue
		}
		for _, w := range in[1:] {
			if !strings.HasPrefix(w.text, "--") {
				break
			}
			if v, ok := strings.CutPrefix(w.text, "--from="); ok && !isIndex(v) {
				at = append(at, place{dfWord{text: v, start: w.start + len("--from="), end: w.end}, stages})
			}
		}
	}
	slices.SortFunc(at, func(a, b place) int { return a.w.start - b.w.start })
	var (
		out  bytes.Buffer
		done [][2]string
		last int
	)
	for _, p := range at {
		w := p.w
		if isStage(w.text, p.stages) {
			continue
		}
		r, ok := parseRef(w.text)
		if !ok {
			continue
		}
		copied, ok := u.rewrite(r)
		if !ok {
			continue
		}
		out.Write(data[last:w.start])
		out.WriteString(copied)
		last = w.end
		if !slices.Contains(done, [2]string{w.text, copied}) {
			done = append(done, [2]string{w.text, copied})
		}
	}
	if len(done) == 0 {
		return data, nil
	}
	out.Write(data[last:])
	return out.Bytes(), done
}

// isIndex reports whether s is a stage's index, a number.
func isIndex(s string) bool {
	return s != "" && strings.Trim(s, "0123456789") == ""
}

// dockerfileBase is the image the build of data, a Dockerfile, starts from: the image of
// the stage target, or of the last stage when target is "", following the stages
// defined before it that it starts from, with the variables of the ARGs before the first
// FROM replaced, args, the build arguments, first. engine is the platform the build
// runs on, os/arch: BuildKit's platform arguments, BUILDPLATFORM, TARGETPLATFORM and
// their OS and ARCH parts, are set from it, as the build sets no --platform, unless an
// ARG or a build argument sets them. platform is the --platform of that image's FROM, its
// variables replaced, "" for none. ok is false for scratch, and for an image or a
// --platform with a variable that has no value or that the build does not read.
func dockerfileBase(data []byte, target string, args map[string]string, engine string) (ref, platform string, ok bool) {
	stages, globals := dockerfileStages(dockerfileInstructions(data))
	if len(stages) == 0 {
		return "", "", false
	}
	vars := map[string]string{}
	for _, w := range globals {
		name, value, set := strings.Cut(w.text, "=")
		if v, ok := args[name]; ok {
			value, set = v, true
		}
		if set {
			vars[name] = unquote(value)
		}
	}
	// A platform argument an ARG or a build argument sets has no known value: whether
	// BuildKit takes it or its own is not read here.
	if goos, goarch, found := strings.Cut(engine, "/"); found {
		for _, p := range []string{"BUILD", "TARGET"} {
			for name, value := range map[string]string{p + "PLATFORM": engine, p + "OS": goos, p + "ARCH": goarch} {
				_, declared := vars[name]
				if _, given := args[name]; declared || given {
					delete(vars, name)
				} else {
					vars[name] = value
				}
			}
		}
	}
	i := len(stages) - 1
	if target != "" {
		i = slices.IndexFunc(stages, func(s dfStage) bool { return s.name == strings.ToLower(target) })
	}
	for i >= 0 {
		s := stages[i]
		ref, ok := expandArgs(s.from.text, vars)
		if !ok {
			return "", "", false
		}
		if strings.EqualFold(ref, "scratch") {
			return "", "", false
		}
		if !isStage(ref, stages[:i]) {
			platform, ok := s.platform, true
			if strings.Contains(platform, "$") {
				platform, ok = expandArgs(platform, vars)
			}
			if !ok {
				return "", "", false
			}
			return ref, platform, true
		}
		i = slices.IndexFunc(stages[:i], func(p dfStage) bool { return p.name == strings.ToLower(ref) })
	}
	return "", "", false
}

// unquote is v without the double or single quotes around it.
func unquote(v string) string {
	if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
		return v[1 : len(v)-1]
	}
	return v
}

// expandArgs is s with its variables replaced from vars, as BuildKit replaces them in
// FROM: $NAME, ${NAME}, ${NAME:-word}, ${NAME-word}, ${NAME:+word} and ${NAME+word}. ok
// is false for a variable vars has no value for, used without a default, for any other
// form, for a word that contains a variable, and for a result that contains a $.
func expandArgs(s string, vars map[string]string) (string, bool) {
	var b strings.Builder
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == '\\' && i+1 < len(s) && s[i+1] == '$':
			b.WriteByte('$')
			i += 2
			continue
		case c != '$':
			b.WriteByte(c)
			i++
			continue
		}
		if i+1 < len(s) && s[i+1] == '{' {
			j := strings.IndexByte(s[i+2:], '}')
			if j < 0 {
				return "", false
			}
			inner := s[i+2 : i+2+j]
			i += j + 3
			k := nameLen(inner)
			name, op := inner[:k], inner[k:]
			v, set := vars[name]
			var word string
			for _, p := range []string{":-", ":+", "-", "+"} {
				if w, ok := strings.CutPrefix(op, p); ok {
					op, word = p, w
					break
				}
			}
			if k == 0 || strings.Contains(word, "$") {
				return "", false
			}
			switch op {
			case "":
				if !set {
					return "", false
				}
				b.WriteString(v)
			case ":-":
				if set && v != "" {
					word = v
				}
				b.WriteString(word)
			case "-":
				if set {
					word = v
				}
				b.WriteString(word)
			case ":+":
				if set && v != "" {
					b.WriteString(word)
				}
			case "+":
				if set {
					b.WriteString(word)
				}
			default:
				return "", false
			}
			continue
		}
		k := nameLen(s[i+1:])
		v, set := vars[s[i+1:i+1+k]]
		if k == 0 || !set {
			return "", false
		}
		b.WriteString(v)
		i += 1 + k
	}
	out := b.String()
	return out, !strings.Contains(out, "$")
}

// nameLen is how long the variable's name at the start of s is: letters, digits and _.
func nameLen(s string) int {
	k := 0
	for k < len(s) && (s[k] == '_' || s[k] >= 'a' && s[k] <= 'z' || s[k] >= 'A' && s[k] <= 'Z' || s[k] >= '0' && s[k] <= '9') {
		k++
	}
	return k
}
