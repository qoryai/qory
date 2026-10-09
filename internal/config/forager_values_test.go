package config_test

import (
	"encoding/base64"
	"fmt"
	"reflect"
	"runtime"
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
		{"wall:\n  <<: {cpus: [" + secret + "]}\n  cpus: \"2\"\n  pids_limit: [" + secret + "]\n", ": line 4: wall.pids_limit is not a whole number"},
		{"wall:\n  <<: [{cpus: \"2\"}, {cpus: [" + secret + "], pids_limit: [" + secret + "]}]\n", ": line 2: wall.pids_limit is not a whole number"},
		{"wall:\n  pids_limit: [" + secret + "]\n  <<: {cpus: [" + secret + "]}\n", ": line 2: wall.pids_limit is not a whole number"},
		{"wall:\n  <<: {<<: {cpus: [" + secret + "]}, cpus: \"2\", user: [" + secret + "]}\n  adapter: docker\n", ": line 2: wall.user is not a string"},
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
		{"session:\n  gateway:\n    url: https://gateway.example\n    url: " + secret + "\n", ": line 4: session.gateway.url is written twice; it was first written at line 3"},
		{"wall:\n  adapter: docker\n  adapter: " + secret + "\n", ": line 3: wall.adapter is written twice; it was first written at line 2"},
		{"session:\n  instance:\n    name: a\n    name: " + secret + "\n", ": line 4: session.instance.name is written twice; it was first written at line 3"},
		{"session: {gateway: {url: \"https://gateway.example\", certificate_sha256: x, certificate_sha256: " + secret + "}}\n", ": line 1: session.gateway.certificate_sha256 is written twice; it was first written at line 1"},
		{"wall:\n  adapter: &k adapter\n  *k: " + secret + "\n", ": line 3: wall.adapter is written twice; it was first written at line 2"},
		{"session: {? [" + secret + "] : y}\n", ": line 1: session has a key that is not a name"},
		{"? {k: " + secret + "}\n: y\n", ": line 1: the file has a key that is not a name"},
		{"session: {\"\": " + secret + "}\n", ": line 1: key \"\" is not one forager.yaml reads"},
		{"wall: {adapter: docker, !!merge team: " + secret + "}\n", ": line 1: key \"team\" is not one forager.yaml reads"},
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
		{"session:\n  instance:\n    name: a\n    name: " + secret + "\n", ": line 4: session.instance.name is written twice; it was first written at line 3"},
		{"session:\n  run: &k name\n  instance: {name: a, *k: " + secret + "}\n", ": line 3: session.instance.name is written twice; it was first written at line 3"},
		{"session: {? [" + secret + "] : y}\n", ": line 1: session has a key that is not a name"},
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
		{"session: {instance: {name: *" + marker + "}}\n", ": an alias names an anchor that is not defined before it", ": an alias names an anchor that is not defined before it"},
		{"session: {instance: {name: *" + marker + "}}\nwall: &" + marker + " {}\n", ": an alias names an anchor that is not defined before it", ": an alias names an anchor that is not defined before it"},
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

// manyAliases is n aliases of the anchor u, as a flow list's items.
func manyAliases(n int) string {
	return strings.Repeat("*u, ", n-1) + "*u"
}

// TestAWalkAtItsLimitSaysNoAliases writes more aliases than qory's walk of the file
// takes, 150,000 of one value, which the decoder reads: a refusal names a fault the walk
// still finds, or says one without its place, never that the aliases expand too far, and
// a file the decoder reads is read. A key that is not a name beside a merge, in a
// mapping the walk no longer reaches, is still refused, by the decoder's failure.
func TestAWalkAtItsLimitSaysNoAliases(t *testing.T) {
	hermetic(t)
	many := manyAliases(150_000)
	env := "wall: {adapter: docker, env: [&u " + secret + ", " + many + "]}\n"
	issuer := "{issuer: \"https://issuer.example\", audience: a, algorithms: [RS256], keys: [{kid: k1, alg: RS256, public_key_file: f.pem}], labels: {forge: {value: x}, repository: {claims: [&u " + secret + ", " + many + "], join: /}, run_key: {claim: sub}}}"
	for _, c := range []struct{ body, whole, instance string }{
		{env + "session: {run: {timeout: [" + secret + "]}}\n", ": line 2: session.run.timeout is not a string", ""},
		{"wall: {adapter: docker, mounts: &m [" + secret + "], env: [&u " + secret + ", " + many + "], cpus: *m}\n", ": a value is not of the type its key takes", ""},
		{env + "session:\n  instance:\n    ? [" + secret + "]\n    : y\n    <<: {name: box}\n", ": line 4: session.instance has a key that is not a name", ": line 4: session.instance has a key that is not a name"},
		{"gateway: {credentials: {c: &p {env: X, hosts: [h], ? [" + secret + "] : y, <<: {file: f}}}}\n" + env + "session: {instance: *p}\n", ": the YAML decoder failed reading the file", ": line 1: session.instance has a key that is not a name"},
		{"gateway:\n  run_credentials:\n    - " + issuer + "\n", "", ""},
		{"gateway:\n  run_credentials:\n    - " + issuer + "\n    - {issuer: !!int " + secret + "}\n", ": line 4: gateway.run_credentials[1].issuer is tagged !!int, and its value is not of that type", ""},
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
			case <-time.After(30 * time.Second):
				t.Fatalf("%.80q: no answer in 30s", c.body)
			}
			switch {
			case r.want == "" && err != nil:
				t.Errorf("%.80q: %v, want it read", c.body, err)
			case r.want != "" && (err == nil || err.Error() != path+r.want):
				t.Errorf("%.80q: %v, want %q", c.body, err, path+r.want)
			}
			if err != nil && (strings.Contains(err.Error(), marker) || strings.Contains(err.Error(), "aliases")) {
				t.Errorf("%.80q: the refusal holds the value or speaks of aliases: %v", c.body, err)
			}
		}
	}
	foragerFile(t, "gateway:\n  run_credentials:\n    - "+issuer+"\n")
	f, err := config.LoadForager()
	if err != nil {
		t.Fatal(err)
	}
	if len(f.RunCredentials) != 1 || len(f.RunCredentials[0].LabelMapping.Repository.Claims) != 150_001 {
		t.Errorf("gateway.run_credentials read as %d issuers", len(f.RunCredentials))
	}
}

