package config

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/qoryai/forager/runcredential"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"
)

// runCredentialsDoc is the name gateway.run_credentials is parsed under, which selects
// YAML.
const runCredentialsDoc = "run_credentials.yaml"

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
	// that reached its limit leaves the list to Forager's decoder, under safeDecode.
	if f := findFault(node, reflect.TypeFor[any](), "gateway.run_credentials", false); f != nil && f != walkLimited {
		return nil, nil, f.error(path)
	}
	notIssuers := fmt.Errorf("%s: gateway.run_credentials is not a list of issuers as run-credentials.schema.json defines them", path)
	b, err := yaml.Marshal(node)
	if err != nil {
		return nil, nil, notIssuers
	}
	var issuers runcredential.Issuers
	err = safeDecode(func() (err error) {
		issuers, err = runcredential.Parse(runCredentialsDoc, b)
		return err
	})
	if err != nil {
		var ve *jsonschema.ValidationError
		if !errors.As(err, &ve) {
			if errors.Is(err, errDecoderFailed) {
				return nil, nil, fmt.Errorf("%s: gateway.run_credentials: %w", path, err)
			}
			// The list is written out on its own, and an alias in it of an anchor
			// elsewhere in the file is written without the anchor, which Forager's
			// decoder then does not know: that is no fault of the file's anchors.
			if text, ok := aliasRefusal(err); ok && text != unknownAnchor {
				return nil, nil, fmt.Errorf("%s: gateway.run_credentials: %s", path, text)
			}
			return nil, nil, notIssuers
		}
		valueFree(ve)
		return nil, nil, fmt.Errorf("%s: gateway.run_credentials: %s", path, ve.Error())
	}
	var rows []Row
	for i, item := range node.Content {
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
