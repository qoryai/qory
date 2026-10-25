package config_test

import (
	"context"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/qoryai/forager/gateway"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"

	"github.com/qoryai/qory/internal/config"
)

// stackHostCases are entries of gateway.stack_hosts, and whether the reader takes each:
// a lower-case host name, an IPv4 address in canonical form or a *. suffix, as Forager's
// gateway.Start takes an entry of StackHosts.
var stackHostCases = []struct {
	host string
	ok   bool
}{
	{"git.example.com", true},
	{"*.example.com", true},
	{"192.0.2.10", true},
	{"localhost", true},
	{"0x7f.example", true},
	{"*.example.123", true},
	{"", false},
	{"Git.example.com", false},
	{"git.example.com.", false},
	{"https://git.example.com", false},
	{"git.example.com:443", false},
	{"git.example.com/team", false},
	{"*", false},
	{"*example.com", false},
	{"git_host.example", false},
	{"10.0.0.01", false},
	{"127.1", false},
	{"256.1.1.1", false},
	{"git.example.123", false},
	{"::1", false},
}

// TestForagerReadsTheStackHosts reads gateway.stack_hosts in the file's order, and lists
// it in one row with the file as origin; absent, null or empty, it is none, and the row
// is "(none)" by default.
func TestForagerReadsTheStackHosts(t *testing.T) {
	hermetic(t)
	want := []string{"git.example.com", "*.example.org", "192.0.2.10"}
	path := foragerFile(t, "gateway:\n  stack_hosts:\n    - git.example.com\n    - \"*.example.org\"\n    - 192.0.2.10\n")
	r, err := config.LoadForager()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(r.StackHosts, want) {
		t.Errorf("gateway.stack_hosts read as %q, want %q", r.StackHosts, want)
	}
	if row := stackHostsRow(t, r); row != (config.Row{Key: "gateway.stack_hosts", Value: strings.Join(want, ", "), Origin: path}) {
		t.Errorf("the row is %+v", row)
	}
	for _, body := range []string{"gateway: {egress: {mode: observe}}\n", "gateway: {stack_hosts: null}\n", "gateway: {stack_hosts: []}\n"} {
		foragerFile(t, body)
		r, err := config.LoadForager()
		if err != nil {
			t.Fatalf("%q: %v", body, err)
		}
		if r.StackHosts != nil {
			t.Errorf("%q: gateway.stack_hosts read as %q, want none", body, r.StackHosts)
		}
		if row := stackHostsRow(t, r); row != (config.Row{Key: "gateway.stack_hosts", Value: "(none)", Origin: config.Default}) {
			t.Errorf("%q: the row is %+v", body, row)
		}
	}
}

// stackHostsRow is the row of gateway.stack_hosts among r's rows.
func stackHostsRow(t *testing.T, r *config.Forager) config.Row {
	t.Helper()
	for _, row := range r.Rows() {
		if row.Key == "gateway.stack_hosts" {
			return row
		}
	}
	t.Fatal("no row of gateway.stack_hosts")
	return config.Row{}
}

// TestForagerTakesTheStackHostsForagerTakes is each entry of stackHostCases read as the
// second entry of gateway.stack_hosts: the reader takes it exactly when Forager's
// gateway.Start takes it, and for any other returns the fix with the index, and no
// value.
func TestForagerTakesTheStackHostsForagerTakes(t *testing.T) {
	hermetic(t)
	for _, c := range stackHostCases {
		path := foragerFile(t, "gateway:\n  stack_hosts: [a.example, "+strconv.Quote(c.host)+"]\n")
		r, err := config.LoadForager()
		want := path + ": " + stackHostFix(1)
		switch {
		case c.ok && err != nil:
			t.Errorf("%q: %v, want it read", c.host, err)
		case c.ok && !slices.Equal(r.StackHosts, []string{"a.example", c.host}):
			t.Errorf("%q: read as %q", c.host, r.StackHosts)
		case !c.ok && (err == nil || err.Error() != want):
			t.Errorf("%q: %v, want %q", c.host, err, want)
		}
		if stopped := startStopsOnStackHost(t, c.host); stopped == c.ok {
			t.Errorf("%q: Forager's gateway.Start returns an error for it %v, and the reader takes it %v", c.host, stopped, c.ok)
		}
	}
}