// numbered is n entries "<prefix><i>: 1" of a flow mapping, and repeated is n items of a
// flow list or mapping, each item.
func numbered(prefix string, n int) string {
	keys := make([]string, n)
	for i := range keys {
		keys[i] = fmt.Sprintf("%s%d: 1", prefix, i)
	}
	return strings.Join(keys, ", ")
}

func repeated(item string, n int) string {
	return strings.TrimSuffix(strings.Repeat(item+", ", n), ", ")
}

// TestForagerReadsAtOnceWhatItCannotSayWithoutAValue writes files the decoder's own
// words, or a walk of them, would print a value of or take too long over: a mapping of
// many keys merged again and again, an alias key whose anchor's value is tagged
// !!binary, and a key that is a list or a mapping that only the decoder reaches. Each
// answers at once, and in qory's words, which name no value.
func TestForagerReadsAtOnceWhatItCannotSayWithoutAValue(t *testing.T) {
	hermetic(t)
	const number = "987654321"
	binary := base64.StdEncoding.EncodeToString([]byte(secret))
	env := "wall: {adapter: docker, pids_limit: &p " + number + ", env: [&n X, " + repeated("*n", 100_001) + "]}\n"
	for _, c := range []struct{ body, whole, instance string }{
		{"gateway: {credentials: &a {" + numbered("k", 5000) + "}, server: {url: https://apiary.example, apiary_public_key: [" + repeated("{<<: *a}", 5000) + "]}}\n", ": its aliases expand to more values than qory reads", ""},
		{"x: &a {" + numbered("k", 2000) + "}\nsession: {instance: {<<: [" + repeated("*a", 2000) + "]}}\n", ": its aliases expand to more values than qory reads", ": its aliases expand to more values than qory reads"},
		{"wall:\n  adapter: docker\n  image: a\n  image: b\n  adapter: docker\n", ": line 5: wall.adapter is written twice; it was first written at line 2", ""},
		{"session:\n  instance:\n    name: &a !!binary " + binary + "\n  *a : 1\n", ": line 4: key *a is an alias of a key forager.yaml does not read", ": session.instance.name is not 1 to 64 of A-Z, a-z, 0-9, dot, underscore and dash, starting with a letter or digit"},
		{env + "gateway: {<<: {listen: \"a:1\"}, ? {a: {[*p]: 1}} : x}\n", ": line 2: gateway has a key that is not a name", ""},
		{env + "webhook: &g {<<: {listen: \"a:1\"}, ? {a: {[*p]: 1}} : x}\ngateway: *g\n", ": a value is not of the type its key takes", ""},
	} {
		path := foragerFile(t, c.body)
		for _, r := range []struct {
			load func() (*config.Forager, error)
			want string
		}{{config.LoadForager, c.whole}, {config.LoadForagerInstance, c.instance}} {
			done := make(chan error, 1)
			start := time.Now()
			go func() {
				_, err := r.load()
				done <- err
			}()
			var err error
			select {
			case err = <-done:
			case <-time.After(10 * time.Second):
				t.Fatalf("%.80q: no answer in 10s", c.body)
			}
			if took := time.Since(start); took > 2*time.Second {
				t.Errorf("%.80q: answered in %v", c.body, took)
			}
			switch {
			case r.want == "" && err != nil:
				t.Errorf("%.80q: %v, want it read", c.body, err)
			case r.want != "" && (err == nil || err.Error() != path+r.want):
				t.Errorf("%.80q: %v, want %q", c.body, err, path+r.want)
			}
			if err != nil && (strings.Contains(err.Error(), marker) || strings.Contains(err.Error(), number)) {
				t.Errorf("%.80q: the refusal holds a value: %v", c.body, err)
			}
		}
	}
}

