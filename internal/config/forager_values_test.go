package config_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/qoryai/qory/internal/config"
)

// marker is what a test writes into forager.yaml's values, so a refusal that prints a
// value is caught: secret is an access key secret's start and the marker.
const (
	marker = "VALUEMARKER7Q"
	secret = "qak_" + marker
)

// foragerKeys is a place of every key of forager.yaml that holds a value, as the file
// writes it with %s for the value, and the key a refusal names.
var foragerKeys = []struct{ body, key string }{
	{"apiVersion: %s\n", "apiVersion"},
	{"gateway: {egress: {mode: %s}}\n", "gateway.egress.mode"},
	{"gateway: {egress: {mode: observe, allow: %s}}\n", "gateway.egress.allow"},
	{"gateway: {egress: {mode: observe, deny: %s}}\n", "gateway.egress.deny"},
	{"gateway: {server: {url: %s}}\n", "gateway.server.url"},
	{"gateway: {server: {url: \"https://qory.example\", access_key_id: %s}}\n", "gateway.server.access_key_id"},
	{"gateway: {server: {url: \"https://qory.example\", apiary_public_key: %s}}\n", "gateway.server.apiary_public_key"},
	{"gateway: {server: {url: \"https://qory.example\", apiary_public_key: [{alg: %s, public_key: x}]}}\n", "gateway.server.apiary_public_key[0].alg"},
	{"gateway: {server: {url: \"https://qory.example\", apiary_public_key: [{alg: ed25519, public_key: %s}]}}\n", "gateway.server.apiary_public_key[0].public_key"},
	{"gateway: {server: {url: \"https://qory.example\", secret: %s}}\n", "gateway.server.secret"},
	{"gateway: {server: {url: \"https://qory.example\", access_key: %s}}\n", "gateway.server.access_key"},
	{"gateway: {credentials: %s}\n", "gateway.credentials"},
	{"gateway: {credentials: {c: {env: %s}}}\n", "gateway.credentials.c.env"},
	{"gateway: {credentials: {c: {file: %s}}}\n", "gateway.credentials.c.file"},
	{"gateway: {credentials: {c: {adapter: %s}}}\n", "gateway.credentials.c.adapter"},
	{"gateway: {credentials: {c: {argument: %s}}}\n", "gateway.credentials.c.argument"},
	{"gateway: {credentials: {c: {hosts: %s}}}\n", "gateway.credentials.c.hosts"},
	{"gateway: {credentials: {c: {paths: %s}}}\n", "gateway.credentials.c.paths"},
	{"gateway: {credentials: {c: {auth: {scheme: %s}}}}\n", "gateway.credentials.c.auth.scheme"},
	{"gateway: {credentials: {c: {auth: {username: %s}}}}\n", "gateway.credentials.c.auth.username"},
	{"gateway: {credentials: {c: {auth: {header: %s}}}}\n", "gateway.credentials.c.auth.header"},
	{"gateway: {credentials: {c: {placeholders: %s}}}\n", "gateway.credentials.c.placeholders"},
	{"gateway: {integrations: %s}\n", "gateway.integrations"},
	{"gateway: {integrations: {i: {program: %s}}}\n", "gateway.integrations.i.program"},
	{"gateway: {integrations: {i: {settings: {s: %s}}}}\n", "gateway.integrations.i.settings"},
	{"gateway: {listen: %s}\n", "gateway.listen"},
	{"gateway: {tls: {certificate: %s, key: k.pem}}\n", "gateway.tls.certificate"},
	{"gateway: {tls: {certificate: c.pem, key: %s}}\n", "gateway.tls.key"},
	{"gateway: {run_credentials: %s}\n", "gateway.run_credentials"},
	{"gateway: {run_credentials: [{issuer: %s}]}\n", "gateway.run_credentials"},
	{"gateway: {run_credentials: [" + issuerSection[:len(issuerSection)-1] + ", leeway: %s}]}\n", "gateway.run_credentials"},
	{"session: {gateway: {url: %s}}\n", "session.gateway.url"},
	{"session: {gateway: {url: \"https://gateway.example\", ca_file: %s}}\n", "session.gateway.ca_file"},
	{"session: {gateway: {url: \"https://gateway.example\", certificate_sha256: %s}}\n", "session.gateway.certificate_sha256"},
	{"session: {gateway: {url: \"https://gateway.example\", run_credential_file: %s}}\n", "session.gateway.run_credential_file"},
	{"session: {instance: {name: %s}}\n", "session.instance.name"},
	{"session: {run: {timeout: %s}}\n", "session.run.timeout"},
	{"session: {run: {stop_signal: %s}}\n", "session.run.stop_signal"},
	{"session: {run: {stop_grace: %s}}\n", "session.run.stop_grace"},
	{"wall: {adapter: %s}\n", "wall.adapter"},
	{"wall: {adapter: docker, image: %s}\n", "wall.image"},
	{"wall: {adapter: docker, images: %s}\n", "wall.images"},
	{"wall: {adapter: docker, images: {go: {ref: %s}}}\n", "wall.images.go.ref"},
	{"wall: {adapter: docker, images: {go: {ref: a, runtime: %s}}}\n", "wall.images.go.runtime"},
	{"wall: {adapter: docker, images: {go: {ref: a, docker: %s}}}\n", "wall.images.go.docker"},
	{"wall: {adapter: docker, command: %s}\n", "wall.command"},
	{"wall: {adapter: docker, helper: %s}\n", "wall.helper"},
	{"wall: {adapter: docker, env: %s}\n", "wall.env"},
	{"wall: {adapter: docker, user: %s}\n", "wall.user"},
	{"wall: {adapter: docker, mounts: %s}\n", "wall.mounts"},
	{"wall: {adapter: docker, cpus: %s}\n", "wall.cpus"},
	{"wall: {adapter: docker, memory: %s}\n", "wall.memory"},
	{"wall: {adapter: docker, pids_limit: %s}\n", "wall.pids_limit"},
	{"wall: {adapter: docker, shm_size: %s}\n", "wall.shm_size"},
	{"wall: {adapter: docker, ca_env: %s}\n", "wall.ca_env"},
	{"webhook: %s\n", "webhook"},
}

