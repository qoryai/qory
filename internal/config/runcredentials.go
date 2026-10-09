package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/qoryai/forager/contracts"
	"github.com/qoryai/forager/runcredential"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/santhosh-tekuri/jsonschema/v6/kind"
	"gopkg.in/yaml.v3"
)

// runCredentialsDoc is the name gateway.run_credentials is parsed under, which selects
// JSON.
const runCredentialsDoc = "run_credentials.json"

// runCredentialsBudget is the most JSON, in bytes, gateway.run_credentials written with
// aliases may make with each alias written out: far more than a list of issuers takes.
const runCredentialsBudget = 4 << 20

// Heartbeat is the heartbeat interval of every gateway qory starts, qory run's own and
// qory gateway, Forager's default; it is also how long an introspection answer holds
// when an issuer sets no cache.
const Heartbeat = 30 * time.Second

// readRunCredentials reads gateway.run_credentials, the list of issuers, against
// Forager's run-credentials.schema.json, and the rows qory config lists of it, each
// value as the file writes it. It reads no file the list names: qory gateway hands the
// list to Forager, which checks the keys and the secrets' files before it listens.
func readRunCredentials(path string, node *yaml.Node) (runcredential.Issuers, []Row, error) {
	// Forager reads the list as JSON, and its messages quote what it cannot read: a
	// value whose tag it does not fit, or a number JSON cannot represent, is refused
	// here first, by its key, and the schema's report has its values left out. A walk
	// that reached its limit leaves the list to the decoder, under safeDecode.
	if f := findFault(node, reflect.TypeFor[any](), "gateway.run_credentials", false); f != nil && f != walkLimited {
		return nil, nil, f.error(path)
	}
	notIssuers := fmt.Errorf("%s: gateway.run_credentials is not a list of issuers as run-credentials.schema.json defines them", path)
	// The list is decoded where it stands in the file, so an alias in it of an anchor
	// elsewhere in the file is the value the anchor holds, as Forager would read it in
	// the file as a whole; the decoder's limits of aliases hold.
	var list any
	if err := safeDecode(func() error { return node.Decode(&list) }); err != nil {
		if errors.Is(err, errDecoderFailed) {
			return nil, nil, fmt.Errorf("%s: gateway.run_credentials: %w", path, err)
		}
		if text, ok := aliasRefusal(err); ok {
			return nil, nil, fmt.Errorf("%s: gateway.run_credentials: %s", path, text)
		}
		return nil, nil, notIssuers
	}
	// A key written as an alias is checked on the list as written, while it is still
	// an alias: the schema's report would name it by its anchor's value.
	schema, err := runCredentialsSchema()
	if err != nil {
		return nil, nil, notIssuers
	}
	keys := newAliasKeys()
	if f := keys.fault(node, schema); f != nil {
		return nil, nil, f.error(path)
	}
	// The decoder holds an alias's value once, however often the alias stands, and the
	// JSON of it once for each time: a list whose aliases make more JSON than
	// runCredentialsBudget is refused. A cheap lower bound of the JSON's length stops most
	// such lists before their JSON is made, and the exact length after json.Marshal
	// refuses the rest.
	aliased := holdsAlias(node)
	if aliased && jsonOver(list, runCredentialsBudget) {
		return nil, nil, fmt.Errorf("%s: gateway.run_credentials: %s", path, tooManyAliases)
	}
	// Forager reads a YAML list as the JSON of what the decoder makes of it, which is
	// the JSON it is handed here.
	b, err := json.Marshal(list)
	if err != nil {
		return nil, nil, notIssuers
	}
	if aliased && len(b) > runCredentialsBudget {
		return nil, nil, fmt.Errorf("%s: gateway.run_credentials: %s", path, tooManyAliases)
	}
	issuers, err := runcredential.Parse(runCredentialsDoc, b)
	if err != nil {
		var ve *jsonschema.ValidationError
		if !errors.As(err, &ve) || keys.reportNamesAlias(node, ve) {
			return nil, nil, notIssuers
		}
		valueFree(ve)
		return nil, nil, fmt.Errorf("%s: gateway.run_credentials: %s", path, ve.Error())
	}
	// The rows are of the list as qory read it: each alias followed and each merge
	// applied, the same rows as of the list written out in full.
	var rows []Row
	for i, item := range resolved(node).Content {
		key := fmt.Sprintf("gateway.run_credentials[%d].", i)
		members := map[string]*yaml.Node{}
		for k := 0; k+1 < len(item.Content); k += 2 {
			members[item.Content[k].Value] = item.Content[k+1]
		}
		for _, name := range []string{"issuer", "audience", "algorithms", "keys", "leeway", "max_lifetime", "allow", "labels", "details"} {
			v, ok := members[name]
			switch {
			case ok:
				rows = append(rows, Row{key + name, written(v), path})
			case name == "leeway":
				rows = append(rows, Row{key + name, fmt.Sprintf("%.0fs", runcredential.DefaultLeeway.Seconds()), Default})
			case name == "max_lifetime" || name == "allow":
				rows = append(rows, Row{key + name, "(none)", Default})
			}
		}
		in, ok := members["introspection"]
		if !ok {
			rows = append(rows, Row{key + "introspection", "(none)", Default})
			continue
		}
		fields := map[string]*yaml.Node{}
		for k := 0; k+1 < len(in.Content); k += 2 {
			fields[in.Content[k].Value] = in.Content[k+1]
		}
		for _, name := range []string{"url", "client_id", "client_secret_file", "cache"} {
			if v, ok := fields[name]; ok {
				rows = append(rows, Row{key + "introspection." + name, written(v), path})
			} else if name == "cache" {
				rows = append(rows, Row{key + "introspection." + name, fmt.Sprintf("%.0fs", Heartbeat.Seconds()), Default})
			}
		}
	}
	return issuers, rows, nil
}

