package config

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestResolvedIsWhatTheDecoderReads is resolved of lists with aliases, alias keys and
// merges, merges of merges and keys a mapping sets beside its merge among them, and
// keys the decoder takes as a merge or as none: << tagged ! or !!merge, a quoted "<<",
// !!merge on another name, and an alias of <<; and a merged key named << or null, which
// the decoder drops. It holds no alias and no merge, and the decoder reads it as it
// reads the list.
func TestResolvedIsWhatTheDecoderReads(t *testing.T) {
	for _, body := range []string{
		"x: &a {k: 1}\nl: [*a, *a]\n",
		"x: &k name\nl: [{*k : 1, other: *k}]\n",
		"x: &a {k: 1, j: 2}\ny: &b {k: 3, m: 4}\nl: [{<<: [*a, *b], j: 5}]\n",
		"x: &a {k: 1, <<: {j: 2, k: 9}}\ny: &b {<<: *a, m: 4}\nl: [{<<: [*b, {j: 7, n: 8}], m: 5}]\n",
		"x: &k j\nl: [{j: 1, *k : 2}, {<<: {j: 1, *k : 2}}]\n",
		"x: &a [1, {k: &v v}]\nl: [*a, {k: *v, <<: {k: w, z: [*a]}}]\n",
		"l: [{!!merge team: {k: 1}, a: 1}]\n",
		"l: [{\"<<\": {k: 1}, a: 1}]\n",
		"l: [{! <<: {k: 1}, a: 1}]\n",
		"l: [{!!merge <<: {k: 1}, a: 1}]\n",
		"l: [{!!merge \"<<\": {k: 1}, a: 1}]\n",
		"x: &m <<\nl: [{*m : {k: 1}, a: 1}]\n",
		"l: [{<<: {\"<<\": {k: 1}, team: {k: 2}}}]\n",
		"l: [{<<: {~: {k: 1}, team: {k: 2}}}]\n",
	} {
		var doc yaml.Node
		if err := yaml.Unmarshal([]byte(body), &doc); err != nil {
			t.Fatal(err)
		}
		list := doc.Content[0].Content[len(doc.Content[0].Content)-1]
		var want, got any
		if err := list.Decode(&want); err != nil {
			t.Fatal(err)
		}
		r := resolved(list)
		if err := r.Decode(&got); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%q: resolved reads as %v, want %v", body, got, want)
		}
		var walk func(n *yaml.Node)
		walk = func(n *yaml.Node) {
			if n.Kind == yaml.AliasNode || n.Anchor != "" || isMergeKey(n) {
				t.Errorf("%q: resolved holds %v", body, n.Value)
			}
			for _, c := range n.Content {
				walk(c)
			}
		}
		walk(r)
	}
}

// TestRunCredentialsJSONOverTheBoundIsRefusedAfterItIsMade writes 1000 aliases of an
// audience of 1000 <, which JSON writes as \u003c, six bytes each. The count made before
// the JSON, a lower bound of its length, is about 1 MB and under the bound; the JSON is
// about 6 MB and over it, and the list is refused with the words of aliases that expand
// too far.
func TestRunCredentialsJSONOverTheBoundIsRefusedAfterItIsMade(t *testing.T) {
	aliases := strings.TrimSuffix(strings.Repeat("*v, ", 1000), ", ")
	body := "- {issuer: \"https://issuer.example\", audience: &v '" + strings.Repeat("<", 1000) + "', algorithms: [RS256], keys: [{kid: k1, alg: RS256, public_key_file: f.pem}], labels: {forge: {value: x}, repository: {claims: [" + aliases + "], join: /}, run_key: {claim: sub}}}\n"
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatal(err)
	}
	list := doc.Content[0]
	var v any
	if err := list.Decode(&v); err != nil {
		t.Fatal(err)
	}
	if jsonOver(v, runCredentialsBudget) {
		t.Fatal("the count before the JSON is over the bound: the check after json.Marshal is not reached")
	}
	if b, err := json.Marshal(v); err != nil || len(b) <= runCredentialsBudget {
		t.Fatalf("the JSON takes %d bytes (%v), want more than %d", len(b), err, runCredentialsBudget)
	}
	_, _, err := readRunCredentials("forager.yaml", list)
	if want := "forager.yaml: gateway.run_credentials: " + tooManyAliases; err == nil || err.Error() != want {
		t.Errorf("%v, want %q", err, want)
	}
}