// TestNoForagerRefusalPrintsAValue writes into every key of forager.yaml a value the
// reader cannot take: an access key secret under a tag its value does not fit, and a
// mapping or a list where another kind goes. Each is refused, naming the file and the
// key, and no refusal holds the value.
func TestNoForagerRefusalPrintsAValue(t *testing.T) {
	hermetic(t)
	for _, k := range foragerKeys {
		for _, v := range []string{"!!int " + secret, "!!bool " + secret, "!!float " + secret, "!!timestamp " + secret, "!!null " + secret, "!!binary " + secret, "{k: " + secret + "}", "[" + secret + "]", "[[" + secret + "]]", "[{k: " + secret + "}]"} {
			body := strings.Replace(k.body, "%s", v, 1)
			path := foragerFile(t, body)
			_, err := config.LoadForager()
			if err == nil && (k.key == "gateway.integrations.i.settings" || strings.HasPrefix(v, "[") && !strings.HasPrefix(v, "[[") && !strings.HasPrefix(v, "[{")) {
				continue // the settings take any value, and a list of words is the value of a key that takes one
			}
			switch {
			case err == nil:
				t.Errorf("%q: read, want a refusal", body)
			case !strings.HasPrefix(err.Error(), path+": "):
				t.Errorf("%q: %v, want one naming the file", body, err)
			case strings.Contains(err.Error(), marker) || strings.Contains(strings.ToLower(err.Error()), "qak_"):
				t.Errorf("%q: the refusal holds the value: %v", body, err)
			case strings.Contains(err.Error(), "config.") || strings.Contains(err.Error(), "struct {"):
				t.Errorf("%q: the refusal holds a Go type: %v", body, err)
			case !strings.Contains(err.Error(), k.key) && !strings.Contains(err.Error(), "secret") && !strings.Contains(err.Error(), `key "k" is not one`):
				t.Errorf("%q: %v, want one naming %s", body, err, k.key)
			}
		}
	}
}

