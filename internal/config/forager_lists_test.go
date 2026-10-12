package config_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/qoryai/qory/internal/config"
)

// foragerList is one list of strings forager.yaml holds: the file with %s for the list,
// a valid entry, the refusal of the entry %d when it is not a string, after the file's
// path, and the list as read.
type foragerList struct {
	body, entry, notString string
	read                   func(*config.Forager) []string
}

// notAHost is the refusal of an entry of gateway.egress.allow or deny.
const notAHost = " is not a lower-case host name or a *. suffix, or it ends in a number and is not an IPv4 address in canonical form; no port, path or scheme"

// foragerLists are the lists of strings of forager.yaml, each written in one line.
var foragerLists = []foragerList{
	{"gateway: {egress: {mode: observe, allow: %s}}\n", "a.example", ": gateway.egress.allow[%d]" + notAHost, func(r *config.Forager) []string { return r.Egress.Allow }},
	{"gateway: {egress: {mode: observe, deny: %s}}\n", "a.example", ": gateway.egress.deny[%d]" + notAHost, func(r *config.Forager) []string { return r.Egress.Deny }},
	{"gateway: {credentials: {c: {adapter: %s}}}\n", "/opt/adapter", ": line 1: gateway.credentials.c.adapter[%d] is not a string", func(r *config.Forager) []string { return r.Credentials[0].Adapter }},
	{"gateway: {credentials: {c: {env: X, hosts: %s}}}\n", "a.example", ": line 1: gateway.credentials.c.hosts[%d] is not a string", func(r *config.Forager) []string { return r.Credentials[0].Hosts }},
	{"gateway: {credentials: {c: {env: X, paths: %s}}}\n", "/v1/*", ": line 1: gateway.credentials.c.paths[%d] is not a string", func(r *config.Forager) []string { return r.Credentials[0].Paths }},
	{"gateway: {credentials: {c: {env: X, placeholders: %s}}}\n", "TOKEN", ": line 1: gateway.credentials.c.placeholders[%d] is not a string", func(r *config.Forager) []string { return r.Credentials[0].Placeholders }},
	{"gateway: {tools: {t: {command: %s}}}\n", "/opt/tool", ": line 1: gateway.tools.t.command[%d] is not a string", func(r *config.Forager) []string { return r.Tools[0].Command }},
	{"gateway: {tools: {t: {command: [/x], serves: %s}}}\n", "a.example", ": line 1: gateway.tools.t.serves[%d] is not a string", func(r *config.Forager) []string { return r.Tools[0].Serves }},
	{"gateway: {tools: {t: {command: [/x], placeholders: %s}}}\n", "TOKEN", ": line 1: gateway.tools.t.placeholders[%d] is not a string", func(r *config.Forager) []string { return r.Tools[0].Placeholders }},
	{"wall: {adapter: docker, env: %s}\n", "MODEL_KEY", ": wall.env[%d] is not a variable's name; the value comes from the environment, never from this file", func(r *config.Forager) []string { return r.Wall.Env }},
	{"wall: {adapter: docker, mounts: %s}\n", "/srv", ": wall.mounts[%d] is not an absolute path, with :ro after it for a read-only one", func(r *config.Forager) []string {
		var out []string
		for _, m := range r.Wall.Mounts {
			out = append(out, m.Path)
		}
		return out
	}},
	{"wall: {adapter: docker, ca_env: %s}\n", "SSL_CERT_FILE", ": wall.ca_env[%d] is not a variable's name", func(r *config.Forager) []string { return r.Wall.CAEnv }},
}