// resolved is a copy of n, and of everything under it, as the decoder reads it: each
// alias is a copy of its anchor's value, without the anchor, and each mapping holds the
// keys it merges where its << stands, without those the mapping sets itself or an
// earlier merge sets. A key tagged !!merge that the decoder takes as no merge, such as
// !!merge name or an alias of <<, is the name it decodes into. It is n as the file
// would write it out in full.
func resolved(n *yaml.Node) *yaml.Node {
	n = followAliases(n)
	c := *n
	c.Anchor, c.Content = "", nil
	switch n.Kind {
	case yaml.SequenceNode:
		for _, item := range n.Content {
			c.Content = append(c.Content, resolved(item))
		}
	case yaml.MappingNode:
		for _, kv := range mergedKeys(n) {
			c.Content = append(c.Content, resolvedKey(kv.k), resolved(kv.v))
		}
	}
	return &c
}

// resolvedKey is [resolved] of k, a key that is no merge key. A key tagged !!merge is
// the string it decodes into, untagged: written out as it stands, it would be a merge
// where it is a <<, and a name with a tag the decoder drops where it is another. A <<
// is quoted, which the YAML encoder does not do of its own.
func resolvedKey(k *yaml.Node) *yaml.Node {
	c := resolved(k)
	if c.Kind != yaml.ScalarNode || !mergeTagged(c) {
		return c
	}
	if name, ok := keyString(k); ok {
		c.Tag, c.Value, c.Style = "!!str", name, 0
		if name == "<<" {
			c.Style = yaml.DoubleQuotedStyle
		}
	}
	return c
}

// mergedKeys are the keys of the mapping m and their values as the decoder takes them,
// in the order the file writes them, the keys of the mappings its << merges where the
// << stands: a key m sets itself wins over every merged one, and of two keys of one
// name, the later. A merged mapping sets a name no key outside it has set, its own keys
// before those it merges in turn, and of two mappings a << lists, the first wins. The
// decoder counts m's << among the names m sets, so a merged key named << is dropped, as
// is a merged null key, which a mapping of names takes as no key.
func mergedKeys(m *yaml.Node) []keyValue {
	return appendMergedKeys(nil, m, map[string]bool{}, true)
}