// TestForagerRefusalsSayWhatIsWrongWithoutTheValue is the refusal of a value of
// forager.yaml the reader cannot take, word for word: the line, the key and what is
// wrong, and never the value.
func TestForagerRefusalsSayWhatIsWrongWithoutTheValue(t *testing.T) {
	hermetic(t)
	for _, c := range []struct{ body, want string }{
		{"session: {gateway: {url: !!int " + secret + "}}\n", ": line 1: session.gateway.url is tagged !!int, and its value is not of that type"},
		{"session:\n  gateway:\n    url: https://gateway.example\n    ca_file: [" + secret + "]\n", ": line 4: session.gateway.ca_file is not a string"},
		{"session:\n  gateway:\n    url: https://gateway.example\n    ca_file: &a {k: " + secret + "}\n    certificate_sha256: *a\n", ": line 4: session.gateway.ca_file is not a string"},
		{"gateway: {egress: {mode: {k: " + secret + "}}}\n", ": line 1: gateway.egress.mode is not a string"},
		{"gateway:\n  egress:\n    mode: observe\n    allow: " + secret + "\n", ": line 4: gateway.egress.allow is not a list of strings"},
		{"gateway:\n  egress:\n    mode: observe\n    allow:\n      - a.example\n      - !!int " + secret + "\n", ": line 6: gateway.egress.allow[1] is tagged !!int, and its value is not of that type"},
		{"gateway: {server: {url: \"https://qory.example\", apiary_public_key: " + secret + "}}\n", ": line 1: gateway.server.apiary_public_key is not a list"},
		{"gateway: {server: {url: \"https://qory.example\", apiary_public_key: [" + secret + "]}}\n", ": line 1: gateway.server.apiary_public_key[0] is not a mapping"},
		{"wall:\n  adapter: docker\n  pids_limit: " + secret + "\n", ": line 3: wall.pids_limit is not a whole number"},
		{"wall:\n  <<: {adapter: docker, cpus: [" + secret + "]}\n", ": line 2: wall.cpus is not a string"},
		{"session: " + secret + "\n", ": line 1: session is not a mapping"},
		{"[" + secret + "]\n", ": line 1: the file is not a mapping of sections"},
		{secret + "\n", ": line 1: the file is not a mapping of sections"},
		{"apiVersion: !!binary " + secret + "\n", ": line 1: apiVersion is tagged !!binary, and its value is not of that type"},
		{"gateway:\n  credentials:\n    c:\n      hosts: {k: " + secret + "}\n", ": line 4: gateway.credentials.c.hosts is not a list of strings"},
		{"gateway:\n  credentials:\n    c:\n      auth: {scheme: !!timestamp " + secret + "}\n", ": line 4: gateway.credentials.c.auth.scheme is tagged !!timestamp, and its value is not of that type"},
		{"gateway:\n  run_credentials:\n    - issuer: !!int " + secret + "\n", ": line 3: gateway.run_credentials[0].issuer is tagged !!int, and its value is not of that type"},
		{"gateway:\n  run_credentials:\n    - " + issuerSection[:len(issuerSection)-1] + ", leeway: .nan}\n", ": line 3: gateway.run_credentials[0].leeway is not a number that JSON can represent"},
		{"gateway: {egress: {mode: " + marker + "}}\n", ": gateway.egress.mode is not observe or enforce"},
		{"gateway: {egress: {mode: enforce, allow: [a.example, \"" + marker + ":443\"]}}\n", ": gateway.egress.allow[1] is not a lower-case host name or a *. suffix; no port, path or scheme"},
		{"gateway: {egress: {mode: observe, deny: [\"" + marker + "\"]}}\n", ": gateway.egress.deny[0] is not a lower-case host name or a *. suffix; no port, path or scheme"},
		{"gateway: {server: {url: \"https://qory.example\", access_key_id: " + marker + "}}\n", ": gateway.server.access_key_id is not ak_ and 16 lower-case Crockford base32 characters"},
		{"gateway: {server: {url: \"https://qory.example\", apiary_public_key: [{alg: " + marker + ", public_key: x}]}}\n", ": gateway.server.apiary_public_key[0].alg is not ed25519, the one there is"},
		{"gateway: {server: {url: \"https://qory.example\", apiary_public_key: [{alg: ed25519, public_key: " + marker + "}]}}\n", ": gateway.server.apiary_public_key[0].public_key is not 32 bytes in base64url without padding"},
		{"gateway: {server: {url: \"https://qory.example\", apiary_public_key: [{alg: ed25519, public_key: AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA}]}}\n", ": gateway.server.apiary_public_key[0].public_key: the public key cannot be used: it is a point of small order"},
		{"gateway: {listen: " + marker + "}\n", ": gateway.listen is not host:port, such as 0.0.0.0:8443"},
		{"gateway: {credentials: {c: {file: " + marker + "}}}\n", ": gateway.credentials.c.file is not an absolute path"},
		{"gateway: {integrations: {i: {program: " + marker + "/x}}}\n", ": gateway.integrations.i.program is not an absolute path; set program to an absolute path or a name on the PATH"},
		{"gateway: {run_credentials: [{issuer: \"http://" + marker + ".example\"}]}\n", ": gateway.run_credentials: jsonschema validation failed with 'https://qory.dev/contracts/forager/v1/run-credentials.schema.json#'\n- at '/0': validation failed\n  - at '/0': missing properties 'audience', 'algorithms', 'keys', 'labels'\n  - at '/0/issuer': value does not match pattern '^https://[^/?#@\\\\s]+(/[^?#\\\\s]*)?$'"},
		{"gateway: {run_credentials: [" + strings.Replace(issuerSection, "\"https://issuer.example\", audience", "\"https://["+marker+"\", audience", 1) + "]}\n", ": gateway.run_credentials: jsonschema validation failed with 'https://qory.dev/contracts/forager/v1/run-credentials.schema.json#'\n- at '/0/issuer': value is not valid uri"},
		{"session: {gateway: {url: \"https://gateway.example\", certificate_sha256: " + marker + "}}\n", ": session.gateway.certificate_sha256 is not the SHA-256 of a public key in standard base64 with padding, 44 characters ending in ="},
		{"session: {instance: {name: \"-" + marker + "\"}}\n", ": session.instance.name is not 1 to 64 of A-Z, a-z, 0-9, dot, underscore and dash, starting with a letter or digit"},
		{"session: {run: {timeout: " + marker + "}}\n", ": session.run.timeout is not a duration above zero, such as 5h30m or 30s"},
		{"session: {run: {stop_grace: " + marker + "}}\n", ": session.run.stop_grace is not a duration above zero, such as 5h30m or 30s"},
		{"session: {run: {stop_signal: " + marker + "}}\n", ": session.run.stop_signal is not one of SIGTERM, SIGINT, SIGHUP, SIGQUIT, SIGUSR1, SIGUSR2"},
		{"wall: {adapter: docker, helper: " + marker + "}\n", ": wall.helper is not an absolute path"},
		{"wall: {adapter: docker, mounts: [/srv, " + marker + "]}\n", ": wall.mounts[1] is not an absolute path, with :ro after it for a read-only one"},
		{"wall: {adapter: docker, pids_limit: -7}\n", ": wall.pids_limit is below 1; a limit is at least 1"},
		{"wall: {adapter: docker, ca_env: [\"" + marker + "-X\"]}\n", ": wall.ca_env[0] is not a variable's name"},
		{"wall: {adapter: docker, env: [HOME, \"" + marker + "=x\"]}\n", ": wall.env[1] is not a variable's name; the value comes from the environment, never from this file"},
		{"wall: {adapter: docker, env: [QORY_ACCESS_KEY_SECRET]}\n", ": wall.env[0] names a variable that is Forager's own and never the agent's"},
		{"wall: {adapter: docker, images: {go: {ref: \"--" + marker + "\"}}}\n", ": wall.images.go.ref is not an image reference, such as ghcr.io/acme/agent@sha256:…"},
		{"wall: {adapter: docker, images: {go: {ref: a, runtime: \"--" + marker + "\"}}}\n", ": wall.images.go.runtime is not a container runtime's name, such as sysbox-runc"},
		{"apiVersion: " + marker + "\n", ": apiVersion is not one this qory reads; versions: qory.dev/v1alpha1"},
	} {
		path := foragerFile(t, c.body)
		_, err := config.LoadForager()
		if err == nil || err.Error() != path+c.want {
			t.Errorf("%q: %v, want %q", c.body, err, path+c.want)
		}
		if err != nil && (strings.Contains(err.Error(), marker) || strings.Contains(err.Error(), "-7") || strings.Contains(err.Error(), "QORY_ACCESS_KEY_SECRET")) {
			t.Errorf("%q: the refusal holds the value: %v", c.body, err)
		}
	}
}

