package config_test

import (
	"encoding/base64"
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
	{"gateway: {egress: {mode: observe, allow: [a.example, %s]}}\n", "gateway.egress.allow[1]"},
	{"gateway: {egress: {mode: observe, deny: %s}}\n", "gateway.egress.deny"},
	{"gateway: {egress: {mode: observe, deny: [a.example, %s]}}\n", "gateway.egress.deny[1]"},
	{"gateway: {egress: {mode: observe, paths: %s}}\n", "gateway.egress.paths"},
	{"gateway: {egress: {mode: observe, paths: {%s: []}}}\n", "gateway.egress.paths"},
	{"gateway: {egress: {mode: observe, paths: {a.example: %s}}}\n", "gateway.egress.paths"},
	{"gateway: {egress: {mode: observe, paths: {a.example: [/v1, %s]}}}\n", "gateway.egress.paths"},
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
	{"gateway: {credentials: {c: {adapter: [/x, %s]}}}\n", "gateway.credentials.c.adapter[1]"},
	{"gateway: {credentials: {c: {argument: %s}}}\n", "gateway.credentials.c.argument"},
	{"gateway: {credentials: {c: {hosts: %s}}}\n", "gateway.credentials.c.hosts"},
	{"gateway: {credentials: {c: {hosts: [a.example, %s]}}}\n", "gateway.credentials.c.hosts[1]"},
	{"gateway: {credentials: {c: {paths: %s}}}\n", "gateway.credentials.c.paths"},
	{"gateway: {credentials: {c: {paths: [/v1, %s]}}}\n", "gateway.credentials.c.paths[1]"},
	{"gateway: {credentials: {c: {auth: {scheme: %s}}}}\n", "gateway.credentials.c.auth.scheme"},
	{"gateway: {credentials: {c: {auth: {username: %s}}}}\n", "gateway.credentials.c.auth.username"},
	{"gateway: {credentials: {c: {auth: {header: %s}}}}\n", "gateway.credentials.c.auth.header"},
	{"gateway: {credentials: {c: {placeholders: %s}}}\n", "gateway.credentials.c.placeholders"},
	{"gateway: {credentials: {c: {placeholders: [T, %s]}}}\n", "gateway.credentials.c.placeholders[1]"},
	{"gateway: {credentials: {c: {auth: {oauth: %s}}}}\n", "gateway.credentials.c.auth.oauth"},
	{"gateway: {credentials: {c: {format: %s}}}\n", "gateway.credentials.c.format"},
	{"gateway: {credentials: {c: {endpoint: %s}}}\n", "gateway.credentials.c.endpoint"},
	{"gateway: {credentials: {c: {key_prefixes: %s}}}\n", "gateway.credentials.c.key_prefixes"},
	{"gateway: {credentials: {c: {key_prefixes: [sk-, %s]}}}\n", "gateway.credentials.c.key_prefixes[1]"},
	{"gateway: {lock_memory: %s}\n", "gateway.lock_memory"},
	{"gateway: {run_limits: %s}\n", "gateway.run_limits"},
	{"gateway: {run_limits: {connections: %s}}\n", "gateway.run_limits.connections"},
	{"gateway: {run_limits: {requests_per_second: %s}}\n", "gateway.run_limits.requests_per_second"},
	{"gateway: {run_limits: {burst: %s}}\n", "gateway.run_limits.burst"},
	{"gateway: {stack_hosts: %s}\n", "gateway.stack_hosts"},
	{"gateway: {stack_hosts: [a.example, %s]}\n", "gateway.stack_hosts[1]"},
	{"gateway: {tools: %s}\n", "gateway.tools"},
	{"gateway: {tools: {t: {command: %s}}}\n", "gateway.tools.t.command"},
	{"gateway: {tools: {t: {command: [/x, %s]}}}\n", "gateway.tools.t.command[1]"},
	{"gateway: {tools: {t: {command: [/x], argument: %s}}}\n", "gateway.tools.t.argument"},
	{"gateway: {tools: {t: {command: [/x], serves: %s}}}\n", "gateway.tools.t.serves"},
	{"gateway: {tools: {t: {command: [/x], serves: [a.example, %s]}}}\n", "gateway.tools.t.serves[1]"},
	{"gateway: {tools: {t: {command: [/x], placeholders: %s}}}\n", "gateway.tools.t.placeholders"},
	{"gateway: {tools: {t: {command: [/x], placeholders: [T, %s]}}}\n", "gateway.tools.t.placeholders[1]"},
	{"gateway: {integrations: %s}\n", "gateway.integrations"},
	{"gateway: {integrations: {i: {program: %s}}}\n", "gateway.integrations.i.program"},
	{"gateway: {integrations: {i: {settings: {s: %s}}}}\n", "gateway.integrations.i.settings"},
	{"gateway: {listen: %s}\n", "gateway.listen"},
	{"gateway: {tls: {certificate: %s, key: k.pem}}\n", "gateway.tls.certificate"},
	{"gateway: {tls: {certificate: c.pem, key: %s}}\n", "gateway.tls.key"},
	{"gateway: {server: {url: \"https://qory.example\", via: {url: %s}}}\n", "gateway.server.via.url"},
	{"gateway: {server: {url: \"https://qory.example\", via: {url: \"https://gateway.example\", ca_file: %s}}}\n", "gateway.server.via.ca_file"},
	{"gateway: {server: {url: \"https://qory.example\", via: {url: \"https://gateway.example\", certificate_sha256: %s}}}\n", "gateway.server.via.certificate_sha256"},
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
	{"wall: {adapter: docker, images_allow: %s}\n", "wall.images_allow"},
	{"wall: {adapter: docker, images_allow: [registry.example/, %s]}\n", "wall.images_allow[1]"},
	{"wall: {adapter: docker, command: %s}\n", "wall.command"},
	{"wall: {adapter: docker, helper: %s}\n", "wall.helper"},
	{"wall: {adapter: docker, env: %s}\n", "wall.env"},
	{"wall: {adapter: docker, env: [HOME, %s]}\n", "wall.env[1]"},
	{"wall: {adapter: docker, user: %s}\n", "wall.user"},
	{"wall: {adapter: docker, mounts: %s}\n", "wall.mounts"},
	{"wall: {adapter: docker, mounts: [/srv, %s]}\n", "wall.mounts[1]"},
	{"wall: {adapter: docker, cpus: %s}\n", "wall.cpus"},
	{"wall: {adapter: docker, memory: %s}\n", "wall.memory"},
	{"wall: {adapter: docker, pids_limit: %s}\n", "wall.pids_limit"},
	{"wall: {adapter: docker, shm_size: %s}\n", "wall.shm_size"},
	{"wall: {adapter: docker, ca_env: %s}\n", "wall.ca_env"},
	{"wall: {adapter: docker, ca_env: [X, %s]}\n", "wall.ca_env[1]"},
	{"wall: {adapter: docker, setup: %s}\n", "wall.setup"},
	{"wall: {adapter: docker, engine_proxy_file: %s}\n", "wall.engine_proxy_file"},
	{"machine: %s\n", "machine"},
	{"webhook: %s\n", "webhook"},
}