// appendMergedKeys appends the keys of the mapping m to out, as [mergedKeys] takes them,
// and returns out: each mapping is walked once for each time it is merged, and each key
// appended once, into the one list. taken are the names a key has set, m's own among
// them before any mapping m merges is walked. own is set for the mapping merged into
// none, where the later of two keys of one name wins, and whose << is taken when it
// merges; in a merged mapping, the first, a name taken before is left to the key that
// took it, and a null key is no key.
func appendMergedKeys(out []keyValue, m *yaml.Node, taken map[string]bool, own bool) []keyValue {
	at := map[string]int{}
	merge := -1
	for i := 0; i+1 < len(m.Content); i += 2 {
		k := m.Content[i]
		if isMergeKey(k) {
			merge = i
			continue
		}
		if !own && followAliases(k).ShortTag() == "!!null" {
			continue
		}
		name, ok := keyString(k)
		if !ok || !own && taken[name] {
			continue
		}
		at[name] = i
		taken[name] = true
	}
	if own && merge >= 0 {
		taken["<<"] = true
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		k := m.Content[i]
		if i == merge {
			for _, mm := range mergedMappings(m.Content[i+1]) {
				out = appendMergedKeys(out, mm, taken, false)
			}
			continue
		}
		if isMergeKey(k) {
			continue
		}
		if name, ok := keyString(k); ok {
			if j, ok := at[name]; ok && j == i {
				out = append(out, keyValue{k, m.Content[i+1]})
			}
		}
	}
	return out
}

// holdsAlias reports whether n, or anything under it as the file writes it, is an
// alias.
func holdsAlias(n *yaml.Node) bool {
	if n.Kind == yaml.AliasNode {
		return true
	}
	for _, c := range n.Content {
		if holdsAlias(c) {
			return true
		}
	}
	return false
}

// jsonOver reports whether a lower bound of the JSON of v, a value the decoder made, is
// over limit bytes: true means the JSON takes more, and false that it may or may not. It
// counts the fewest bytes each value takes in JSON, and stops as soon as the count is
// over limit, so a string the decoder shares among many aliases is never read through.
func jsonOver(v any, limit int) bool {
	n := 0
	var over func(v any) bool
	over = func(v any) bool {
		switch v := v.(type) {
		case string:
			n += len(v) + 2
		case []any:
			n += 2
			for _, e := range v {
				if over(e) {
					return true
				}
			}
		case map[string]any:
			n += 2
			for k, e := range v {
				if n += len(k) + 3; over(e) {
					return true
				}
			}
		case map[any]any:
			n += 2
			for _, e := range v {
				if n += 3; over(e) {
					return true
				}
			}
		default:
			n++
		}
		return n > limit
	}
	return over(v)
}

// written is a value as the file writes it, on one line: a scalar as it is, anything
// else in YAML's flow style, without comments.
func written(n *yaml.Node) string {
	if n.Kind == yaml.ScalarNode {
		return n.Value
	}
	flow := flowCopy(n)
	b, err := yaml.Marshal(flow)
	if err != nil {
		return n.Value
	}
	return strings.TrimSpace(string(b))
}

// flowCopy is a copy of n and everything under it in flow style, without comments.
func flowCopy(n *yaml.Node) *yaml.Node {
	c := *n
	c.HeadComment, c.LineComment, c.FootComment = "", "", ""
	if c.Kind == yaml.MappingNode || c.Kind == yaml.SequenceNode {
		c.Style = yaml.FlowStyle
	}
	c.Content = nil
	for _, child := range n.Content {
		c.Content = append(c.Content, flowCopy(child))
	}
	return &c
}