// TestRunCredentialsReadAnAnchorElsewhereInTheFile writes aliases under
// gateway.run_credentials of anchors in other sections: the list is read as the file
// means it, the same as with each value written out, and one the schema refuses is
// refused by its report, which names no value. An alias of an anchor the file does not
// define before it is refused as the decoder refuses it.
func TestRunCredentialsReadAnAnchorElsewhereInTheFile(t *testing.T) {
	hermetic(t)
	const rest = `algorithms: [RS256], keys: [{kid: k1, alg: RS256, public_key_file: f.pem}], labels: {forge: {value: x}, repository: {claim: repo}, run_key: {claim: sub}}`
	foragerFile(t, "gateway:\n  run_credentials:\n    - {issuer: \"https://issuer.example\", audience: box, "+rest+"}\n")
	want, err := config.LoadForager()
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{
		"session: {instance: {name: &aud box}}\ngateway:\n  run_credentials:\n    - {issuer: \"https://issuer.example\", audience: *aud, " + rest + "}\n",
		"session: {instance: {name: &k audience}}\ngateway:\n  run_credentials:\n    - {issuer: \"https://issuer.example\", *k : box, " + rest + "}\n",
	} {
		foragerFile(t, body)
		got, err := config.LoadForager()
		if err != nil {
			t.Errorf("%q: %v, want it read", body, err)
			continue
		}
		if !reflect.DeepEqual(got.RunCredentials, want.RunCredentials) {
			t.Errorf("%q: read as %+v, want %+v", body, got.RunCredentials, want.RunCredentials)
		}
	}
	for _, c := range []struct{ body, want string }{
		{"gateway: {listen: &l 127.0.0.1:8443, run_credentials: [{issuer: *l}]}\n", ": gateway.run_credentials: jsonschema validation failed with 'https://qory.dev/contracts/forager/v1/run-credentials.schema.json#'\n- at '/0': validation failed\n  - at '/0': missing properties 'audience', 'algorithms', 'keys', 'labels'\n  - at '/0/issuer': value is not valid uri"},
		{"wall: {adapter: docker, image: &d " + marker + "}\ngateway:\n  run_credentials:\n    - {issuer: \"https://issuer.example\", audience: box, algorithms: [*d], keys: [{kid: k1, alg: RS256, public_key_file: f.pem}], labels: {forge: {value: x}, repository: {claim: repo}, run_key: {claim: sub}}}\n", ": gateway.run_credentials: jsonschema validation failed with 'https://qory.dev/contracts/forager/v1/run-credentials.schema.json#'\n- at '/0/algorithms/0': value must be one of 'RS256', 'ES256', 'EdDSA'"},
		{"gateway:\n  run_credentials:\n    - {issuer: \"https://issuer.example\", audience: *" + marker + "}\n", ": an alias names an anchor that is not defined before it"},
		{"gateway:\n  run_credentials:\n    - {issuer: \"https://issuer.example\", audience: *" + marker + "}\nsession: {instance: {name: &" + marker + " box}}\n", ": an alias names an anchor that is not defined before it"},
	} {
		path := foragerFile(t, c.body)
		_, err := config.LoadForager()
		if err == nil || err.Error() != path+c.want {
			t.Errorf("%q: %v, want %q", c.body, err, path+c.want)
		}
		if err != nil && (strings.Contains(err.Error(), marker) || strings.Contains(err.Error(), "8443")) {
			t.Errorf("%q: the refusal holds a value: %v", c.body, err)
		}
	}
}