// TestNoForagerRefusalPrintsAValue writes into every key of forager.yaml, and into an
// entry of every list of strings, a value the reader cannot take: an access key secret
// under a tag its value does not fit, and a mapping or a list where another kind goes.
// Each is refused, naming the file and the key, and no refusal holds the value.
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
			case !strings.Contains(err.Error(), path):
				t.Errorf("%q: %v, want one naming the file", body, err)
			case strings.Contains(err.Error(), marker) || strings.Contains(strings.ToLower(err.Error()), "qak_"):
				t.Errorf("%q: the refusal holds the value: %v", body, err)
			case strings.Contains(err.Error(), "config.") || strings.Contains(err.Error(), "struct {"):
				t.Errorf("%q: the refusal holds a Go type: %v", body, err)
			case !strings.Contains(err.Error(), k.key) && !strings.Contains(err.Error(), "secret") && !strings.Contains(err.Error(), `Key "k" is not one`):
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
		{"gateway: {server: {url: \"https://qory.example\", via: {url: !!int " + secret + "}}}\n", "Remove the tag !!int from gateway.server.via.url, or write a value of that type.\n  gateway.server.via.url is tagged !!int, and its value is not of that type.\n  It is at line 1 of {path}."},
		{"gateway:\n  server:\n    url: https://qory.example\n    via:\n      url: https://gateway.example\n      ca_file: [" + secret + "]\n", "Set gateway.server.via.ca_file to a string.\n  gateway.server.via.ca_file is not a string.\n  It is at line 6 of {path}."},
		{"gateway:\n  server:\n    url: https://qory.example\n    via:\n      url: https://gateway.example\n      ca_file: &a {k: " + secret + "}\n      certificate_sha256: *a\n", "Set gateway.server.via.ca_file to a string.\n  gateway.server.via.ca_file is not a string.\n  It is at line 6 of {path}."},
		{"gateway: {egress: {mode: {k: " + secret + "}}}\n", "Set gateway.egress.mode to a string.\n  gateway.egress.mode is not a string.\n  It is at line 1 of {path}."},
		{"gateway:\n  egress:\n    mode: observe\n    allow: " + secret + "\n", "Set gateway.egress.allow to a list of strings.\n  gateway.egress.allow is not a list of strings.\n  It is at line 4 of {path}."},
		{"gateway:\n  egress:\n    mode: observe\n    allow:\n      - a.example\n      - !!int " + secret + "\n", "Set gateway.egress.allow[1] to a lower-case host name or a *. suffix.\n  A host is a lower-case host name or a *. suffix. One that ends in a number is an IPv4 address in canonical form. It has no port, path, or scheme.\n  gateway.egress.allow is in {path}."},
		{"gateway: {server: {url: \"https://qory.example\", apiary_public_key: " + secret + "}}\n", "Set gateway.server.apiary_public_key to a list.\n  gateway.server.apiary_public_key is not a list.\n  It is at line 1 of {path}."},
		{"gateway: {server: {url: \"https://qory.example\", apiary_public_key: [" + secret + "]}}\n", "Set gateway.server.apiary_public_key[0] to a mapping.\n  gateway.server.apiary_public_key[0] is not a mapping.\n  It is at line 1 of {path}."},
		{"wall:\n  adapter: docker\n  pids_limit: " + secret + "\n", "Set wall.pids_limit to a whole number.\n  wall.pids_limit is not a whole number.\n  It is at line 3 of {path}."},
		{"wall:\n  <<: {adapter: docker, cpus: [" + secret + "]}\n", "Set wall.cpus to a string.\n  wall.cpus is not a string.\n  It is at line 2 of {path}."},
		{"wall:\n  <<: {cpus: [" + secret + "]}\n  cpus: \"2\"\n  pids_limit: [" + secret + "]\n", "Set wall.pids_limit to a whole number.\n  wall.pids_limit is not a whole number.\n  It is at line 4 of {path}."},
		{"wall:\n  <<: [{cpus: \"2\"}, {cpus: [" + secret + "], pids_limit: [" + secret + "]}]\n", "Set wall.pids_limit to a whole number.\n  wall.pids_limit is not a whole number.\n  It is at line 2 of {path}."},
		{"wall:\n  pids_limit: [" + secret + "]\n  <<: {cpus: [" + secret + "]}\n", "Set wall.pids_limit to a whole number.\n  wall.pids_limit is not a whole number.\n  It is at line 2 of {path}."},
		{"wall:\n  <<: {<<: {cpus: [" + secret + "]}, cpus: \"2\", user: [" + secret + "]}\n  adapter: docker\n", "Set wall.user to a string.\n  wall.user is not a string.\n  It is at line 2 of {path}."},
		{"session: " + secret + "\n", "Set session to a mapping.\n  session is not a mapping.\n  It is at line 1 of {path}."},
		{"[" + secret + "]\n", "Write forager.yaml as a mapping of sections.\n  The file is not a mapping of sections.\n  It is at line 1 of {path}."},
		{secret + "\n", "Write forager.yaml as a mapping of sections.\n  The file is not a mapping of sections.\n  It is at line 1 of {path}."},
		{"apiVersion: !!binary " + secret + "\n", "Remove the tag !!binary from apiVersion, or write a value of that type.\n  apiVersion is tagged !!binary, and its value is not of that type.\n  It is at line 1 of {path}."},
		{"gateway:\n  credentials:\n    c:\n      hosts: {k: " + secret + "}\n", "Set gateway.credentials.c.hosts to a list of strings.\n  gateway.credentials.c.hosts is not a list of strings.\n  It is at line 4 of {path}."},
		{"gateway:\n  credentials:\n    c:\n      auth: {scheme: !!timestamp " + secret + "}\n", "Remove the tag !!timestamp from gateway.credentials.c.auth.scheme, or write a value of that type.\n  gateway.credentials.c.auth.scheme is tagged !!timestamp, and its value is not of that type.\n  It is at line 4 of {path}."},
		{"gateway: {egress: {mode: " + marker + "}}\n", "Set gateway.egress.mode to observe or enforce.\n  gateway.egress.mode is neither.\n  gateway.egress.mode is in {path}."},
		{"gateway: {egress: {mode: enforce, allow: [a.example, \"" + marker + ":443\"]}}\n", "Set gateway.egress.allow[1] to a lower-case host name or a *. suffix.\n  A host is a lower-case host name or a *. suffix. One that ends in a number is an IPv4 address in canonical form. It has no port, path, or scheme.\n  gateway.egress.allow is in {path}."},
		{"gateway: {egress: {mode: observe, deny: [\"" + marker + "\"]}}\n", "Set gateway.egress.deny[0] to a lower-case host name or a *. suffix.\n  A host is a lower-case host name or a *. suffix. One that ends in a number is an IPv4 address in canonical form. It has no port, path, or scheme.\n  gateway.egress.deny is in {path}."},
		{"gateway: {server: {url: \"https://qory.example\", access_key_id: " + marker + "}}\n", "Set gateway.server.access_key_id to ak_ and 16 lower-case Crockford base32 characters.\n  The value is not one.\n  gateway.server.access_key_id is in {path}."},
		{"gateway: {server: {url: \"https://qory.example\", apiary_public_key: [{alg: " + marker + ", public_key: x}]}}\n", "Set gateway.server.apiary_public_key[0].alg to ed25519.\n  ed25519 is the one there is.\n  gateway.server.apiary_public_key[0] is in {path}."},
		{"gateway: {server: {url: \"https://qory.example\", apiary_public_key: [{alg: ed25519, public_key: " + marker + "}]}}\n", "Set gateway.server.apiary_public_key[0].public_key to 32 bytes in base64url without padding.\n  The value is not one.\n  gateway.server.apiary_public_key[0] is in {path}."},
		{"gateway: {server: {url: \"https://qory.example\", apiary_public_key: [{alg: ed25519, public_key: AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA}]}}\n", "Set gateway.server.apiary_public_key[0].public_key to a key qory can verify under.\n  The public key cannot be used: it is a point of small order.\n  gateway.server.apiary_public_key[0] is in {path}."},
		{"gateway: {listen: " + marker + "}\n", "Set gateway.listen to host:port, such as 0.0.0.0:8443.\n  The value is not host:port.\n  gateway.listen is in {path}."},
		{"gateway: {credentials: {c: {file: " + marker + "}}}\n", "Set gateway.credentials.c.file to an absolute path.\n  The value is not one.\n  gateway.credentials.c is in {path}."},
		{"gateway: {egress: {mode: observe, paths: {" + secret + ": []}}}\n", "Set each key of gateway.egress.paths to a lower-case host name or a *. suffix.\n  A host is a lower-case host name or a *. suffix. One that ends in a number is an IPv4 address in canonical form. It has no port, path, or scheme.\n  gateway.egress.paths is in {path}."},
		{"gateway: {egress: {mode: observe, paths: {\"" + marker + ":443\": " + secret + "}}}\n", "Set each key of gateway.egress.paths to a lower-case host name or a *. suffix.\n  A host is a lower-case host name or a *. suffix. One that ends in a number is an IPv4 address in canonical form. It has no port, path, or scheme.\n  gateway.egress.paths is in {path}."},
		{"gateway: {egress: {mode: observe, paths: {a.example: " + secret + "}}}\n", "Set gateway.egress.paths.a.example to a list of paths.\n  The value is not a list.\n  gateway.egress.paths is in {path}."},
		{"gateway: {egress: {mode: observe, paths: {a.example: [/v1, " + secret + "]}}}\n", "Set gateway.egress.paths.a.example[1] to a path from /, matched whole or up to a final *.\n  The entry is not one.\n  gateway.egress.paths is in {path}."},
		{"gateway: {egress: {mode: observe, paths: {a.example: [\"/" + secret + "\", \"/" + secret + "\"]}}}\n", "List each path of gateway.egress.paths.a.example once.\n  The list has a path twice.\n  gateway.egress.paths is in {path}."},
		{"gateway:\n  egress:\n    mode: observe\n    paths:\n      a.example: [/v1]\n      a.example: [\"/" + secret + "\"]\n", "Remove one of the two gateway.egress.paths.a.example keys.\n  Line 6 writes it again; it was first written at line 5.\n  gateway.egress.paths is in {path}."},
		{"gateway:\n  egress:\n    mode: observe\n    paths:\n      " + secret + ": [/v1]\n      " + secret + ": [/v2]\n", "Set each key of gateway.egress.paths to a lower-case host name or a *. suffix.\n  A host is a lower-case host name or a *. suffix. One that ends in a number is an IPv4 address in canonical form. It has no port, path, or scheme.\n  gateway.egress.paths is in {path}."},
		{"gateway: {tools: {t: {command: [" + marker + "], serves: [t.example]}}}\n", "Set gateway.tools.t.command to a program's absolute path, then its arguments.\n  The value is not one.\n  gateway.tools.t is in {path}."},
		{"gateway:\n  tools:\n    t:\n      command: [/x]\n      serves: {k: " + secret + "}\n", "Set gateway.tools.t.serves to a list of strings.\n  gateway.tools.t.serves is not a list of strings.\n  It is at line 5 of {path}."},
		{"gateway: {integrations: {i: {program: " + marker + "/x}}}\n", "Set gateway.integrations.i.program to an absolute path or a name on the PATH.\n  The value is a relative path.\n  gateway.integrations.i is in {path}."},
		{"gateway: {server: {url: \"https://qory.example\", via: {url: \"https://gateway.example\", certificate_sha256: " + marker + "}}}\n", "Set gateway.server.via.certificate_sha256 to the SHA-256 of a public key.\n  The value is not 44 characters of standard base64 with padding, ending in =.\n  gateway.server.via.certificate_sha256 is in {path}."},
		{"session: {instance: {name: \"-" + marker + "\"}}\n", "Set session.instance.name to 1 to 64 of A-Z, a-z, 0-9, dot, underscore, and dash.\n  The name starts with a letter or digit.\n  session.instance.name is in {path}."},
		{"session: {run: {timeout: " + marker + "}}\n", "Set session.run.timeout to a duration above zero, such as 5h30m or 30s.\n  The value is not one.\n  session.run.timeout is in {path}."},
		{"session: {run: {stop_grace: " + marker + "}}\n", "Set session.run.stop_grace to a duration above zero, such as 5h30m or 30s.\n  The value is not one.\n  session.run.stop_grace is in {path}."},
		{"session: {run: {stop_signal: " + marker + "}}\n", "Set session.run.stop_signal to SIGTERM, SIGINT, SIGHUP, SIGQUIT, SIGUSR1, or SIGUSR2.\n  The value is none of them.\n  session.run.stop_signal is in {path}."},
		{"wall: {adapter: docker, helper: " + marker + "}\n", "Set wall.helper to an absolute path.\n  The value is not one.\n  wall.helper is in {path}."},
		{"wall: {adapter: docker, mounts: [/srv, " + marker + "]}\n", "Set wall.mounts[1] to an absolute path, with :ro after it for a read-only one.\n  The entry is not an absolute path.\n  wall.mounts is in {path}."},
		{"wall: {adapter: docker, pids_limit: -7}\n", "Set wall.pids_limit to 1 or more.\n  wall.pids_limit is below 1.\n  wall.pids_limit is in {path}."},
		{"wall: {adapter: docker, ca_env: [\"" + marker + "-X\"]}\n", "Set wall.ca_env[0] to a variable's name.\n  The entry is not one.\n  wall.ca_env is in {path}."},
		{"gateway: {stack_hosts: [a.example, \"https://oauth2:" + marker + "@git.example.com\"]}\n", "Set gateway.stack_hosts[1] to a lower-case host name, an IPv4 address, or a *. suffix.\n  An entry is a host such as git.example.com, with no port, path, or scheme.\n  The file is {path}."},
		{"gateway:\n  stack_hosts:\n    - !!int " + secret + "\n", "Set gateway.stack_hosts[0] to a lower-case host name, an IPv4 address, or a *. suffix.\n  An entry is a host such as git.example.com, with no port, path, or scheme.\n  It is at line 3 of {path}."},
		{"gateway:\n  stack_hosts: {k: " + secret + "}\n", "Set gateway.stack_hosts to a list of strings.\n  gateway.stack_hosts is not a list of strings.\n  It is at line 2 of {path}."},
		{"wall: {adapter: docker, images_allow: [registry.example/, \"" + marker + "/\"]}\n", "Set wall.images_allow[1] to the start of an image reference pinned by digest, in lower case.\n  The start is a whole part of it: a registry, a name or a path ending in a slash, or a name and its @. The entry is not one.\n  wall.images_allow is in {path}."},
		{"wall: {adapter: docker, env: [HOME, \"" + marker + "=x\"]}\n", "Set wall.env[1] to a variable's name.\n  The entry is not one. The value comes from the environment, never from this file.\n  wall.env is in {path}."},
		{"wall: {adapter: docker, env: [QORY_ACCESS_KEY_SECRET]}\n", "Remove wall.env[0].\n  It is a variable that is Forager's and never the agent's.\n  wall.env is in {path}."},
		{"wall: {adapter: docker, images: {go: {ref: \"--" + marker + "\"}}}\n", "Set wall.images.go.ref to an image reference, such as ghcr.io/acme/agent@sha256:…\n  The value is not one.\n  wall.images.go is in {path}."},
		{"wall: {adapter: docker, images: {go: {ref: a, runtime: \"--" + marker + "\"}}}\n", "Set wall.images.go.runtime to a container runtime's name, such as sysbox-runc.\n  The value is not one.\n  wall.images.go is in {path}."},
		{"apiVersion: " + marker + "\n", "Set apiVersion to qory.dev/v1alpha1.\n  This qory reads qory.dev/v1alpha1.\n  apiVersion is in {path}."},
		{"gateway:\n  server:\n    url: https://qory.example\n    via:\n      url: https://gateway.example\n      url: " + secret + "\n", "Remove one of the two keys gateway.server.via.url.\n  gateway.server.via.url is written twice; it was first written at line 5.\n  It is at line 6 of {path}."},
		{"wall:\n  adapter: docker\n  adapter: " + secret + "\n", "Remove one of the two keys wall.adapter.\n  wall.adapter is written twice; it was first written at line 2.\n  It is at line 3 of {path}."},
		{"session:\n  instance:\n    name: a\n    name: " + secret + "\n", "Remove one of the two keys session.instance.name.\n  session.instance.name is written twice; it was first written at line 3.\n  It is at line 4 of {path}."},
		{"gateway: {server: {url: \"https://qory.example\", via: {url: \"https://gateway.example\", certificate_sha256: x, certificate_sha256: " + secret + "}}}\n", "Remove one of the two keys gateway.server.via.certificate_sha256.\n  gateway.server.via.certificate_sha256 is written twice; it was first written at line 1.\n  It is at line 1 of {path}."},
		{"wall:\n  adapter: &k adapter\n  *k: " + secret + "\n", "Remove one of the two keys wall.adapter.\n  wall.adapter is written twice; it was first written at line 2.\n  It is at line 3 of {path}."},
		{"session: {? [" + secret + "] : y}\n", "Give each key of session a name.\n  session has a key that is not a name.\n  It is at line 1 of {path}."},
		{"? {k: " + secret + "}\n: y\n", "Give each key of the file a name.\n  The file has a key that is not a name.\n  It is at line 1 of {path}."},
		{"session: {\"\": " + secret + "}\n", "Remove the key, or rename it to one forager.yaml reads.\n  Key \"\" is not one forager.yaml reads.\n  It is at line 1 of {path}."},
		{"wall: {adapter: docker, !!merge team: " + secret + "}\n", "Remove the key, or rename it to one forager.yaml reads.\n  Key \"team\" is not one forager.yaml reads.\n  It is at line 1 of {path}."},
	} {
		path := foragerFile(t, c.body)
		_, err := config.LoadForager()
		if err == nil || err.Error() != strings.ReplaceAll(c.want, "{path}", path) {
			t.Errorf("%q: %v, want %q", c.body, err, strings.ReplaceAll(c.want, "{path}", path))
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
		{"session: {instance: {name: !!int " + secret + "}}\n", "Remove the tag !!int from session.instance.name, or write a value of that type.\n  session.instance.name is tagged !!int, and its value is not of that type.\n  It is at line 1 of {path}."},
		{"session:\n  instance:\n    name: [" + secret + "]\n", "Set session.instance.name to a string.\n  session.instance.name is not a string.\n  It is at line 3 of {path}."},
		{"session: {instance: {name: \"-" + marker + "\"}}\n", "Set session.instance.name to 1 to 64 of A-Z, a-z, 0-9, dot, underscore, and dash.\n  The name starts with a letter or digit.\n  session.instance.name is in {path}."},
		{"session:\n  instance:\n    name: a\n    name: " + secret + "\n", "Remove one of the two keys session.instance.name.\n  session.instance.name is written twice; it was first written at line 3.\n  It is at line 4 of {path}."},
		{"session:\n  run: &k name\n  instance: {name: a, *k: " + secret + "}\n", "Remove one of the two keys session.instance.name.\n  session.instance.name is written twice; it was first written at line 3.\n  It is at line 3 of {path}."},
		{"session: {? [" + secret + "] : y}\n", "Give each key of session a name.\n  session has a key that is not a name.\n  It is at line 1 of {path}."},
	} {
		path := foragerFile(t, c.body)
		_, err := config.LoadForagerInstance()
		if err == nil || err.Error() != strings.ReplaceAll(c.want, "{path}", path) {
			t.Errorf("%q: %v, want %q", c.body, err, strings.ReplaceAll(c.want, "{path}", path))
		}
	}
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
		{"session: &" + marker + " {<<: *" + marker + "}\n", "Write the values out in full, in place of the aliases and merges that qory cannot read.\n  The decoder refuses the file: an anchor's value holds an alias of that anchor.\n  The file is {path}.", "Write the values out in full, in place of the aliases and merges that qory cannot read.\n  The decoder refuses the file: an anchor's value holds an alias of that anchor.\n  The file is {path}."},
		{"session:\n  instance: &" + marker + " {<<: *" + marker + "}\n", "Write the values out in full, in place of the aliases and merges that qory cannot read.\n  The decoder refuses the file: an anchor's value holds an alias of that anchor.\n  The file is {path}.", "Write the values out in full, in place of the aliases and merges that qory cannot read.\n  The decoder refuses the file: an anchor's value holds an alias of that anchor.\n  The file is {path}."},
		{"session:\n  instance: &i {name: " + secret + ", <<: *i}\n", "Write the values out in full, in place of the aliases and merges that qory cannot read.\n  The decoder refuses the file: an anchor's value holds an alias of that anchor.\n  The file is {path}.", "Write the values out in full, in place of the aliases and merges that qory cannot read.\n  The decoder refuses the file: an anchor's value holds an alias of that anchor.\n  The file is {path}."},
		{"gateway: {credentials: {c: {auth: &a {scheme: " + secret + ", <<: *a}}}}\n", "Write the values out in full, in place of the aliases and merges that qory cannot read.\n  The decoder refuses gateway.credentials.c: an anchor's value holds an alias of that anchor.\n  The file is {path}.", ""},
		{mergeBomb(9), "Write the values out in full, in place of the aliases and merges that qory cannot read.\n  The decoder refuses the file: its aliases expand to more values than qory reads.\n  The file is {path}.", "Write the values out in full, in place of the aliases and merges that qory cannot read.\n  The decoder refuses the file: its aliases expand to more values than qory reads.\n  The file is {path}."},
		{"session: {instance: {<<: " + secret + "}}\n", "Write the values out in full, in place of the aliases and merges that qory cannot read.\n  The decoder refuses the file: a merge, <<, holds a value that is not a mapping or a list of mappings.\n  The file is {path}.", "Write the values out in full, in place of the aliases and merges that qory cannot read.\n  The decoder refuses the file: a merge, <<, holds a value that is not a mapping or a list of mappings.\n  The file is {path}."},
		{"session: {instance: {name: *" + marker + "}}\n", "Write the values out in full, in place of the aliases and merges that qory cannot read.\n  The decoder refuses the file: an alias names an anchor that is not defined before it.\n  The file is {path}.", "Write the values out in full, in place of the aliases and merges that qory cannot read.\n  The decoder refuses the file: an alias names an anchor that is not defined before it.\n  The file is {path}."},
		{"session: {instance: {name: *" + marker + "}}\nwall: &" + marker + " {}\n", "Write the values out in full, in place of the aliases and merges that qory cannot read.\n  The decoder refuses the file: an alias names an anchor that is not defined before it.\n  The file is {path}.", "Write the values out in full, in place of the aliases and merges that qory cannot read.\n  The decoder refuses the file: an alias names an anchor that is not defined before it.\n  The file is {path}."},
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
			case r.want != "" && (err == nil || err.Error() != strings.ReplaceAll(r.want, "{path}", path)):
				t.Errorf("%q: %v, want %q", c.body, err, strings.ReplaceAll(r.want, "{path}", path))
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
// takes, 120,000 of one entry of gateway.server.apiary_public_key, which the decoder
// reads: a refusal names a fault the walk still finds, or says one without its place,
// never that the aliases expand too far, and a file the decoder reads is read. A key
// that is not a name beside a merge, in a mapping the walk no longer reaches, is still
// refused, by the decoder's failure.
func TestAWalkAtItsLimitSaysNoAliases(t *testing.T) {
	hermetic(t)
	server := "  server:\n    url: https://qory.example\n    apiary_public_key: [&u {alg: ed25519, public_key: " + secret + "}, " + manyAliases(120_000) + "]\n"
	pins := "gateway:\n" + server
	for _, c := range []struct{ body, whole, instance string }{
		{pins + "session: {run: {timeout: [" + secret + "]}}\n", "Set session.run.timeout to a string.\n  session.run.timeout is not a string.\n  It is at line 5 of {path}.", ""},
		{pins + "wall: {adapter: docker, mounts: &m [" + secret + "], cpus: *m}\n", "Set each value in the file to the type its key takes.\n  A value is not of the type its key takes.\n  The file is {path}.", ""},
		{pins + "session:\n  instance:\n    ? [" + secret + "]\n    : y\n    <<: {name: box}\n", "Give each key of session.instance a name.\n  session.instance has a key that is not a name.\n  It is at line 7 of {path}.", "Give each key of session.instance a name.\n  session.instance has a key that is not a name.\n  It is at line 7 of {path}."},
		{"gateway:\n  credentials: {c: &p {env: X, hosts: [h], ? [" + secret + "] : y, <<: {file: f}}}\n" + server + "session: {instance: *p}\n", "Check the YAML of the file.\n  The YAML decoder failed reading the file.\n  The file is {path}.", "Give each key of session.instance a name.\n  session.instance has a key that is not a name.\n  It is at line 2 of {path}."},
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
			case r.want != "" && (err == nil || err.Error() != strings.ReplaceAll(r.want, "{path}", path)):
				t.Errorf("%.80q: %v, want %q", c.body, err, strings.ReplaceAll(r.want, "{path}", path))
			}
			if err != nil && (strings.Contains(err.Error(), marker) || strings.Contains(err.Error(), "aliases")) {
				t.Errorf("%.80q: the refusal holds the value or speaks of aliases: %v", c.body, err)
			}
		}
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
	merges := "wall: {adapter: docker, pids_limit: &p " + number + "}\nsession: {instance: &n {}, <<: [" + repeated("*n", 100_001) + "]}\n"
	for _, c := range []struct{ body, whole, instance string }{
		{"gateway: {credentials: &a {" + numbered("k", 5000) + "}, server: {url: https://apiary.example, apiary_public_key: [" + repeated("{<<: *a}", 5000) + "]}}\n", "Write the values out in full, in place of the aliases and merges that qory cannot read.\n  The decoder refuses the file: its aliases expand to more values than qory reads.\n  The file is {path}.", ""},
		{"x: &a {" + numbered("k", 2000) + "}\nsession: {instance: {<<: [" + repeated("*a", 2000) + "]}}\n", "Write the values out in full, in place of the aliases and merges that qory cannot read.\n  The decoder refuses the file: its aliases expand to more values than qory reads.\n  The file is {path}.", "Write the values out in full, in place of the aliases and merges that qory cannot read.\n  The decoder refuses the file: its aliases expand to more values than qory reads.\n  The file is {path}."},
		{"wall:\n  adapter: docker\n  image: a\n  image: b\n  adapter: docker\n", "Remove one of the two keys wall.adapter.\n  wall.adapter is written twice; it was first written at line 2.\n  It is at line 5 of {path}.", ""},
		{"session:\n  instance:\n    name: &a !!binary " + binary + "\n  *a : 1\n", "Remove the key, or rename it to one forager.yaml reads.\n  Key *a is an alias of a key forager.yaml does not read.\n  It is at line 4 of {path}.", "Set session.instance.name to 1 to 64 of A-Z, a-z, 0-9, dot, underscore, and dash.\n  The name starts with a letter or digit.\n  session.instance.name is in {path}."},
		{merges + "gateway: {<<: {listen: \"a:1\"}, ? {a: {[*p]: 1}} : x}\n", "Give each key of gateway a name.\n  gateway has a key that is not a name.\n  It is at line 3 of {path}.", ""},
		{merges + "webhook: &g {<<: {listen: \"a:1\"}, ? {a: {[*p]: 1}} : x}\ngateway: *g\n", "Set each value in the file to the type its key takes.\n  A value is not of the type its key takes.\n  The file is {path}.", ""},
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
			case r.want != "" && (err == nil || err.Error() != strings.ReplaceAll(r.want, "{path}", path)):
				t.Errorf("%.80q: %v, want %q", c.body, err, strings.ReplaceAll(r.want, "{path}", path))
			}
			if err != nil && (strings.Contains(err.Error(), marker) || strings.Contains(err.Error(), number)) {
				t.Errorf("%.80q: the refusal holds a value: %v", c.body, err)
			}
		}
	}
}