// runCredentialsSchema is Forager's run-credentials.schema.json, compiled once: the
// names [aliasKeys] reads the keys of gateway.run_credentials by.
var runCredentialsSchema = sync.OnceValues(func() (*jsonschema.Schema, error) {
	return contracts.Compile("run-credentials.schema.json")
})

// aliasKeys is the check of the keys of gateway.run_credentials written as aliases,
// made on the list as written, where an alias is still one. The schema's report names a
// key by the name it decodes into, which for an alias key is a value written elsewhere
// in the file: a key whose name the schema allows nowhere where it stands is refused by
// its alias, as forager.yaml's other sections refuse it, and no report is said that
// names an alias key by a name the schema does not itself list there.
type aliasKeys struct {
	// seen are the values walked, each as a schema it is read by.
	seen map[aliasKeysVisit]bool
	// listed are the alias keys walked: true when every schema the key was read by lists
	// its name as one of its properties, false when one allows it among any names.
	listed map[*yaml.Node]bool
	// names are the keys of each mapping by their names, its merges' keys among them.
	names map[*yaml.Node]map[string][]keyValue
}

// aliasKeysVisit is a value as a schema it is read by.
type aliasKeysVisit struct {
	n *yaml.Node
	s *jsonschema.Schema
}

// keyValue is a key of a mapping and its value.
type keyValue struct{ k, v *yaml.Node }

// newAliasKeys is an aliasKeys of nothing walked yet.
func newAliasKeys() *aliasKeys {
	return &aliasKeys{seen: map[aliasKeysVisit]bool{}, listed: map[*yaml.Node]bool{}, names: map[*yaml.Node]map[string][]keyValue{}}
}

// fault is the first key under n, read by the schema s, that is written as an alias of
// a name s does not allow where the key stands; nil when there is none. Each value is
// walked once for each schema it is read by, so the walk ends whatever the aliases
// expand to.
func (a *aliasKeys) fault(n *yaml.Node, s *jsonschema.Schema) *fault {
	n = followAliases(n)
	visit := aliasKeysVisit{n, s}
	if a.seen[visit] {
		return nil
	}
	a.seen[visit] = true
	switch n.Kind {
	case yaml.SequenceNode:
		for i, item := range n.Content {
			for _, is := range itemSchemas(s, i, map[*jsonschema.Schema]bool{}) {
				if f := a.fault(item, is); f != nil {
					return f
				}
			}
		}
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			k, v := n.Content[i], n.Content[i+1]
			if isMergeKey(k) {
				// A merged mapping's keys stand where the mapping that merges it stands.
				for _, m := range mergedMappings(v) {
					if f := a.fault(m, s); f != nil {
						return f
					}
				}
				continue
			}
			name, ok := keyString(k)
			if !ok {
				continue
			}
			if k.Kind == yaml.AliasNode {
				listed, allowed := keyAllowed(s, name, map[*jsonschema.Schema]bool{})
				if !allowed {
					return &fault{k.Line, fmt.Sprintf("key *%s is an alias of a key %s does not read", k.Value, ForagerFileName)}
				}
				if was, ok := a.listed[k]; !ok || was {
					a.listed[k] = listed
				}
			}
			for _, ps := range propertySchemas(s, name, map[*jsonschema.Schema]bool{}) {
				if f := a.fault(v, ps); f != nil {
					return f
				}
			}
		}
	}
	return nil
}

// reportNamesAlias reports whether the schema's report e, of the list n, names a key
// written as an alias by a name that is not one the schema lists where the key stands:
// in the place it names, or as a key it quotes.
func (a *aliasKeys) reportNamesAlias(n *yaml.Node, e *jsonschema.ValidationError) bool {
	if a.aliasOnPath(n, e.InstanceLocation) {
		return true
	}
	var quoted []string
	switch k := e.ErrorKind.(type) {
	case *kind.AdditionalProperties:
		quoted = k.Properties
	case *kind.PropertyNames:
		quoted = []string{k.Property}
	}
	for _, q := range quoted {
		if a.aliasOnPath(n, append(slices.Clip(e.InstanceLocation), q)) {
			return true
		}
	}
	for _, c := range e.Causes {
		if a.reportNamesAlias(n, c) {
			return true
		}
	}
	return false
}