// runCredentialRows are qory config's rows of gateway.run_credentials of the file body.
func runCredentialRows(t *testing.T, body string) []config.Row {
	t.Helper()
	foragerFile(t, body)
	f, err := config.LoadForager()
	if err != nil {
		t.Fatalf("%q: %v, want it read", body, err)
	}
	var rows []config.Row
	for _, r := range f.Rows() {
		if strings.HasPrefix(r.Key, "gateway.run_credentials") {
			rows = append(rows, r)
		}
	}
	return rows
}

// TestConfigRowsOfRunCredentialsAreOfTheListAsRead writes gateway.run_credentials with
// aliases and merges: the list as an alias of an anchor in another section, an issuer as
// one, an issuer that merges another, a merge of several mappings, a key written as an
// alias, and an alias and an anchor inside the section; and keys the decoder takes as a
// merge or as none: !!merge on a name, a quoted "<<", << tagged ! or !!merge, and an
// alias of <<. qory config lists the same rows as of the list written out in full.
func TestConfigRowsOfRunCredentialsAreOfTheListAsRead(t *testing.T) {
	hermetic(t)
	const (
		rest          = `algorithms: [RS256], keys: [{kid: k1, alg: RS256, public_key_file: f.pem}], labels: {forge: {value: x}, repository: {claim: repo}, run_key: {claim: sub}}`
		issuer        = `{issuer: "https://issuer.example", audience: box, ` + rest + `}`
		introspection = `introspection: {url: "https://issuer.example/introspect", client_id: example-gateway, client_secret_file: issuer-secret}`
		program       = "program: /opt/acme/bin/acme-tracker"
	)
	inspecting := strings.TrimSuffix(issuer, "}") + ", " + introspection + "}"
	details := func(d string) string {
		return "gateway:\n  run_credentials:\n    - " + strings.TrimSuffix(issuer, "}") + ", details: " + d + "}\n"
	}
	for _, c := range []struct{ body, written string }{
		{
			"gateway: {integrations: {i: {" + program + ", settings: {s: &rc [" + issuer + "]}}}, run_credentials: *rc}\n",
			"gateway: {integrations: {i: {" + program + ", settings: {s: [" + issuer + "]}}}, run_credentials: [" + issuer + "]}\n",
		},
		{
			"gateway: {integrations: {i: {" + program + ", settings: {s: &it " + inspecting + "}}}, run_credentials: [*it]}\n",
			"gateway: {integrations: {i: {" + program + ", settings: {s: " + inspecting + "}}}, run_credentials: [" + inspecting + "]}\n",
		},
		{
			"gateway:\n  run_credentials:\n    - &i " + issuer + "\n    - {<<: *i, issuer: \"https://other.example\"}\n",
			"gateway:\n  run_credentials:\n    - " + issuer + "\n    - " + strings.Replace(issuer, "issuer.example", "other.example", 1) + "\n",
		},
		{
			"gateway:\n  integrations:\n    i: {" + program + ", settings: {l: &l {run_key: {claim: sub}, repository: {claim: other}}}}\n  run_credentials:\n" +
				"    - {issuer: \"https://issuer.example\", audience: box, algorithms: [RS256], keys: [{kid: k1, alg: RS256, public_key_file: f.pem}], labels: &own {forge: {value: x}, repository: {claim: repo}, run_key: {claim: sub}}}\n" +
				"    - {issuer: \"https://other.example\", audience: box, algorithms: [RS256], keys: [{kid: k1, alg: RS256, public_key_file: f.pem}], labels: {<<: [*l, *own], forge: {value: y}}}\n",
			"gateway:\n  integrations:\n    i: {" + program + ", settings: {l: {run_key: {claim: sub}, repository: {claim: other}}}}\n  run_credentials:\n" +
				"    - {issuer: \"https://issuer.example\", audience: box, algorithms: [RS256], keys: [{kid: k1, alg: RS256, public_key_file: f.pem}], labels: {forge: {value: x}, repository: {claim: repo}, run_key: {claim: sub}}}\n" +
				"    - {issuer: \"https://other.example\", audience: box, algorithms: [RS256], keys: [{kid: k1, alg: RS256, public_key_file: f.pem}], labels: {run_key: {claim: sub}, repository: {claim: other}, forge: {value: y}}}\n",
		},
		{
			"session: {instance: {name: &k audience}}\ngateway:\n  run_credentials:\n    - {issuer: \"https://issuer.example\", *k : box, " + rest + "}\n",
			"session: {instance: {name: audience}}\ngateway:\n  run_credentials:\n    - " + issuer + "\n",
		},
		{
			"gateway:\n  run_credentials:\n    - {issuer: \"https://issuer.example\", audience: &a box, algorithms: [RS256], keys: &k [{kid: k1, alg: RS256, public_key_file: f.pem}], labels: {forge: {value: x}, repository: {claim: repo}, run_key: {claim: sub}}}\n" +
				"    - {issuer: \"https://other.example\", audience: *a, algorithms: [RS256], keys: *k, labels: {forge: {value: x}, repository: {claim: repo}, run_key: {claim: sub}}}\n",
			"gateway:\n  run_credentials:\n    - " + issuer + "\n    - " + strings.Replace(issuer, "issuer.example", "other.example", 1) + "\n",
		},
		{details("{!!merge team: {claim: org}}"), details("{team: {claim: org}}")},
		{details(`{"<<": {claim: org}}`), details(`{"<<": {claim: org}}`)},
		{details("{! <<: {team: {claim: org}}}"), details("{team: {claim: org}}")},
		{details("{!!merge <<: {team: {claim: org}}}"), details("{team: {claim: org}}")},
		{details(`{!!merge "<<": {team: {claim: org}}}`), details("{team: {claim: org}}")},
		{details("{a: {claim: &m <<}, *m : {claim: org}}"), details(`{a: {claim: <<}, "<<": {claim: org}}`)},
		{
			"gateway:\n  run_credentials:\n    - {issuer: \"https://issuer.example\", !!merge audience: box, " + rest + "}\n",
			"gateway:\n  run_credentials:\n    - " + issuer + "\n",
		},
	} {
		want := runCredentialRows(t, c.written)
		got := runCredentialRows(t, c.body)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%q: rows\n%v\nwant, as of the list written out,\n%v", c.body, got, want)
		}
	}
}