// TestNoForagerRefusalPrintsAKeyWrittenAsAnAlias writes a value, then a key that is an
// alias of it, in every section: a refusal names such a key by its alias, never by the
// value it stands for, in both readers.
func TestNoForagerRefusalPrintsAKeyWrittenAsAnAlias(t *testing.T) {
	hermetic(t)
	const value = "session:\n  instance:\n    name: &u " + marker + "\n"
	for _, c := range []struct{ body, whole, instance string }{
		{value + "*u : x\n", "Remove the key, or rename it to one forager.yaml reads.\n  Key *u is an alias of a key forager.yaml does not read.\n  It is at line 4 of {path}.", ""},
		{value + "  *u : x\n", "Remove the key, or rename it to one forager.yaml reads.\n  Key *u is an alias of a key forager.yaml does not read.\n  It is at line 4 of {path}.", ""},
		{"session:\n  instance:\n    name: &u " + marker + "\n    *u : x\n", "Remove the key, or rename it to one forager.yaml reads.\n  Key *u is an alias of a key forager.yaml does not read.\n  It is at line 4 of {path}.", ""},
		{value + "gateway:\n  server:\n    url: https://qory.example\n    via:\n      url: https://gateway.example\n      *u : x\n", "Remove the key, or rename it to one forager.yaml reads.\n  Key *u is an alias of a key forager.yaml does not read.\n  It is at line 9 of {path}.", ""},
		{value + "  run:\n    *u : x\n", "Remove the key, or rename it to one forager.yaml reads.\n  Key *u is an alias of a key forager.yaml does not read.\n  It is at line 5 of {path}.", ""},
		{value + "wall:\n  adapter: docker\n  *u : [x]\n", "Remove the key, or rename it to one forager.yaml reads.\n  Key *u is an alias of a key forager.yaml does not read.\n  It is at line 6 of {path}.", ""},
		{value + "wall:\n  adapter: docker\n  <<: {*u : x}\n", "Remove the key, or rename it to one forager.yaml reads.\n  Key *u is an alias of a key forager.yaml does not read.\n  It is at line 6 of {path}.", ""},
		{value + "gateway:\n  server:\n    url: https://qory.example\n    apiary_public_key:\n      - alg: ed25519\n        *u : x\n", "Remove the key, or rename it to one forager.yaml reads.\n  Key *u is an alias of a key forager.yaml does not read.\n  It is at line 9 of {path}.", ""},
		{value + "wall:\n  adapter: docker\n  *u : x\n  *u : y\n", "Remove one of the two keys wall.*u.\n  wall.*u is written twice; it was first written at line 6.\n  It is at line 7 of {path}.", ""},
		{"session:\n  run: {timeout: &u " + marker + "}\n  instance:\n    *u : x\n    *u : y\n", "Remove one of the two keys session.instance.*u.\n  session.instance.*u is written twice; it was first written at line 4.\n  It is at line 5 of {path}.", "Remove one of the two keys session.instance.*u.\n  session.instance.*u is written twice; it was first written at line 4.\n  It is at line 5 of {path}."},
		{value + "gateway:\n  credentials:\n    *u : {env: X, hosts: x}\n", "Set gateway.credentials.u.hosts to a list of strings.\n  gateway.credentials.u.hosts is not a list of strings.\n  It is at line 6 of {path}.", ""},
		{value + "gateway:\n  credentials:\n    c:\n      env: X\n      *u : x\n", "Remove the key, or rename it to one forager.yaml reads.\n  gateway.credentials.c: key \"u\" is not one.\n  The file is {path}.", ""},
		{value + "gateway:\n  integrations:\n    i:\n      program: /x\n      *u : x\n", "Remove the key, or rename it to one forager.yaml reads.\n  gateway.integrations.i: key \"u\" is not one.\n  The file is {path}.", ""},
		{value + "wall:\n  adapter: docker\n  images:\n    go:\n      ref: a\n      *u : x\n", "Remove the key, or rename it to one forager.yaml reads.\n  wall.images.go: key \"u\" is not one. An image has ref, runtime, docker.\n  The file is {path}.", ""},
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
			case r.want != "" && (err == nil || err.Error() != strings.ReplaceAll(r.want, "{path}", path)):
				t.Errorf("%q: %v, want %q", c.body, err, strings.ReplaceAll(r.want, "{path}", path))
			}
			if err != nil && strings.Contains(err.Error(), marker) {
				t.Errorf("%q: the refusal holds the value an alias key stands for: %v", c.body, err)
			}
		}
	}
}