// aliasOnPath reports whether the place loc under n, as the schema's report names it,
// goes through a key written as an alias whose name the schema does not list there, or
// one the check did not walk. Every value the place may stand for is looked at.
func (a *aliasKeys) aliasOnPath(n *yaml.Node, loc []string) bool {
	at := []*yaml.Node{followAliases(n)}
	for _, step := range loc {
		var next []*yaml.Node
		added := map[*yaml.Node]bool{}
		add := func(v *yaml.Node) {
			if v = followAliases(v); !added[v] {
				added[v] = true
				next = append(next, v)
			}
		}
		for _, c := range at {
			switch c.Kind {
			case yaml.SequenceNode:
				if i, err := strconv.Atoi(step); err == nil && i >= 0 && i < len(c.Content) {
					add(c.Content[i])
				}
			case yaml.MappingNode:
				for _, kv := range a.keysOf(c)[step] {
					if kv.k.Kind == yaml.AliasNode && !a.listed[kv.k] {
						return true
					}
					add(kv.v)
				}
			}
		}
		at = next
	}
	return false
}

// keysOf is the keys of the mapping n by the names they decode into, the keys of the
// mappings it merges among them, as the decoder may take any of them for the name.
func (a *aliasKeys) keysOf(n *yaml.Node) map[string][]keyValue {
	if names, ok := a.names[n]; ok {
		return names
	}
	names := map[string][]keyValue{}
	walked := map[*yaml.Node]bool{}
	var collect func(m *yaml.Node)
	collect = func(m *yaml.Node) {
		if walked[m] {
			return
		}
		walked[m] = true
		for i := 0; i+1 < len(m.Content); i += 2 {
			k, v := m.Content[i], m.Content[i+1]
			if isMergeKey(k) {
				for _, merged := range mergedMappings(v) {
					collect(merged)
				}
				continue
			}
			if name, ok := keyString(k); ok {
				names[name] = append(names[name], keyValue{k, v})
			}
		}
	}
	collect(n)
	a.names[n] = names
	return names
}

// isMergeKey reports whether k is a merge key as the decoder takes one: a scalar <<,
// untagged, tagged ! or tagged !!merge. An alias of one is no merge key, nor is a key
// tagged !!merge whose value is not <<.
func isMergeKey(k *yaml.Node) bool {
	return k.Kind == yaml.ScalarNode && k.Value == "<<" && (k.Tag == "" || k.Tag == "!" || mergeTagged(k))
}

// mergeTagged reports whether n is tagged !!merge, in its short form or its long one.
func mergeTagged(n *yaml.Node) bool {
	return n.Tag == "!!merge" || n.Tag == "tag:yaml.org,2002:merge"
}

// mergedMappings are the mappings the value of a merge key merges: a mapping, or each
// mapping of a list, each followed through its alias.
func mergedMappings(v *yaml.Node) []*yaml.Node {
	v = followAliases(v)
	each := []*yaml.Node{v}
	if v.Kind == yaml.SequenceNode {
		each = v.Content
	}
	var out []*yaml.Node
	for _, m := range each {
		if m = followAliases(m); m.Kind == yaml.MappingNode {
			out = append(out, m)
		}
	}
	return out
}

// keyString is the name the key k decodes into, followed through its alias; false when
// it decodes into no string.
func keyString(k *yaml.Node) (string, bool) {
	var name string
	if k = followAliases(k); k.Kind != yaml.ScalarNode || k.Decode(&name) != nil {
		return "", false
	}
	return name, true
}