// TestRunCredentialsAliasesExpandWithinABound writes a value of 1 MiB anchored in
// another section and aliased under gateway.run_credentials: three aliases are read,
// and 2000, which would make 2 GiB of JSON, are refused at once, without the JSON
// being made, in qory's words, which name no value.
func TestRunCredentialsAliasesExpandWithinABound(t *testing.T) {
	hermetic(t)
	value := strings.Repeat(marker, (1<<20)/len(marker))
	body := func(n int) string {
		return "gateway:\n  integrations: {i: {program: /opt/acme/bin/acme-tracker, settings: {s: &v " + value + "}}}\n  run_credentials:\n" +
			"    - {issuer: \"https://issuer.example\", audience: box, algorithms: [RS256], keys: [{kid: k1, alg: RS256, public_key_file: f.pem}], labels: {forge: {value: x}, repository: {claims: [" + repeated("*v", n) + "], join: /}, run_key: {claim: sub}}}\n"
	}
	foragerFile(t, body(3))
	if _, err := config.LoadForager(); err != nil {
		t.Errorf("three aliases of 1 MiB: %v, want them read", err)
	}
	path := foragerFile(t, body(2000))
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	start := time.Now()
	_, err := config.LoadForager()
	took := time.Since(start)
	runtime.ReadMemStats(&after)
	if want := path + ": gateway.run_credentials: its aliases expand to more values than qory reads"; err == nil || err.Error() != want {
		t.Errorf("2000 aliases of 1 MiB: %v, want %q", err, want)
	}
	if took > 2*time.Second {
		t.Errorf("2000 aliases of 1 MiB took %v", took)
	}
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 512<<20 {
		t.Errorf("2000 aliases of 1 MiB allocated %d MiB", allocated>>20)
	}
}