// TestForagerRefusesAListEntryThatIsNotAString writes into every list of strings of
// forager.yaml an entry that is not a string: a null, a list of a null alone, a number,
// a boolean, a list, a mapping, a value its tag does not fit and an alias of a null.
// Each is refused by its index, in the words the list uses for an entry it cannot take,
// and never with the value.
func TestForagerRefusesAListEntryThatIsNotAString(t *testing.T) {
	hermetic(t)
	for _, l := range foragerLists {
		for _, c := range []struct {
			before, list string
			at           int
		}{
			{"", "[" + l.entry + ", ~]", 1},
			{"", "[" + l.entry + ", null]", 1},
			{"", "[~]", 0},
			{"", "[~, " + l.entry + "]", 0},
			{"", "[" + l.entry + ", 7]", 1},
			{"", "[" + l.entry + ", true]", 1},
			{"", "[" + l.entry + ", [" + marker + "]]", 1},
			{"", "[" + l.entry + ", {k: " + marker + "}]", 1},
			{"", "[" + l.entry + ", !!int " + secret + "]", 1},
			{"session: {instance: {name: &n ~}}\n", "[" + l.entry + ", *n]", 1},
		} {
			body := c.before + fmt.Sprintf(l.body, c.list)
			path := foragerFile(t, body)
			_, err := config.LoadForager()
			notString := l.notString
			if c.before != "" {
				notString = strings.Replace(notString, ": line 1: ", ": line 2: ", 1)
			}
			if want := path + fmt.Sprintf(notString, c.at); err == nil || err.Error() != want {
				t.Errorf("%q: %v, want %q", body, err, want)
			}
			if err != nil && (strings.Contains(err.Error(), marker) || strings.Contains(err.Error(), "qak_")) {
				t.Errorf("%q: the refusal holds the value: %v", body, err)
			}
		}
	}
}

// TestForagerReadsAListAsBefore reads every list of strings of forager.yaml written with
// strings, an alias entry as the string its anchor holds, a quoted number as a string,
// and an absent list and a null one alike.
func TestForagerReadsAListAsBefore(t *testing.T) {
	hermetic(t)
	for _, l := range foragerLists {
		for _, c := range []struct {
			list string
			want []string
		}{
			{"[" + l.entry + "]", []string{l.entry}},
			{"[&a " + l.entry + ", *a]", []string{l.entry, l.entry}},
			{"[" + l.entry + ", !!str " + l.entry + "]", []string{l.entry, l.entry}},
		} {
			body := fmt.Sprintf(l.body, c.list)
			foragerFile(t, body)
			r, err := config.LoadForager()
			if err != nil {
				t.Errorf("%q: %v", body, err)
				continue
			}
			if got := l.read(r); !slices.Equal(got, c.want) {
				t.Errorf("%q: read as %q, want %q", body, got, c.want)
			}
		}
	}
	for _, c := range []struct {
		body string
		read func(*config.Forager) []string
	}{
		{"gateway: {egress: {mode: observe, allow: ~}}\n", func(r *config.Forager) []string { return r.Egress.Allow }},
		{"gateway: {credentials: {c: {env: X, hosts: ~}}}\n", func(r *config.Forager) []string { return r.Credentials[0].Hosts }},
		{"gateway: {credentials: {c: {env: X}}}\n", func(r *config.Forager) []string { return r.Credentials[0].Hosts }},
		{"gateway: {credentials: {c: {env: X, adapter: ~}}}\n", func(r *config.Forager) []string { return r.Credentials[0].Adapter }},
		{"gateway: {tools: {t: {command: [/x], serves: ~}}}\n", func(r *config.Forager) []string { return r.Tools[0].Serves }},
		{"wall: {adapter: docker, env: ~}\n", func(r *config.Forager) []string { return r.Wall.Env }},
	} {
		foragerFile(t, c.body)
		r, err := config.LoadForager()
		if err != nil {
			t.Errorf("%q: %v", c.body, err)
			continue
		}
		if got := c.read(r); got != nil {
			t.Errorf("%q: read as %#v, want none", c.body, got)
		}
	}
	foragerFile(t, "gateway: {credentials: {c: {env: X, hosts: []}}, tools: {t: {command: [/x], serves: []}}}\n")
	r, err := config.LoadForager()
	if err != nil {
		t.Fatal(err)
	}
	if h, s := r.Credentials[0].Hosts, r.Tools[0].Serves; h == nil || len(h) != 0 || s == nil || len(s) != 0 {
		t.Errorf("empty lists read as %#v and %#v, want empty and not none", h, s)
	}
}

