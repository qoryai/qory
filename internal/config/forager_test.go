package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/qoryai/forager/accesskey"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"

	"github.com/qoryai/qory/internal/config"
)

// serverKey is a server's signing key of the test's own, and the pin of it as
// forager.yaml writes one.
func serverKey(t *testing.T) (*accesskey.Key, string) {
	t.Helper()
	k, err := accesskey.Generate()
	if err != nil {
		t.Fatal(err)
	}
	return k, "[{alg: ed25519, public_key: " + k.PublicKey().String() + "}]"
}

// foragerFile writes the machine's forager.yaml under the configuration directory.
func foragerFile(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", config.ForagerFileName)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestForagerFileReadsBothSections reads gateway.egress, gateway.server and
// session.instance, lists them with the file as origin and the pin by its fingerprint,
// and carries them on the configuration a checkout loads.
func TestForagerFileReadsBothSections(t *testing.T) {
	hermetic(t)
	signer, pin := serverKey(t)
	path := foragerFile(t, "apiVersion: qory.dev/v1alpha1\ngateway:\n  egress:\n    mode: enforce\n    allow: [api.anthropic.com, \"*.github.com\"]\n    deny: [gist.github.com, \"*.ads.example\"]\n  server:\n    url: https://qory.example\n    access_key_id: ak_f1xt0re000000000\n    apiary_public_key: "+pin+"\nsession:\n  instance:\n    name: build-01\n")
	c, err := config.Load(t.TempDir(), true)
	if err != nil {
		t.Fatal(err)
	}
	r := c.Forager
	if r == nil || r.File != path || r.Egress == nil || r.Egress.Mode != "enforce" || strings.Join(r.Egress.Allow, " ") != "api.anthropic.com *.github.com" || strings.Join(r.Egress.Deny, " ") != "gist.github.com *.ads.example" {
		t.Fatalf("egress read as %+v", r)
	}
	if r.Server == nil || r.Server.URL != "https://qory.example" || r.Server.AccessKeyID != "ak_f1xt0re000000000" || r.Server.AccessKeyIDFrom != path || len(r.Server.Pin) != 1 || r.Server.Pin[0].PublicKey != signer.PublicKey().String() || r.Server.PinFrom != path || r.InstanceName != "build-01" {
		t.Fatalf("server read as %+v", r.Server)
	}
	rows := map[string]config.Row{}
	for _, row := range c.Rows() {
		rows[row.Key] = row
	}
	for key, want := range map[string]string{"gateway.egress.mode": "enforce", "gateway.egress.allow": "api.anthropic.com, *.github.com", "gateway.egress.deny": "gist.github.com, *.ads.example", "gateway.server.url": "https://qory.example", "gateway.server.access_key_id": "ak_f1xt0re000000000",
		"gateway.server.apiary_public_key": "fingerprint " + signer.Fingerprint(), "session.instance.name": "build-01"} {
		if rows[key].Value != want || rows[key].Origin != path {
			t.Errorf("%s: %+v, want %q from %s", key, rows[key], want, path)
		}
	}
}

// TestForagerFileSectionsMapAsBefore is every section of forager.yaml read into the
// fields the run takes: gateway holds egress, server, credentials and integrations,
// session holds instance and run, and wall stays at the top. qory access-key enrol's
// reader of the instance's name alone reads session.instance.name too.
func TestForagerFileSectionsMapAsBefore(t *testing.T) {
	hermetic(t)
	path := foragerFile(t, `apiVersion: qory.dev/v1alpha1
gateway:
  egress:
    mode: enforce
    allow: [api.example.com]
    deny: [ads.example.com]
  server:
    url: https://apiary.example
    access_key_id: ak_0123456789abcdef
  credentials:
    model:
      env: MODEL_TOKEN
      hosts: [api.example.com]
  integrations:
    github:
      settings: {owner: example}
session:
  instance:
    name: build-01
  run:
    timeout: 2h
    stop_signal: SIGINT
    stop_grace: 20s
wall:
  adapter: docker
  image: example.com/agent:1
`)
	c, err := config.Load(t.TempDir(), true)
	if err != nil {
		t.Fatal(err)
	}
	r := c.Forager
	switch {
	case r == nil || r.File != path:
		t.Fatalf("read as %+v", r)
	case r.Egress == nil || r.Egress.Mode != "enforce" || strings.Join(r.Egress.Allow, " ") != "api.example.com" || strings.Join(r.Egress.Deny, " ") != "ads.example.com":
		t.Errorf("gateway.egress read as %+v", r.Egress)
	case r.Server == nil || r.Server.URL != "https://apiary.example" || r.Server.AccessKeyID != "ak_0123456789abcdef":
		t.Errorf("gateway.server read as %+v", r.Server)
	case len(r.Credentials) != 1 || r.Credentials[0].Name != "model" || r.Credentials[0].Env != "MODEL_TOKEN":
		t.Errorf("gateway.credentials read as %+v", r.Credentials)
	case len(r.Integrations) != 1 || r.Integrations[0].Key != "github" || r.Integrations[0].Program != "qory-github" || string(r.Integrations[0].Settings) != `{"owner":"example"}`:
		t.Errorf("gateway.integrations read as %+v", r.Integrations)
	case r.InstanceName != "build-01":
		t.Errorf("session.instance.name read as %q", r.InstanceName)
	case r.Timeout != 2*time.Hour || r.StopSignal != "SIGINT" || r.StopGrace != 20*time.Second:
		t.Errorf("session.run read as %v, %q, %v", r.Timeout, r.StopSignal, r.StopGrace)
	case r.Wall == nil || r.Wall.Adapter != config.WallDocker || r.Wall.Image != "example.com/agent:1":
		t.Errorf("wall read as %+v", r.Wall)
	}
	in, err := config.LoadForagerInstance()
	if err != nil || in == nil || in.File != path || in.InstanceName != "build-01" {
		t.Errorf("the instance alone: %+v, %v", in, err)
	}
	foragerFile(t, "gateway: {server: {url: \"https://apiary.example\"}}\n")
	if in, err := config.LoadForagerInstance(); err != nil || in == nil || in.InstanceName != "" {
		t.Errorf("no session section: %+v, %v", in, err)
	}
}

// TestForagerFileReadsTheWall reads the wall section, lists it, and lists no wall as the
// default when the file names none.
func TestForagerFileReadsTheWall(t *testing.T) {
	hermetic(t)
	path := foragerFile(t, "wall:\n  adapter: docker\n  image: example.com/agent:1\n  command: podman\n  helper: /opt/qory/qory-linux\n  env: [ANTHROPIC_API_KEY, GH_TOKEN]\n  user: \"1000:1000\"\n  mounts: [/srv/data:ro, /srv/cache]\n  cpus: \"3.5\"\n  memory: 14g\n  pids_limit: 4096\n  shm_size: 2g\n  ca_env: [SSL_CERT_FILE, MY_TOOLS_CA]\nsession:\n  run:\n    timeout: 5h30m\n    stop_signal: SIGINT\n    stop_grace: 30s\ngateway:\n  credentials:\n    product:\n      adapter: [/opt/adapters/git-host, --repo, \"${argument}\"]\n      argument: \"[a-z0-9-]+/[a-z0-9-]+\"\n      hosts: [\"*.example.com\"]\n      placeholders: [GIT_HOST_TOKEN]\n    model:\n      env: MODEL_TOKEN\n      hosts: [api.model.example]\n      auth: {scheme: header, header: X-Api-Key}\n")
	c, err := config.Load(t.TempDir(), true)
	if err != nil {
		t.Fatal(err)
	}
	w := c.Forager.Wall
	if w == nil || w.Adapter != config.WallDocker || w.Image != "example.com/agent:1" || w.Command != "podman" || w.Helper != "/opt/qory/qory-linux" || w.User != "1000:1000" || strings.Join(w.Env, " ") != "ANTHROPIC_API_KEY GH_TOKEN" {
		t.Fatalf("wall read as %+v", w)
	}
	rows := map[string]config.Row{}
	for _, row := range c.Rows() {
		rows[row.Key] = row
	}
	for key, want := range map[string]string{"wall.adapter": "docker", "wall.image": "example.com/agent:1 (a reference)", "wall.env": "ANTHROPIC_API_KEY, GH_TOKEN", "wall.command": "podman", "wall.helper": "/opt/qory/qory-linux",
		"wall.mounts": "/srv/data:ro, /srv/cache", "wall.cpus": "3.5", "wall.memory": "14g", "wall.pids_limit": "4096", "wall.shm_size": "2g",
		"session.run.timeout": "5h30m0s", "session.run.stop_grace": "30s", "session.run.stop_signal": "SIGINT", "wall.ca_env": "SSL_CERT_FILE, MY_TOOLS_CA",
		"gateway.credentials.product": "adapter /opt/adapters/git-host", "gateway.credentials.model": "env MODEL_TOKEN"} {
		if rows[key].Value != want || rows[key].Origin != path {
			t.Errorf("%s: %+v, want %q from %s", key, rows[key], want, path)
		}
	}
	foragerFile(t, "gateway: {egress: {mode: observe}}\n")
	c, err = config.Load(t.TempDir(), true)
	if err != nil || c.Forager.Wall != nil {
		t.Fatalf("no wall section: %+v, %v", c.Forager, err)
	}
	for _, row := range c.Rows() {
		if row.Key == "wall.adapter" && (row.Value != "(none)" || row.Origin != config.Default) {
			t.Errorf("no wall listed as %+v", row)
		}
	}
}

// TestForagerFileDefaults is no file, an empty file and a file with one section: what is
// absent is observe everything and files only, listed as defaults, and the secret may
// come from the environment.
func TestForagerFileDefaults(t *testing.T) {
	hermetic(t)
	c, err := config.Load(t.TempDir(), true)
	if err != nil || c.Forager != nil {
		t.Fatalf("no file: %+v, %v", c.Forager, err)
	}
	rows := map[string]config.Row{}
	for _, row := range c.Rows() {
		rows[row.Key] = row
	}
	if rows["gateway.egress.mode"].Value != "observe" || rows["gateway.egress.mode"].Origin != config.Default || rows["gateway.egress.deny"].Value != "(none)" || rows["gateway.server.url"].Value != "(none)" {
		t.Errorf("default rows %+v", rows)
	}
	foragerFile(t, "apiVersion: qory.dev/v1alpha1\n")
	if c, err = config.Load(t.TempDir(), true); err != nil || c.Forager == nil || c.Forager.Egress != nil || c.Forager.Server != nil {
		t.Errorf("empty file: %+v, %v", c.Forager, err)
	}
	signer, _ := serverKey(t)
	serverVariables(t, config.ServerVariables{AccessKeyID: "ak_0123456789abcdef", ApiaryPublicKey: `[{"alg":"ed25519","public_key":"` + signer.PublicKey().String() + `"}]`})
	foragerFile(t, "gateway:\n  server:\n    url: http://127.0.0.1:8787\n")
	c, err = config.Load(t.TempDir(), true)
	if err != nil || c.Forager.Server == nil || c.Forager.Server.AccessKeyID != "ak_0123456789abcdef" || c.Forager.Server.AccessKeyIDFrom != "$QORY_ACCESS_KEY_ID" || len(c.Forager.Server.Pin) != 1 || c.Forager.Server.PinFrom != "$QORY_APIARY_PUBLIC_KEY" || c.Forager.Egress != nil {
		t.Fatalf("the id and the pin from the environment: %+v, %v", c.Forager, err)
	}
	rows = map[string]config.Row{}
	for _, row := range c.Rows() {
		rows[row.Key] = row
	}
	if rows["gateway.server.access_key_id"].Origin != "$QORY_ACCESS_KEY_ID" || rows["gateway.server.apiary_public_key"].Value != "fingerprint "+signer.Fingerprint() || rows["session.instance.name"].Origin != config.Default {
		t.Errorf("rows from the environment: %+v", rows)
	}

	// Neither is required in the file, since enrolment writes them; both are required
	// of a run.
	serverVariables(t, config.ServerVariables{})
	if c, err = config.Load(t.TempDir(), true); err != nil || c.Forager.Server.AccessKeyID != "" || c.Forager.Server.Pin != nil {
		t.Fatalf("a server section with a url alone: %+v, %v", c.Forager.Server, err)
	}
}

// TestForagerFileRefusesTheIDAndThePinTwice is a forager.yaml that sets the access key's
// id or the pin while the environment sets it too, and values in the environment that
// are not one: each is refused, the variable named and no secret quoted.
func TestForagerFileRefusesTheIDAndThePinTwice(t *testing.T) {
	hermetic(t)
	signer, pin := serverKey(t)
	envPin := `[{"alg":"ed25519","public_key":"` + signer.PublicKey().String() + `"}]`
	path := foragerFile(t, "gateway:\n  server:\n    url: https://qory.example\n    access_key_id: ak_f1xt0re000000000\n    apiary_public_key: "+pin+"\n")
	serverVariables(t, config.ServerVariables{AccessKeyID: "ak_0123456789abcdef"})
	if _, err := config.Load(t.TempDir(), true); err == nil || err.Error() != path+": gateway.server.access_key_id is set, and so is QORY_ACCESS_KEY_ID; set one of them" {
		t.Errorf("the id twice: %v", err)
	}
	serverVariables(t, config.ServerVariables{ApiaryPublicKey: envPin})
	if _, err := config.Load(t.TempDir(), true); err == nil || err.Error() != path+": gateway.server.apiary_public_key is set, and so is QORY_APIARY_PUBLIC_KEY; set one of them" {
		t.Errorf("the pin twice: %v", err)
	}
	foragerFile(t, "gateway:\n  server:\n    url: https://qory.example\n")
	const secret = "qak_AQIDBAUGBwgJCgsMDQ4PEBESExQVFhcYGRobHB0eHyA"
	for _, c := range []struct{ id, pin, want string }{
		{"AK_F1XT0RE000000000", "", `QORY_ACCESS_KEY_ID: the access key id "AK_F1XT0RE000000000" is not ak_`},
		{secret, "", "QORY_ACCESS_KEY_ID: the access key id (a value that contains an access key secret)"},
		{"", `{"alg":"ed25519"}`, "QORY_APIARY_PUBLIC_KEY: the pin is not a list"},
		{"", `[{"alg":"ed25519","public_key":"` + secret + `"}]`, "QORY_APIARY_PUBLIC_KEY: the document contains an access key secret"},
		{"", `[{"alg":"ed25519","public_key":"rcFAEfgtHFbZVqpPnXPYhYNhpgYEhSXg0Ixjjcdd2Mc"}]`, "QORY_APIARY_PUBLIC_KEY: it lists the Forager contract's published fixture key"},
	} {
		serverVariables(t, config.ServerVariables{AccessKeyID: c.id, ApiaryPublicKey: c.pin})
		_, err := config.Load(t.TempDir(), true)
		if err == nil || !strings.HasPrefix(err.Error(), c.want) || strings.Contains(err.Error(), "AQIDBAUG") {
			t.Errorf("%q %q: %v, want %q", c.id, c.pin, err, c.want)
		}
	}
}

// TestForagerFileRefusesTheWorkspaceSecretVariable is QORY_SERVER_SECRET, which held a
// workspace access key's secret: with a server section it is refused, the variable named
// and its value never quoted, and the message says to connect the machine as a node,
// with qory access-key enrol or a key generated on the node's page in Qory Apiary.
func TestForagerFileRefusesTheWorkspaceSecretVariable(t *testing.T) {
	hermetic(t)
	foragerFile(t, "gateway:\n  server:\n    url: https://qory.example\n")
	serverVariables(t, config.ServerVariables{WorkspaceSecret: "sixteen-characters-at-least"})
	_, err := config.Load(t.TempDir(), true)
	want := "QORY_SERVER_SECRET holds a workspace access key's secret, which servers no longer accept; unset it, and connect this machine as a node: run qory access-key enrol <server> <code>, or generate a key on the node's page in Qory Apiary and set the QORY_ variables it shows; see https://github.com/qoryai/qory/blob/main/docs/run.md#the-access-key-and-the-instance"
	if err == nil || err.Error() != want {
		t.Errorf("QORY_SERVER_SECRET: %v, want %q", err, want)
	}
}

// TestForagerFileRefusesAMistake is every refusal, each naming the file and the key.
func TestForagerFileRefusesAMistake(t *testing.T) {
	hermetic(t)
	for _, c := range []struct{ body, want string }{
		{"gateway: {egres: {mode: observe}}\n", `key "egres" is not one forager.yaml reads`},
		{"egress: {mode: observe}\n", `key "egress" is not one forager.yaml reads`},
		{"server: {url: \"https://qory.example\"}\n", `key "server" is not one forager.yaml reads`},
		{"credentials: {}\n", `key "credentials" is not one forager.yaml reads`},
		{"integrations: {}\n", `key "integrations" is not one forager.yaml reads`},
		{"instance: {name: build-01}\n", `key "instance" is not one forager.yaml reads`},
		{"run: {timeout: 1h}\n", `key "run" is not one forager.yaml reads`},
		{"gateway: {wall: {adapter: docker}}\n", `key "wall" is not one forager.yaml reads`},
		{"session: {egress: {mode: observe}}\n", `key "egress" is not one forager.yaml reads`},
		{"gateway: [egress]\n", "gateway"},
		{"gateway: {egress: {allow: [a.example]}}\n", "gateway.egress.mode is required"},
		{"gateway: {egress: {mode: log}}\n", `gateway.egress.mode "log" is not observe or enforce`},
		{"gateway: {egress: {mode: enforce, allow: [\"api.example.com:443\"]}}\n", `gateway.egress.allow: "api.example.com:443" is not a lower-case host name or a *. suffix`},
		{"gateway: {egress: {mode: observe, deny: [\"tracker.example:443\"]}}\n", `gateway.egress.deny: "tracker.example:443" is not a lower-case host name or a *. suffix`},
		{"gateway: {server: {access_key_id: ak_f1xt0re000000000}}\n", "gateway.server.url is required"},
		{"gateway: {server: {url: \"ftp://x\"}}\n", `gateway.server.url "ftp://x" is not an https URL, or an http URL to this machine`},
		{"gateway: {server: {url: \"http://qory.example\"}}\n", `gateway.server.url "http://qory.example" is http to a host that is not this machine`},
		{"gateway: {server: {url: \"https://qory.example/v1/events\"}}\n", `gateway.server.url "https://qory.example/v1/events" is more than a scheme and a host`},
		{"gateway: {server: {url: \"https://qory.example/\"}}\n", `is more than a scheme and a host`},
		{"gateway: {server: {url: \"https://qory.example/?qak_AQIDBAUGBwgJCgsMDQ4PEBESExQVFhcYGRobHB0eHyA\"}}\n", "gateway.server.url: the document contains an access key secret"},
		{"gateway: {server: {url: \"https://qory.example\", access_key_id: AK_F1XT0RE000000000}}\n", `gateway.server.access_key_id: the access key id "AK_F1XT0RE000000000" is not ak_ and 16 lower-case Crockford base32 characters`},
		{"gateway: {server: {url: \"https://qory.example\", access_key_id: ak_f1xt0re0000000}}\n", `gateway.server.access_key_id: the access key id "ak_f1xt0re0000000" is not ak_`},
		{"gateway: {server: {url: \"https://qory.example\", access_key: ak_f1xt0re000000000}}\n", "gateway.server.access_key is a workspace access key, which servers no longer accept; remove gateway.server.access_key and gateway.server.secret from forager.yaml, then connect this machine as a node: run qory access-key enrol <server> <code>, or generate a key on the node's page in Qory Apiary and set the QORY_ variables it shows; see https://github.com/qoryai/qory/blob/main/docs/run.md#the-access-key-and-the-instance"},
		{"gateway: {server: {url: \"https://qory.example\", secret: qak_AQIDBAUGBwgJCgsMDQ4PEBESExQVFhcYGRobHB0eHyA}}\n", "gateway.server.secret is a workspace access key's secret, which servers no longer accept; remove gateway.server.access_key and gateway.server.secret from forager.yaml, then connect this machine as a node: run qory access-key enrol <server> <code>, or generate a key on the node's page in Qory Apiary and set the QORY_ variables it shows; see https://github.com/qoryai/qory/blob/main/docs/run.md#the-access-key-and-the-instance"},
		{"gateway: {server: {url: \"https://qory.example\", apiary_public_key: [{alg: ed25519}]}}\n", "gateway.server.apiary_public_key[0] needs both alg and public_key"},
		{"gateway: {server: {url: \"https://qory.example\", apiary_public_key: [{alg: ed25519, public_key: abc}]}}\n", "gateway.server.apiary_public_key: the pin: the public key \"abc\": 2 bytes where 32 belong"},
		{"gateway: {server: {url: \"https://qory.example\", apiary_public_key: [{alg: rsa, public_key: rcFAEfgtHFbZVqpPnXPYhYNhpgYEhSXg0Ixjjcdd2Mc}]}}\n", "gateway.server.apiary_public_key: the pin lists a key of alg \"rsa\""},
		{"gateway: {server: {url: \"https://qory.example\", apiary_public_key: [{alg: ed25519, public_key: rcFAEfgtHFbZVqpPnXPYhYNhpgYEhSXg0Ixjjcdd2Mc}]}}\n", "gateway.server.apiary_public_key: it lists the Forager contract's published fixture key, whose secret anyone can read"},
		{"gateway: {server: {url: \"https://qory.example\", apiary_public_key: [{alg: ed25519, public_key: rcFAEfgtHFbZVqpPnXPYhYNhpgYEhSXg0Ixjjcdd2Mc, kid: x}]}}\n", `key "kid" is not one`},
		{"gateway: {server: {url: \"https://qory.example\", apiary_public_key: []}}\n", "gateway.server.apiary_public_key: the pin lists no key"},
		{"gateway: {server: {url: \"https://qory.example\", events: [\"*\"]}}\n", `key "events" is not one`},
		{"session: {instance: {name: \"-build\"}}\n", `session.instance.name: the name "-build" is not 1 to 64 of A-Z, a-z, 0-9, dot, underscore and dash, starting with a letter or digit`},
		{"session: {instance: {name: build-01, id: i_x}}\n", `key "id" is not one`},
		{"webhook: {url: \"https://example.com/e\", secret: sixteen-characters-at-least}\n", "webhook: qory 0.10.0 replaced this section with server; see the docs of forager.yaml"},
		{"wall: {image: i}\n", "wall.adapter is required, and docker is the one there is"},
		{"wall: {adapter: bubblewrap}\n", "wall.adapter is required, and docker is the one there is"},
		{"wall: {adapter: docker, helper: qory-linux}\n", `wall.helper "qory-linux" is not an absolute path`},
		{"wall: {adapter: docker, env: [\"KEY=value\"]}\n", `wall.env: "KEY=value" is not a variable's name`},
		{"wall: {adapter: docker, network: host}\n", `key "network" is not one`},
		{"wall: {adapter: docker, mounts: [srv]}\n", `wall.mounts: the mount "srv" is not an absolute path`},
		{"wall: {adapter: docker, pids_limit: 0}\n", `wall.pids_limit is 0`},
		{"wall: {adapter: docker, env: [QORY_ACCESS_KEY_SECRET]}\n", `wall.env: QORY_ACCESS_KEY_SECRET is Forager's own`},
		{"wall: {adapter: docker, env: [QORY_ACCESS_KEY_ID]}\n", `wall.env: QORY_ACCESS_KEY_ID is Forager's own`},
		{"wall: {adapter: docker, env: [QORY_APIARY_PUBLIC_KEY]}\n", `wall.env: QORY_APIARY_PUBLIC_KEY is Forager's own`},
		{"wall: {adapter: docker, env: [QORY_SERVER_SECRET]}\n", `wall.env: QORY_SERVER_SECRET is Forager's own and never the agent's`},
		{"gateway: {credentials: {product: {adapter: [git-host]}}}\n", `gateway.credentials.product.adapter is a program by its absolute path`},
		{"gateway: {credentials: {product: {command: [/x]}}}\n", `gateway.credentials.product: key "command" is not one`},
		{"gateway: {credentials: [product]}\n", `gateway.credentials is a mapping`},
		{"session: {run: {timeout: soon}}\n", `session.run.timeout "soon" is not a duration above zero`},
		{"session: {run: {stop_grace: 0s}}\n", `session.run.stop_grace "0s" is not a duration above zero`},
		{"session: {run: {stop_signal: SIGKILL}}\n", `session.run.stop_signal: the stop signal "SIGKILL" is not one of`},
		{"apiVersion: qory.dev/v9\n", "qory.dev/v9"},
	} {
		path := foragerFile(t, c.body)
		_, err := config.Load(t.TempDir(), true)
		if err == nil || !strings.Contains(err.Error(), c.want) || !strings.HasPrefix(err.Error(), path) {
			t.Errorf("%q: error %v, want one naming the file and %q", c.body, err, c.want)
		}
	}
}

// TestTheForagerSchemaTakesTheServerSection holds forager.schema.json to what the reader
// takes of gateway.server and session.instance: url, access_key_id and the pin, each of the last two
// optional since the environment may hold it, and session.instance.name; the workspace access
// key's keys and a server with no url fail it.
func TestTheForagerSchemaTakesTheServerSection(t *testing.T) {
	schema, err := jsonschema.NewCompiler().Compile(filepath.Join("..", "..", "contracts", "harness", "v1", "forager.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	_, pin := serverKey(t)
	for _, c := range []struct {
		body  string
		valid bool
	}{
		{"gateway: {server: {url: \"https://qory.example\"}}\n", true},
		{"gateway: {server: {url: \"https://qory.example\", access_key_id: ak_0123456789abcdef, apiary_public_key: " + pin + "}}\n", true},
		{"session: {instance: {name: build-01}}\n", true},
		{"gateway: {server: {access_key_id: ak_0123456789abcdef}}\n", false},
		{"gateway: {server: {url: \"https://qory.example\", access_key_id: AK_0123456789ABCDEF}}\n", false},
		{"gateway: {server: {url: \"https://qory.example\", apiary_public_key: []}}\n", false},
		{"gateway: {server: {url: \"https://qory.example\", apiary_public_key: [{alg: rsa, public_key: abc}]}}\n", false},
		{"gateway: {server: {url: \"https://qory.example\", access_key: ak_0123456789abcdef}}\n", false},
		{"gateway: {server: {url: \"https://qory.example\", secret: sixteen-characters-at-least}}\n", false},
		{"session: {instance: {name: \"-build\"}}\n", false},
		{"session: {instance: {id: i_x}}\n", false},
		{"server: {url: \"https://qory.example\"}\n", false},
		{"instance: {name: build-01}\n", false},
		{"gateway: {instance: {name: build-01}}\n", false},
		{"session: {server: {url: \"https://qory.example\"}}\n", false},
	} {
		var doc any
		if err := yaml.Unmarshal([]byte("apiVersion: qory.dev/v1alpha1\n"+c.body), &doc); err != nil {
			t.Fatal(err)
		}
		if err := schema.Validate(doc); (err == nil) != c.valid {
			t.Errorf("%q: %v, want valid %v", c.body, err, c.valid)
		}
	}
}

// TestTheDocsExampleServerReads is gateway.server of the forager.yaml docs/run.md shows:
// it reads, its access key id is one, and its pin is a key the gateway verifies under
// that is no published fixture's.
func TestTheDocsExampleServerReads(t *testing.T) {
	hermetic(t)
	data, err := os.ReadFile(filepath.Join("..", "..", "docs", "run.md"))
	if err != nil {
		t.Fatal(err)
	}
	const start = "```yaml\n# ~/.config/qory/forager.yaml\n"
	doc := string(data)
	i := strings.Index(doc, start)
	if i < 0 {
		t.Fatal("docs/run.md shows no forager.yaml")
	}
	example, _, _ := strings.Cut(doc[i+len(start):], "```")
	// The lines of gateway.server: "  server:" under gateway, and every line below it
	// indented further.
	var section []string
	gateway := false
	for _, line := range strings.SplitAfter(example, "\n") {
		if len(section) > 0 && !strings.HasPrefix(line, "    ") {
			break
		}
		switch {
		case !strings.HasPrefix(line, " "):
			gateway = strings.HasPrefix(line, "gateway:")
		case gateway && (strings.HasPrefix(line, "  server:") || len(section) > 0):
			section = append(section, line)
		}
	}
	if len(section) == 0 {
		t.Fatal("the forager.yaml of docs/run.md has no gateway.server")
	}
	foragerFile(t, "apiVersion: qory.dev/v1alpha1\ngateway:\n"+strings.Join(section, ""))
	c, err := config.Load(t.TempDir(), true)
	if err != nil {
		t.Fatalf("the docs' gateway.server: %v\n%s", err, strings.Join(section, ""))
	}
	s := c.Forager.Server
	if s == nil || accesskey.CheckID(s.AccessKeyID) != nil || len(s.Pin) == 0 || s.Pin.Check() != nil || s.Pin.Fixture() {
		t.Errorf("the docs' gateway.server: %+v", s)
	}
}