// TestRunCredentialsMergeChainIsReadAtOnce writes an issuer whose details merge the
// last of a chain of 64000 mappings, each merging the one before it, anchored in a merge
// the issuer itself makes, beside 6400 claims: it is read, and qory config lists every
// detail of the chain, in under 2s. A walk that copied the keys of the chain at each of
// its mappings took several seconds.
func TestRunCredentialsMergeChainIsReadAtOnce(t *testing.T) {
	if raceDetector {
		t.Skip("the race detector makes the read many times slower than its bound")
	}
	hermetic(t)
	const n = 64000
	var b strings.Builder
	b.WriteString("gateway:\n  run_credentials:\n    - {<<: {issuer: [&m0 {d0: {claim: c}}")
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, ", &m%d {d%d: {claim: c}, <<: *m%d}", i, i, i-1)
	}
	b.WriteString("]}, issuer: \"https://issuer.example\", audience: box, algorithms: [RS256], keys: [{kid: k1, alg: RS256, public_key_file: f.pem}], ")
	fmt.Fprintf(&b, "labels: {forge: {value: x}, repository: {claims: [%s], join: /}, run_key: {claim: sub}}, details: {<<: *m%d}}\n", repeated("c", n/10), n)
	foragerFile(t, b.String())
	start := time.Now()
	f, err := config.LoadForager()
	took := time.Since(start)
	if err != nil {
		t.Fatalf("a chain of %d merges: %v, want it read", n, err)
	}
	if took > 2*time.Second {
		t.Errorf("a chain of %d merges took %v", n, took)
	}
	var details string
	for _, r := range f.Rows() {
		if r.Key == "gateway.run_credentials[0].details" {
			details = r.Value
		}
	}
	if got := strings.Count(details, ": {claim: c}"); got != n+1 {
		t.Errorf("a chain of %d merges: details lists %d details, want %d", n, got, n+1)
	}
}

