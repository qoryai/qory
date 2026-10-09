package config

import (
	"reflect"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestResolvedIsWhatTheDecoderReads is resolved of lists with aliases, alias keys and
// merges, merges of merges and keys a mapping sets beside its merge among them, and
// keys the decoder takes as a merge or as none: << tagged ! or !!merge, a quoted "<<",
// !!merge on another name, and an alias of <<. It holds no alias and no merge, and the
// decoder reads it as it reads the list.
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