// TestForagerReadsTheEgressHostsAsForagerDoes reads each host of gateway.egress.allow
// and deny as Forager reads a policy's: a name, a *. suffix, or an IPv4 address in
// canonical form. A host that ends in a number and is no such address is refused by
// its index, and never printed.
func TestForagerReadsTheEgressHostsAsForagerDoes(t *testing.T) {
	hermetic(t)
	for _, list := range []string{"allow", "deny"} {
		for _, host := range []string{"api.example.123", "127.1", "443", "0x1f", "10.0.0.01", "256.1.1.1", "example.0x", "api.example.com:443", "API.example.com"} {
			body := "gateway: {egress: {mode: enforce, " + list + ": [a.example, \"" + host + "\"]}}\n"
			path := foragerFile(t, body)
			_, err := config.LoadForager()
			if want := path + ": gateway.egress." + list + "[1]" + notAHost; err == nil || err.Error() != want {
				t.Errorf("%q: %v, want %q", body, err, want)
			}
			if err != nil && strings.Contains(err.Error(), host) {
				t.Errorf("%q: the refusal holds the host: %v", body, err)
			}
		}
		hosts := []string{"10.0.0.1", "*.example.com", "api.example.com", "0x7f.example", "*.example.123"}
		body := "gateway: {egress: {mode: enforce, " + list + ": [\"" + strings.Join(hosts, "\", \"") + "\"]}}\n"
		foragerFile(t, body)
		r, err := config.LoadForager()
		if err != nil {
			t.Errorf("%q: %v", body, err)
			continue
		}
		got := r.Egress.Allow
		if list == "deny" {
			got = r.Egress.Deny
		}
		if !slices.Equal(got, hosts) {
			t.Errorf("%q: read as %q, want %q", body, got, hosts)
		}
	}
}

// TestForagerReadsALongEgressAtOnce reads 2,000 hosts of gateway.egress.allow, and deny
// and paths beside them, in order and at once; a host written twice, which a policy's
// list may not hold, is read as before, each time it is written.
func TestForagerReadsALongEgressAtOnce(t *testing.T) {
	hermetic(t)
	hosts := make([]string, 2000)
	for i := range hosts {
		hosts[i] = fmt.Sprintf("h%d.example.com", i)
	}
	foragerFile(t, "gateway: {egress: {mode: enforce, allow: ["+strings.Join(hosts, ", ")+"], deny: [d.example.com], paths: {p.example.com: [/v1, \"/v2/*\"], \"*.files.example.com\": []}}}\n")
	start := time.Now()
	r, err := config.LoadForager()
	if err != nil {
		t.Fatal(err)
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Errorf("2,000 hosts read in %v", took)
	}
	if e := r.Egress; !slices.Equal(e.Allow, hosts) || !slices.Equal(e.Deny, []string{"d.example.com"}) || len(e.Paths) != 2 || !slices.Equal(e.Paths["p.example.com"], []string{"/v1", "/v2/*"}) || e.Paths["*.files.example.com"] == nil || len(e.Paths["*.files.example.com"]) != 0 {
		t.Errorf("read as %+v", e)
	}
	foragerFile(t, "gateway: {egress: {mode: enforce, allow: [a.example, a.example], deny: [d.example, d.example]}}\n")
	if r, err = config.LoadForager(); err != nil || !slices.Equal(r.Egress.Allow, []string{"a.example", "a.example"}) || !slices.Equal(r.Egress.Deny, []string{"d.example", "d.example"}) {
		t.Errorf("a host written twice: %+v, %v", r, err)
	}
}