// TestNoForagerRefusalPrintsAKeyWrittenAsAnAlias writes a value, then a key that is an
// alias of it, in every section: a refusal names such a key by its alias, never by the
// value it stands for, in both readers.
func TestNoForagerRefusalPrintsAKeyWrittenAsAnAlias(t *testing.T) {
	hermetic(t)
	const value = "session:\n  instance:\n    name: &u " + marker + "\n"
	issuer := "{issuer: \"https://issuer.example\", audience: a, algorithms: [RS256], keys: [{kid: k1, alg: RS256, public_key_file: f.pem}], labels: {forge: {value: x}}}"
	for _, c := range []struct{ body, whole, instance string }{
		{value + "*u : x\n", ": line 4: key *u is an alias of a key forager.yaml does not read", ""},
		{value + "  *u : x\n", ": line 4: key *u is an alias of a key forager.yaml does not read", ""},
		{"session:\n  instance:\n    name: &u " + marker + "\n    *u : x\n", ": line 4: key *u is an alias of a key forager.yaml does not read", ""},
		{value + "  gateway:\n    url: https://gateway.example\n    *u : x\n", ": line 6: key *u is an alias of a key forager.yaml does not read", ""},
		{value + "  run:\n    *u : x\n", ": line 5: key *u is an alias of a key forager.yaml does not read", ""},
		{value + "wall:\n  adapter: docker\n  *u : [x]\n", ": line 6: key *u is an alias of a key forager.yaml does not read", ""},
		{value + "wall:\n  adapter: docker\n  <<: {*u : x}\n", ": line 6: key *u is an alias of a key forager.yaml does not read", ""},
		{value + "gateway:\n  server:\n    url: https://qory.example\n    apiary_public_key:\n      - alg: ed25519\n        *u : x\n", ": line 9: key *u is an alias of a key forager.yaml does not read", ""},
		{value + "wall:\n  adapter: docker\n  *u : x\n  *u : y\n", ": line 7: wall.*u is written twice; it was first written at line 6", ""},
		{"session:\n  run: {timeout: &u " + marker + "}\n  instance:\n    *u : x\n    *u : y\n", ": line 5: session.instance.*u is written twice; it was first written at line 4", ": line 5: session.instance.*u is written twice; it was first written at line 4"},
		{"session:\n  run: {timeout: 1h}\ngateway:\n  run_credentials:\n    - " + issuer + "\n    - issuer: &u " + marker + "\n      labels:\n        *u : !!int x\n", ": line 8: gateway.run_credentials[1].labels.*u is tagged !!int, and its value is not of that type", ""},
		{value + "gateway:\n  credentials:\n    *u : {env: X, hosts: x}\n", ": line 6: gateway.credentials.u.hosts is not a list of strings", ""},
		{value + "gateway:\n  credentials:\n    c:\n      env: X\n      *u : x\n", ": gateway.credentials.c: key \"u\" is not one", ""},
		{value + "gateway:\n  integrations:\n    i:\n      program: /x\n      *u : x\n", ": gateway.integrations.i: key \"u\" is not one", ""},
		{value + "wall:\n  adapter: docker\n  images:\n    go:\n      ref: a\n      *u : x\n", ": wall.images.go: key \"u\" is not one; an image has ref, runtime, docker", ""},
	} {
		path := foragerFile(t, c.body)
		for _, r := range []struct {
			load func() (*config.Forager, error)
			want string
		}{{config.LoadForager, c.whole}, {config.LoadForagerInstance, c.instance}} {
			_, err := r.load()
			switch {
			case r.want == "" && err != nil:
				t.Errorf("%q: %v, want it read: this reader reads session.instance alone", c.body, err)
			case r.want != "" && (err == nil || err.Error() != path+r.want):
				t.Errorf("%q: %v, want %q", c.body, err, path+r.want)
			}
			if err != nil && strings.Contains(err.Error(), marker) {
				t.Errorf("%q: the refusal holds the value an alias key stands for: %v", c.body, err)
			}
		}
	}
}

// TestNoRunCredentialsReportPrintsAnAliasKeysValue writes, under
// gateway.run_credentials, keys that are aliases of a value: one whose value is no name
// the schema allows where the key stands is refused by its alias, as the other
// sections refuse it, in a mapping, a merged one, a list's item and a oneOf; one the
// schema allows among any names, under details, is never named in the schema's report.
// No refusal holds the value.
func TestNoRunCredentialsReportPrintsAnAliasKeysValue(t *testing.T) {
	hermetic(t)
	const (
		head      = "gateway:\n  run_credentials:\n    - "
		rest      = `algorithms: [RS256], keys: [{kid: k1, alg: RS256, public_key_file: f.pem}], labels: {forge: {value: x}, repository: {claim: repo}, run_key: {claim: sub}}`
		notIssuer = ": gateway.run_credentials is not a list of issuers as run-credentials.schema.json defines them"
	)
	for _, c := range []struct{ body, want string }{
		{head + "{issuer: &s " + marker + ", *s: 1, audience: a}\n", ": line 3: key *s is an alias of a key forager.yaml does not read"},
		{head + "{issuer: \"https://issuer.example\", audience: &u " + marker + ", " + rest + ", <<: {*u : 1}}\n", ": line 3: key *u is an alias of a key forager.yaml does not read"},
		{head + "{issuer: \"https://issuer.example\", audience: &u " + marker + ", algorithms: [RS256], keys: [{kid: k1, alg: RS256, public_key_file: f.pem, *u : 1}], labels: {forge: {value: x}, repository: {claim: repo}, run_key: {claim: sub}}}\n", ": line 3: key *u is an alias of a key forager.yaml does not read"},
		{head + "{issuer: \"https://issuer.example\", audience: &u " + marker + ", algorithms: [RS256], keys: [{kid: k1, alg: RS256, public_key_file: f.pem}], labels: {forge: {*u : x}, repository: {claim: repo}, run_key: {claim: sub}}}\n", ": line 3: key *u is an alias of a key forager.yaml does not read"},
		{head + "{issuer: \"https://issuer.example\", audience: &d \"" + marker + "=1\", " + rest + ", details: {*d : {claim: ref}}}\n", ": line 3: key *d is an alias of a key forager.yaml does not read"},
		{head + "{issuer: \"https://issuer.example\", audience: &d " + marker + ", " + rest + ", details: {*d : {claim: \"\"}}}\n", notIssuer},
	} {
		path := foragerFile(t, c.body)
		_, err := config.LoadForager()
		if err == nil || err.Error() != path+c.want {
			t.Errorf("%q: %v, want %q", c.body, err, path+c.want)
		}
		if err != nil && strings.Contains(err.Error(), marker) {
			t.Errorf("%q: the refusal holds the value an alias key stands for: %v", c.body, err)
		}
	}
	// An alias key of a name the schema lists is read, as is one of a name details
	// allows.
	for _, body := range []string{
		head + "{issuer: \"https://a.example\", audience: &k audience, " + rest + "}\n    - {issuer: \"https://b.example\", *k : box, " + rest + "}\n",
		head + "{issuer: \"https://issuer.example\", audience: &d branch, " + rest + ", details: {*d : {claim: ref}}}\n",
	} {
		foragerFile(t, body)
		if _, err := config.LoadForager(); err != nil {
			t.Errorf("%q: %v, want it read", body, err)
		}
	}
}