// TestTheInstanceAloneRefusesWithoutTheValue is session.instance.name read alone, for a
// key made for another machine: a value it cannot take is refused as the whole file's
// reader refuses it, without the value.
func TestTheInstanceAloneRefusesWithoutTheValue(t *testing.T) {
	hermetic(t)
	for _, c := range []struct{ body, want string }{
		{"session: {instance: {name: !!int " + secret + "}}\n", ": line 1: session.instance.name is tagged !!int, and its value is not of that type"},
		{"session:\n  instance:\n    name: [" + secret + "]\n", ": line 3: session.instance.name is not a string"},
		{"session: {instance: {name: \"-" + marker + "\"}}\n", ": session.instance.name is not 1 to 64 of A-Z, a-z, 0-9, dot, underscore and dash, starting with a letter or digit"},
	} {
		path := foragerFile(t, c.body)
		_, err := config.LoadForagerInstance()
		if err == nil || err.Error() != path+c.want {
			t.Errorf("%q: %v, want %q", c.body, err, path+c.want)
		}
	}
}

// aliasBomb is a list of n levels of anchors under gateway.run_credentials, each level
// ten aliases of the one before, and the first ten values.
func aliasBomb(levels int) string {
	b := "gateway:\n  run_credentials:\n    - &a0 [" + strings.Repeat(secret+", ", 9) + secret + "]\n"
	for i := 1; i < levels; i++ {
		b += fmt.Sprintf("    - &a%d [%s*a%d]\n", i, strings.Repeat(fmt.Sprintf("*a%d, ", i-1), 9), i-1)
	}
	return b
}

