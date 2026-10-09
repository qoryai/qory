package config

import (
	"fmt"
	"strings"
	"time"

	"github.com/qoryai/forager/runcredential"
	"gopkg.in/yaml.v3"
)

// runCredentialsDoc is the name gateway.run_credentials is parsed under, which selects
// YAML.
const runCredentialsDoc = "run_credentials.yaml"

// defaultCache is how long an introspection answer holds when an issuer sets no cache:
// the run's heartbeat interval, which qory gateway leaves at Forager's default.
const defaultCache = 30 * time.Second

// readRunCredentials reads gateway.run_credentials, the list of issuers, against
// Forager's run-credentials.schema.json, and the rows qory config lists of it, each
// value as the file writes it. It reads no file the list names: qory gateway hands the
// list to Forager, which checks the keys and the secrets' files before it listens.
func readRunCredentials(path string, node *yaml.Node) (runcredential.Issuers, []Row, error) {
	b, err := yaml.Marshal(node)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: gateway.run_credentials: %w", path, err)
	}
	issuers, err := runcredential.Parse(runCredentialsDoc, b)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: gateway.run_credentials: %s", path, strings.TrimPrefix(err.Error(), "run credentials "+runCredentialsDoc+": "))
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
				rows = append(rows, Row{key + "introspection." + name, fmt.Sprintf("%.0fs", defaultCache.Seconds()), Default})
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