// TestForagerRefusesAKeyThatIsNotANameBesideAMerge is a mapping that merges and has a
// key that is a list or a mapping, which the YAML decoder fails on with a panic: it is
// refused before it is decoded, at once and without the value, by both readers, and
// under gateway.credentials too.
func TestForagerRefusesAKeyThatIsNotANameBesideAMerge(t *testing.T) {
	hermetic(t)
	for _, c := range []struct{ body, whole, instance string }{
		{"wall:\n  ? [" + secret + "]\n  : y\n  <<: {adapter: docker}\n", "Give each key of wall a name.\n  wall has a key that is not a name.\n  It is at line 2 of {path}.", ""},
		{"wall:\n  <<: {adapter: docker}\n  ? {k: " + secret + "}\n  : y\n", "Give each key of wall a name.\n  wall has a key that is not a name.\n  It is at line 3 of {path}.", ""},
		{"session:\n  instance:\n    ? [" + secret + "]\n    : y\n    <<: {name: a}\n", "Give each key of session.instance a name.\n  session.instance has a key that is not a name.\n  It is at line 3 of {path}.", "Give each key of session.instance a name.\n  session.instance has a key that is not a name.\n  It is at line 3 of {path}."},
		{"session:\n  <<: {run: {timeout: 1h}}\n  ? {k: " + secret + "}\n  : y\n", "Give each key of session a name.\n  session has a key that is not a name.\n  It is at line 3 of {path}.", "Give each key of session a name.\n  session has a key that is not a name.\n  It is at line 3 of {path}."},
		{"? [" + secret + "]\n: y\n<<: {wall: {adapter: docker}}\n", "Give each key of the file a name.\n  The file has a key that is not a name.\n  It is at line 1 of {path}.", "Give each key of the file a name.\n  The file has a key that is not a name.\n  It is at line 1 of {path}."},
		{"gateway: {server: {url: \"https://qory.example\", via: {url: \"https://gateway.example\", ? [" + secret + "] : y, <<: {}}}}\n", "Give each key of gateway.server.via a name.\n  gateway.server.via has a key that is not a name.\n  It is at line 1 of {path}.", ""},
		{"gateway: {egress: {mode: observe, ? [" + secret + "] : y, <<: {}}}\n", "Give each key of gateway.egress a name.\n  gateway.egress has a key that is not a name.\n  It is at line 1 of {path}.", ""},
		{"gateway: {credentials: {c: {env: X, auth: {? [" + secret + "] : y, <<: {scheme: basic}}}}}\n", "Give each key of gateway.credentials.c.auth a name.\n  gateway.credentials.c.auth has a key that is not a name.\n  It is at line 1 of {path}.", ""},
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
			case r.want != "" && (err == nil || err.Error() != strings.ReplaceAll(r.want, "{path}", path)):
				t.Errorf("%q: %v, want %q", c.body, err, strings.ReplaceAll(r.want, "{path}", path))
			}
			if err != nil && (strings.Contains(err.Error(), marker) || strings.Contains(err.Error(), "qak_")) {
				t.Errorf("%q: the refusal holds the value: %v", c.body, err)
			}
		}
	}
}