// startStopsOnStackHost reports whether Forager's gateway.Start returns an error for host
// as an entry of StackHosts. Start checks the empty policy after StackHosts and returns
// an error for it, so no gateway starts.
func startStopsOnStackHost(t *testing.T, host string) bool {
	t.Helper()
	g, err := gateway.Start(context.Background(), gateway.Config{Dir: t.TempDir(), StackHosts: []string{host}, Policy: &gateway.Policy{}})
	if err == nil {
		g.Close(context.Background())
		t.Fatalf("%q: gateway.Start started a gateway with an empty policy", host)
	}
	return strings.Contains(err.Error(), "StackHosts")
}

// TestForagerStopsOnAStackHostThatIsNoString is gateway.stack_hosts that is not a list,
// and an entry of it that is not a string. Each error contains the line and the key, an
// entry's error the fix with the index too, and no error contains the value.
func TestForagerStopsOnAStackHostThatIsNoString(t *testing.T) {
	hermetic(t)
	for _, c := range []struct{ body, want string }{
		{"gateway:\n  stack_hosts: git.example.com\n", ": line 2: gateway.stack_hosts is not a list of strings"},
		{"gateway:\n  stack_hosts: {git.example.com: 443}\n", ": line 2: gateway.stack_hosts is not a list of strings"},
		{"gateway:\n  stack_hosts: [a.example, 443]\n", ": line 2: " + stackHostFix(1)},
		{"gateway:\n  stack_hosts:\n    - a.example\n    - [" + marker + "]\n", ": line 4: " + stackHostFix(1)},
		{"gateway:\n  stack_hosts: [null]\n", ": line 2: " + stackHostFix(0)},
	} {
		path := foragerFile(t, c.body)
		_, err := config.LoadForager()
		if err == nil || err.Error() != path+c.want {
			t.Errorf("%q: %v, want %q", c.body, err, path+c.want)
		}
		if err != nil && strings.Contains(err.Error(), marker) {
			t.Errorf("%q: the error contains the value: %v", c.body, err)
		}
	}
}

// TestAStackHostErrorPrintsNoValue is an entry of gateway.stack_hosts that contains a
// token in a URL, and one that contains an access key secret: each error is the fix with
// the index, and contains neither.
func TestAStackHostErrorPrintsNoValue(t *testing.T) {
	hermetic(t)
	for _, host := range []string{"https://oauth2:" + marker + "@git.example.com", "git.example.com/" + secret} {
		path := foragerFile(t, "gateway:\n  stack_hosts: ["+strconv.Quote(host)+"]\n")
		_, err := config.LoadForager()
		want := path + ": " + stackHostFix(0)
		if err == nil || err.Error() != want {
			t.Errorf("%q: %v, want %q", host, err, want)
		}
		if err != nil && (strings.Contains(err.Error(), marker) || strings.Contains(strings.ToLower(err.Error()), "qak_")) {
			t.Errorf("%q: the error contains the value: %v", host, err)
		}
	}
}

// stackHostFix is the fix of an entry of gateway.stack_hosts that the reader does not
// take, by its index i.
func stackHostFix(i int) string {
	return "set gateway.stack_hosts[" + strconv.Itoa(i) + "] to a lower-case host name, an IPv4 address or a *. suffix, such as git.example.com; no port, path or scheme"
}

// TestTheForagerSchemaTakesTheStackHostsTheReaderTakes checks that forager.schema.json
// takes what the reader takes of an entry of gateway.stack_hosts, a host listed twice
// included.
func TestTheForagerSchemaTakesTheStackHostsTheReaderTakes(t *testing.T) {
	hermetic(t)
	schema, err := jsonschema.NewCompiler().Compile(filepath.Join("..", "..", "contracts", "harness", "v1", "forager.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range stackHostCases {
		var doc any
		if err := yaml.Unmarshal([]byte("apiVersion: qory.dev/v1alpha1\ngateway: {stack_hosts: [a.example, "+strconv.Quote(c.host)+"]}\n"), &doc); err != nil {
			t.Fatal(err)
		}
		if err := schema.Validate(doc); (err == nil) != c.ok {
			t.Errorf("%q: %v, want valid %v", c.host, err, c.ok)
		}
	}
	for _, body := range []string{"gateway: {stack_hosts: []}\n", "gateway: {stack_hosts: [a.example, a.example]}\n"} {
		var doc any
		if err := yaml.Unmarshal([]byte("apiVersion: qory.dev/v1alpha1\n"+body), &doc); err != nil {
			t.Fatal(err)
		}
		if err := schema.Validate(doc); err != nil {
			t.Errorf("%q: %v, want valid", body, err)
		}
	}
}