// keyAllowed reports whether the schema s allows a key named name in a mapping it
// reads, and whether it lists the name as one of its properties: every schema s holds
// by $ref and allOf allows it, and one of each oneOf and anyOf does. seen are the
// schemas being read, so a schema that refers to itself is read once.
func keyAllowed(s *jsonschema.Schema, name string, seen map[*jsonschema.Schema]bool) (listed, allowed bool) {
	if s == nil || seen[s] {
		return false, true
	}
	seen[s] = true
	defer delete(seen, s)
	if s.Bool != nil {
		return false, *s.Bool
	}
	_, listed = s.Properties[name]
	allowed = listed
	for re := range s.PatternProperties {
		allowed = allowed || re.MatchString(name)
	}
	if b, ok := s.AdditionalProperties.(bool); !allowed && (!ok || b) {
		allowed = true
	}
	if s.PropertyNames != nil && s.PropertyNames.Validate(name) != nil {
		allowed = false
	}
	all := append([]*jsonschema.Schema{s.Ref}, s.AllOf...)
	for _, c := range all {
		l, ok := keyAllowed(c, name, seen)
		listed, allowed = listed || l, allowed && ok
	}
	for _, group := range [][]*jsonschema.Schema{s.OneOf, s.AnyOf} {
		if len(group) == 0 {
			continue
		}
		one := false
		for _, c := range group {
			if l, ok := keyAllowed(c, name, seen); ok {
				listed, one = listed || l, true
			}
		}
		allowed = allowed && one
	}
	return listed, allowed
}

// propertySchemas are the schemas under s that read the value of a key named name: the
// property's, those of the patterns it matches, or else additionalProperties, of s and
// of every schema s holds by $ref, allOf, oneOf and anyOf.
func propertySchemas(s *jsonschema.Schema, name string, seen map[*jsonschema.Schema]bool) []*jsonschema.Schema {
	if s == nil || seen[s] || s.Bool != nil {
		return nil
	}
	seen[s] = true
	defer delete(seen, s)
	var out []*jsonschema.Schema
	p, matched := s.Properties[name]
	if matched {
		out = append(out, p)
	}
	for re, p := range s.PatternProperties {
		if re.MatchString(name) {
			out, matched = append(out, p), true
		}
	}
	if as, ok := s.AdditionalProperties.(*jsonschema.Schema); ok && !matched {
		out = append(out, as)
	}
	for _, c := range composed(s) {
		out = append(out, propertySchemas(c, name, seen)...)
	}
	return out
}

// itemSchemas are the schemas under s that read the item i of a list, of s and of
// every schema s holds by $ref, allOf, oneOf and anyOf.
func itemSchemas(s *jsonschema.Schema, i int, seen map[*jsonschema.Schema]bool) []*jsonschema.Schema {
	if s == nil || seen[s] || s.Bool != nil {
		return nil
	}
	seen[s] = true
	defer delete(seen, s)
	var out []*jsonschema.Schema
	switch {
	case i < len(s.PrefixItems):
		out = append(out, s.PrefixItems[i])
	case s.Items2020 != nil:
		out = append(out, s.Items2020)
	}
	switch items := s.Items.(type) {
	case *jsonschema.Schema:
		out = append(out, items)
	case []*jsonschema.Schema:
		if i < len(items) {
			out = append(out, items[i])
		} else if more, ok := s.AdditionalItems.(*jsonschema.Schema); ok {
			out = append(out, more)
		}
	}
	for _, c := range composed(s) {
		out = append(out, itemSchemas(c, i, seen)...)
	}
	return out
}

// composed are the schemas s holds by $ref, allOf, oneOf and anyOf.
func composed(s *jsonschema.Schema) []*jsonschema.Schema {
	var out []*jsonschema.Schema
	if s.Ref != nil {
		out = append(out, s.Ref)
	}
	out = append(out, s.AllOf...)
	out = append(out, s.OneOf...)
	return append(out, s.AnyOf...)
}