// mergeBomb is n levels of anchors under gateway.credentials, each level a mapping that
// merges the one before ten times, and the first ten keys, merged into session.instance.
func mergeBomb(levels int) string {
	b := "gateway:\n  credentials:\n    c0: &m0 {"
	for j := range 10 {
		if j > 0 {
			b += ", "
		}
		b += fmt.Sprintf("k%d: %s", j, secret)
	}
	b += "}\n"
	for i := 1; i < levels; i++ {
		b += fmt.Sprintf("    c%d: &m%d {<<: [%s*m%d]}\n", i, i, strings.Repeat(fmt.Sprintf("*m%d, ", i-1), 9), i-1)
	}
	return b + fmt.Sprintf("session:\n  instance: {<<: *m%d}\n", levels-1)
}

// TestForagerRefusesAnchorsItCannotReadAtOnce is a file whose anchors hold an alias of
// themselves, or whose aliases or merges expand without end: each is refused at once,
// by both readers, in words that name no anchor and no value, and is never walked
// without end.
func TestForagerRefusesAnchorsItCannotReadAtOnce(t *testing.T) {
	hermetic(t)
	for _, c := range []struct{ body, whole, instance string }{
		{"session: &" + marker + " {<<: *" + marker + "}\n", ": an anchor's value holds an alias of that anchor", ": an anchor's value holds an alias of that anchor"},
		{"session:\n  instance: &" + marker + " {<<: *" + marker + "}\n", ": an anchor's value holds an alias of that anchor", ": an anchor's value holds an alias of that anchor"},
		{"session:\n  instance: &i {name: " + secret + ", <<: *i}\n", ": an anchor's value holds an alias of that anchor", ": an anchor's value holds an alias of that anchor"},
		{"gateway: {credentials: {c: {auth: &a {scheme: " + secret + ", <<: *a}}}}\n", ": gateway.credentials.c: an anchor's value holds an alias of that anchor", ""},
		{"gateway: {run_credentials: &r [*r, " + secret + "]}\n", ": line 1: gateway.run_credentials[0][0] is an alias of a value that holds it", ""},
		{"gateway: {run_credentials: &r [{issuer: " + secret + ", labels: *r}]}\n", ": line 1: gateway.run_credentials[0].labels[0] is an alias of a value that holds it", ""},
		{aliasBomb(9), ": gateway.run_credentials: its aliases expand to more values than qory reads", ""},
		{mergeBomb(9), ": its aliases expand to more values than qory reads", ": its aliases expand to more values than qory reads"},
		{"session: {instance: {<<: " + secret + "}}\n", ": a merge, <<, holds a value that is not a mapping or a list of mappings", ": a merge, <<, holds a value that is not a mapping or a list of mappings"},
	} {
		path := foragerFile(t, c.body)
		for _, r := range []struct {
			load func() (*config.Forager, error)
			want string
		}{{config.LoadForager, c.whole}, {config.LoadForagerInstance, c.instance}} {
			done := make(chan error, 1)
			go func() {
				_, err := r.load()
				done <- err
			}()
			var err error
			select {
			case err = <-done:
			case <-time.After(2 * time.Second):
				t.Fatalf("%q: no answer in 2s", c.body)
			}
			switch {
			case r.want == "" && err != nil:
				t.Errorf("%q: %v, want it read: this reader reads session.instance alone", c.body, err)
			case r.want != "" && (err == nil || err.Error() != path+r.want):
				t.Errorf("%q: %v, want %q", c.body, err, path+r.want)
			}
			if err != nil && (strings.Contains(err.Error(), marker) || strings.Contains(err.Error(), "qak_")) {
				t.Errorf("%q: the refusal holds the value or the anchor: %v", c.body, err)
			}
		}
	}
}