// TestForagerRefusesAKeyThatIsNotANameBesideAMerge is a mapping that merges and has a
// key that is a list or a mapping, which the YAML decoder fails on with a panic: it is
// refused before it is decoded, at once and without the value, by both readers, and
// under gateway.credentials and gateway.run_credentials too.
func TestForagerRefusesAKeyThatIsNotANameBesideAMerge(t *testing.T) {
	hermetic(t)
	for _, c := range []struct{ body, whole, instance string }{
		{"wall:\n  ? [" + secret + "]\n  : y\n  <<: {adapter: docker}\n", ": line 2: wall has a key that is not a name", ""},
		{"wall:\n  <<: {adapter: docker}\n  ? {k: " + secret + "}\n  : y\n", ": line 3: wall has a key that is not a name", ""},
		{"session:\n  instance:\n    ? [" + secret + "]\n    : y\n    <<: {name: a}\n", ": line 3: session.instance has a key that is not a name", ": line 3: session.instance has a key that is not a name"},
		{"session:\n  <<: {run: {timeout: 1h}}\n  ? {k: " + secret + "}\n  : y\n", ": line 3: session has a key that is not a name", ": line 3: session has a key that is not a name"},
		{"? [" + secret + "]\n: y\n<<: {wall: {adapter: docker}}\n", ": line 1: the file has a key that is not a name", ": line 1: the file has a key that is not a name"},
		{"session: {gateway: {url: \"https://gateway.example\", ? [" + secret + "] : y, <<: {}}}\n", ": line 1: session.gateway has a key that is not a name", ""},
		{"gateway: {egress: {mode: observe, ? [" + secret + "] : y, <<: {}}}\n", ": line 1: gateway.egress has a key that is not a name", ""},
		{"gateway: {credentials: {c: {env: X, auth: {? [" + secret + "] : y, <<: {scheme: basic}}}}}\n", ": line 1: gateway.credentials.c.auth has a key that is not a name", ""},
		{"gateway: {run_credentials: [{1: a, <<: {? [" + secret + "] : y}}]}\n", ": line 1: gateway.run_credentials[0].<< has a key that is not a name", ""},
		{"gateway: {run_credentials: [{issuer: x, labels: {1: a, <<: [{a: 1}, {? [" + secret + "] : y}]}}]}\n", ": line 1: gateway.run_credentials[0].labels.<<[1] has a key that is not a name", ""},
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
				t.Errorf("%q: the refusal holds the value: %v", c.body, err)
			}
		}
	}
}
