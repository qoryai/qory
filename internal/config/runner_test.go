package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/runner/accesskey"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"

	"github.com/qoryai/qory/internal/config"
)

// serverKey is a server's signing key of the test's own, and the pin of it as the
// runner file writes one.
func serverKey(t *testing.T) (*accesskey.Key, string) {
	t.Helper()
	k, err := accesskey.Generate()
	if err != nil {
		t.Fatal(err)
	}
	return k, "[{alg: ed25519, public_key: " + k.PublicKey().String() + "}]"
}

// runnerFile writes the machine's runner file under the configuration directory.
func runnerFile(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", config.RunnerFileName)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestRunnerFileReadsBothSections reads egress, server and instance, lists them with
// the file as origin and the pin by its fingerprint, and carries them on the
// configuration a checkout loads.
func TestRunnerFileReadsBothSections(t *testing.T) {
	hermetic(t)
	signer, pin := serverKey(t)
	path := runnerFile(t, "apiVersion: qory.dev/v1alpha1\negress:\n  mode: enforce\n  allow: [api.anthropic.com, \"*.github.com\"]\n  deny: [gist.github.com, \"*.ads.example\"]\nserver:\n  url: https://qory.example\n  access_key_id: ak_f1xt0re000000000\n  apiary_public_key: "+pin+"\ninstance:\n  name: build-01\n")
	c, err := config.Load(t.TempDir(), true)
	if err != nil {
		t.Fatal(err)
	}
	r := c.Runner
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
	for key, want := range map[string]string{"runner.egress.mode": "enforce", "runner.egress.allow": "api.anthropic.com, *.github.com", "runner.egress.deny": "gist.github.com, *.ads.example", "runner.server.url": "https://qory.example", "runner.server.access_key_id": "ak_f1xt0re000000000",
		"runner.server.apiary_public_key": "fingerprint " + signer.Fingerprint(), "runner.instance.name": "build-01"} {
		if rows[key].Value != want || rows[key].Origin != path {
			t.Errorf("%s: %+v, want %q from %s", key, rows[key], want, path)
		}
	}
}

// TestRunnerFileReadsTheWall reads the wall section, lists it, and lists no wall as the
// default when the file names none.
func TestRunnerFileReadsTheWall(t *testing.T) {
	hermetic(t)
	path := runnerFile(t, "wall:\n  adapter: docker\n  image: example.com/agent:1\n  command: podman\n  helper: /opt/qory/qory-linux\n  env: [ANTHROPIC_API_KEY, GH_TOKEN]\n  user: \"1000:1000\"\n  mounts: [/srv/data:ro, /srv/cache]\n  cpus: \"3.5\"\n  memory: 14g\n  pids_limit: 4096\n  shm_size: 2g\n  ca_env: [SSL_CERT_FILE, MY_TOOLS_CA]\nrun:\n  timeout: 5h30m\n  stop_signal: SIGINT\n  stop_grace: 30s\ncredentials:\n  product:\n    adapter: [/opt/adapters/git-host, --repo, \"${argument}\"]\n    argument: \"[a-z0-9-]+/[a-z0-9-]+\"\n    hosts: [\"*.example.com\"]\n    placeholders: [GIT_HOST_TOKEN]\n  model:\n    env: MODEL_TOKEN\n    hosts: [api.model.example]\n    auth: {scheme: header, header: X-Api-Key}\n")
	c, err := config.Load(t.TempDir(), true)
	if err != nil {
		t.Fatal(err)
	}
	w := c.Runner.Wall
	if w == nil || w.Adapter != config.WallDocker || w.Image != "example.com/agent:1" || w.Command != "podman" || w.Helper != "/opt/qory/qory-linux" || w.User != "1000:1000" || strings.Join(w.Env, " ") != "ANTHROPIC_API_KEY GH_TOKEN" {
		t.Fatalf("wall read as %+v", w)
	}
	rows := map[string]config.Row{}
	for _, row := range c.Rows() {
		rows[row.Key] = row
	}
	for key, want := range map[string]string{"runner.wall.adapter": "docker", "runner.wall.image": "example.com/agent:1 (a reference)", "runner.wall.env": "ANTHROPIC_API_KEY, GH_TOKEN", "runner.wall.command": "podman", "runner.wall.helper": "/opt/qory/qory-linux",
		"runner.wall.mounts": "/srv/data:ro, /srv/cache", "runner.wall.cpus": "3.5", "runner.wall.memory": "14g", "runner.wall.pids_limit": "4096", "runner.wall.shm_size": "2g",
		"runner.run.timeout": "5h30m0s", "runner.run.stop_grace": "30s", "runner.run.stop_signal": "SIGINT", "runner.wall.ca_env": "SSL_CERT_FILE, MY_TOOLS_CA",
		"runner.credentials.product": "adapter /opt/adapters/git-host", "runner.credentials.model": "env MODEL_TOKEN"} {
		if rows[key].Value != want || rows[key].Origin != path {
			t.Errorf("%s: %+v, want %q from %s", key, rows[key], want, path)
		}
	}
	runnerFile(t, "egress: {mode: observe}\n")
	c, err = config.Load(t.TempDir(), true)
	if err != nil || c.Runner.Wall != nil {
		t.Fatalf("no wall section: %+v, %v", c.Runner, err)
	}
	for _, row := range c.Rows() {
		if row.Key == "runner.wall.adapter" && (row.Value != "(none)" || row.Origin != config.Default) {
			t.Errorf("no wall listed as %+v", row)
		}
	}
}

// TestRunnerFileDefaults is no file, an empty file and a file with one section: what is
// absent is observe everything and files only, listed as defaults, and the secret may
// come from the environment.
func TestRunnerFileDefaults(t *testing.T) {
	hermetic(t)
	c, err := config.Load(t.TempDir(), true)
	if err != nil || c.Runner != nil {
		t.Fatalf("no file: %+v, %v", c.Runner, err)
	}
	rows := map[string]config.Row{}
	for _, row := range c.Rows() {
		rows[row.Key] = row
	}
	if rows["runner.egress.mode"].Value != "observe" || rows["runner.egress.mode"].Origin != config.Default || rows["runner.egress.deny"].Value != "(none)" || rows["runner.server.url"].Value != "(none)" {
		t.Errorf("default rows %+v", rows)
	}
	runnerFile(t, "apiVersion: qory.dev/v1alpha1\n")
	if c, err = config.Load(t.TempDir(), true); err != nil || c.Runner == nil || c.Runner.Egress != nil || c.Runner.Server != nil {
		t.Errorf("empty file: %+v, %v", c.Runner, err)
	}
	signer, _ := serverKey(t)
	serverVariables(t, config.ServerVariables{AccessKeyID: "ak_0123456789abcdef", ApiaryPublicKey: `[{"alg":"ed25519","public_key":"` + signer.PublicKey().String() + `"}]`})
	runnerFile(t, "server:\n  url: http://127.0.0.1:8787\n")
	c, err = config.Load(t.TempDir(), true)
	if err != nil || c.Runner.Server == nil || c.Runner.Server.AccessKeyID != "ak_0123456789abcdef" || c.Runner.Server.AccessKeyIDFrom != "$QORY_ACCESS_KEY_ID" || len(c.Runner.Server.Pin) != 1 || c.Runner.Server.PinFrom != "$QORY_APIARY_PUBLIC_KEY" || c.Runner.Egress != nil {
		t.Fatalf("the id and the pin from the environment: %+v, %v", c.Runner, err)
	}
	rows = map[string]config.Row{}
	for _, row := range c.Rows() {
		rows[row.Key] = row
	}
	if rows["runner.server.access_key_id"].Origin != "$QORY_ACCESS_KEY_ID" || rows["runner.server.apiary_public_key"].Value != "fingerprint "+signer.Fingerprint() || rows["runner.instance.name"].Origin != config.Default {
		t.Errorf("rows from the environment: %+v", rows)
	}

	// Neither is required in the file, since enrolment writes them; both are required
	// of a run.
	serverVariables(t, config.ServerVariables{})
	if c, err = config.Load(t.TempDir(), true); err != nil || c.Runner.Server.AccessKeyID != "" || c.Runner.Server.Pin != nil {
		t.Fatalf("a server section with a url alone: %+v, %v", c.Runner.Server, err)
	}
}

// TestRunnerFileRefusesTheIDAndThePinTwice is a runner file that sets the access key's
// id or the pin while the environment sets it too, and values in the environment that
// are not one: each is refused, the variable named and no secret quoted.
func TestRunnerFileRefusesTheIDAndThePinTwice(t *testing.T) {
	hermetic(t)
	signer, pin := serverKey(t)
	envPin := `[{"alg":"ed25519","public_key":"` + signer.PublicKey().String() + `"}]`
	path := runnerFile(t, "server:\n  url: https://qory.example\n  access_key_id: ak_f1xt0re000000000\n  apiary_public_key: "+pin+"\n")
	serverVariables(t, config.ServerVariables{AccessKeyID: "ak_0123456789abcdef"})
	if _, err := config.Load(t.TempDir(), true); err == nil || err.Error() != path+": server.access_key_id is set, and so is QORY_ACCESS_KEY_ID; set one of them" {
		t.Errorf("the id twice: %v", err)
	}
	serverVariables(t, config.ServerVariables{ApiaryPublicKey: envPin})
	if _, err := config.Load(t.TempDir(), true); err == nil || err.Error() != path+": server.apiary_public_key is set, and so is QORY_APIARY_PUBLIC_KEY; set one of them" {
		t.Errorf("the pin twice: %v", err)
	}
	runnerFile(t, "server:\n  url: https://qory.example\n")
	const secret = "qak_AQIDBAUGBwgJCgsMDQ4PEBESExQVFhcYGRobHB0eHyA"
	for _, c := range []struct{ id, pin, want string }{
		{"AK_F1XT0RE000000000", "", `QORY_ACCESS_KEY_ID: the access key id "AK_F1XT0RE000000000" is not ak_`},
		{secret, "", "QORY_ACCESS_KEY_ID: the access key id (a value that contains an access key secret)"},
		{"", `{"alg":"ed25519"}`, "QORY_APIARY_PUBLIC_KEY: the pin is not a list"},
		{"", `[{"alg":"ed25519","public_key":"` + secret + `"}]`, "QORY_APIARY_PUBLIC_KEY: the document contains an access key secret"},
		{"", `[{"alg":"ed25519","public_key":"rcFAEfgtHFbZVqpPnXPYhYNhpgYEhSXg0Ixjjcdd2Mc"}]`, "QORY_APIARY_PUBLIC_KEY: it lists the runner contract's published fixture key"},
	} {
		serverVariables(t, config.ServerVariables{AccessKeyID: c.id, ApiaryPublicKey: c.pin})
		_, err := config.Load(t.TempDir(), true)
		if err == nil || !strings.HasPrefix(err.Error(), c.want) || strings.Contains(err.Error(), "AQIDBAUG") {
			t.Errorf("%q %q: %v, want %q", c.id, c.pin, err, c.want)
		}
	}
}

// TestRunnerFileRefusesTheWorkspaceSecretVariable is QORY_SERVER_SECRET, which held a
// workspace access key's secret: with a server section it is refused, the variable named
// and its value never quoted, and the message says to connect the machine as a node,
// with qory access-key enrol or a key generated on the node's page in Qory Apiary.
func TestRunnerFileRefusesTheWorkspaceSecretVariable(t *testing.T) {
	hermetic(t)
	runnerFile(t, "server:\n  url: https://qory.example\n")
	serverVariables(t, config.ServerVariables{WorkspaceSecret: "sixteen-characters-at-least"})
	_, err := config.Load(t.TempDir(), true)
	want := "QORY_SERVER_SECRET holds a workspace access key's secret, which servers no longer accept; unset it, and connect this machine as a node: run qory access-key enrol <server> <code>, or generate a key on the node's page in Qory Apiary and set the QORY_ variables it shows; see https://github.com/qoryai/qory/blob/main/docs/run.md#the-access-key-and-the-instance"
	if err == nil || err.Error() != want {
		t.Errorf("QORY_SERVER_SECRET: %v, want %q", err, want)
	}
}

// TestRunnerFileRefusesAMistake is every refusal, each naming the file and the key.
func TestRunnerFileRefusesAMistake(t *testing.T) {
	hermetic(t)
	for _, c := range []struct{ body, want string }{
		{"egres: {mode: observe}\n", `key "egres" is not one runner.yaml reads`},
		{"egress: {allow: [a.example]}\n", "egress.mode is required"},
		{"egress: {mode: log}\n", `egress.mode "log" is not observe or enforce`},
		{"egress: {mode: enforce, allow: [\"api.example.com:443\"]}\n", `egress.allow: "api.example.com:443" is not a lower-case host name or a *. suffix`},
		{"egress: {mode: observe, deny: [\"tracker.example:443\"]}\n", `egress.deny: "tracker.example:443" is not a lower-case host name or a *. suffix`},
		{"server: {access_key_id: ak_f1xt0re000000000}\n", "server.url is required"},
		{"server: {url: \"ftp://x\"}\n", `server.url "ftp://x" is not an https URL, or an http URL to this machine`},
		{"server: {url: \"http://qory.example\"}\n", `server.url "http://qory.example" is http to a host that is not this machine`},
		{"server: {url: \"https://qory.example/v1/events\"}\n", `server.url "https://qory.example/v1/events" is more than a scheme and a host`},
		{"server: {url: \"https://qory.example/\"}\n", `is more than a scheme and a host`},
		{"server: {url: \"https://qory.example/?qak_AQIDBAUGBwgJCgsMDQ4PEBESExQVFhcYGRobHB0eHyA\"}\n", "server.url: the document contains an access key secret"},
		{"server: {url: \"https://qory.example\", access_key_id: AK_F1XT0RE000000000}\n", `server.access_key_id: the access key id "AK_F1XT0RE000000000" is not ak_ and 16 lower-case Crockford base32 characters`},
		{"server: {url: \"https://qory.example\", access_key_id: ak_f1xt0re0000000}\n", `server.access_key_id: the access key id "ak_f1xt0re0000000" is not ak_`},
		{"server: {url: \"https://qory.example\", access_key: ak_f1xt0re000000000}\n", "server.access_key is a workspace access key, which servers no longer accept; remove server.access_key and server.secret from runner.yaml, then connect this machine as a node: run qory access-key enrol <server> <code>, or generate a key on the node's page in Qory Apiary and set the QORY_ variables it shows; see https://github.com/qoryai/qory/blob/main/docs/run.md#the-access-key-and-the-instance"},
		{"server: {url: \"https://qory.example\", secret: qak_AQIDBAUGBwgJCgsMDQ4PEBESExQVFhcYGRobHB0eHyA}\n", "server.secret is a workspace access key's secret, which servers no longer accept; remove server.access_key and server.secret from runner.yaml, then connect this machine as a node: run qory access-key enrol <server> <code>, or generate a key on the node's page in Qory Apiary and set the QORY_ variables it shows; see https://github.com/qoryai/qory/blob/main/docs/run.md#the-access-key-and-the-instance"},
		{"server: {url: \"https://qory.example\", apiary_public_key: [{alg: ed25519}]}\n", "server.apiary_public_key[0] needs both alg and public_key"},
		{"server: {url: \"https://qory.example\", apiary_public_key: [{alg: ed25519, public_key: abc}]}\n", "server.apiary_public_key: the pin: the public key \"abc\": 2 bytes where 32 belong"},
		{"server: {url: \"https://qory.example\", apiary_public_key: [{alg: rsa, public_key: rcFAEfgtHFbZVqpPnXPYhYNhpgYEhSXg0Ixjjcdd2Mc}]}\n", "server.apiary_public_key: the pin lists a key of alg \"rsa\""},
		{"server: {url: \"https://qory.example\", apiary_public_key: [{alg: ed25519, public_key: rcFAEfgtHFbZVqpPnXPYhYNhpgYEhSXg0Ixjjcdd2Mc}]}\n", "server.apiary_public_key: it lists the runner contract's published fixture key, whose secret anyone can read"},
		{"server: {url: \"https://qory.example\", apiary_public_key: [{alg: ed25519, public_key: rcFAEfgtHFbZVqpPnXPYhYNhpgYEhSXg0Ixjjcdd2Mc, kid: x}]}\n", `key "kid" is not one`},
		{"server: {url: \"https://qory.example\", apiary_public_key: []}\n", "server.apiary_public_key: the pin lists no key"},
		{"server: {url: \"https://qory.example\", events: [\"*\"]}\n", `key "events" is not one`},
		{"instance: {name: \"-build\"}\n", `instance.name: the name "-build" is not 1 to 64 of A-Z, a-z, 0-9, dot, underscore and dash, starting with a letter or digit`},
		{"instance: {name: build-01, id: i_x}\n", `key "id" is not one`},
		{"webhook: {url: \"https://example.com/e\", secret: sixteen-characters-at-least}\n", "webhook: qory 0.10.0 replaced this section with server; see the runner file docs"},
		{"wall: {image: i}\n", "wall.adapter is required, and docker is the one there is"},
		{"wall: {adapter: bubblewrap}\n", "wall.adapter is required, and docker is the one there is"},
		{"wall: {adapter: docker, helper: qory-linux}\n", `wall.helper "qory-linux" is not an absolute path`},
		{"wall: {adapter: docker, env: [\"KEY=value\"]}\n", `wall.env: "KEY=value" is not a variable's name`},
		{"wall: {adapter: docker, network: host}\n", `key "network" is not one`},
		{"wall: {adapter: docker, mounts: [srv]}\n", `wall.mounts: the mount "srv" is not an absolute path`},
		{"wall: {adapter: docker, pids_limit: 0}\n", `wall.pids_limit is 0`},
		{"wall: {adapter: docker, env: [QORY_ACCESS_KEY_SECRET]}\n", `wall.env: QORY_ACCESS_KEY_SECRET is the runner's own`},
		{"wall: {adapter: docker, env: [QORY_ACCESS_KEY_ID]}\n", `wall.env: QORY_ACCESS_KEY_ID is the runner's own`},
		{"wall: {adapter: docker, env: [QORY_APIARY_PUBLIC_KEY]}\n", `wall.env: QORY_APIARY_PUBLIC_KEY is the runner's own`},
		{"wall: {adapter: docker, env: [QORY_SERVER_SECRET]}\n", `wall.env: QORY_SERVER_SECRET is the runner's own and never the session's`},
		{"credentials: {product: {adapter: [git-host]}}\n", `credentials.product.adapter is a program by its absolute path`},
		{"credentials: {product: {command: [/x]}}\n", `credentials.product: key "command" is not one`},
		{"credentials: [product]\n", `credentials is a mapping`},
		{"run: {timeout: soon}\n", `run.timeout "soon" is not a duration above zero`},
		{"run: {stop_grace: 0s}\n", `run.stop_grace "0s" is not a duration above zero`},
		{"run: {stop_signal: SIGKILL}\n", `run.stop_signal: the stop signal "SIGKILL" is not one of`},
		{"apiVersion: qory.dev/v9\n", "qory.dev/v9"},
	} {
		path := runnerFile(t, c.body)
		_, err := config.Load(t.TempDir(), true)
		if err == nil || !strings.Contains(err.Error(), c.want) || !strings.HasPrefix(err.Error(), path) {
			t.Errorf("%q: error %v, want one naming the file and %q", c.body, err, c.want)
		}
	}
}

// TestTheRunnerSchemaTakesTheServerSection holds runner.schema.json to what the reader
// takes of server and instance: url, access_key_id and the pin, each of the last two
// optional since the environment may hold it, and instance.name; the workspace access
// key's keys and a server with no url fail it.
func TestTheRunnerSchemaTakesTheServerSection(t *testing.T) {
	schema, err := jsonschema.NewCompiler().Compile(filepath.Join("..", "..", "contracts", "harness", "v1", "runner.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	_, pin := serverKey(t)
	for _, c := range []struct {
		body  string
		valid bool
	}{
		{"server: {url: \"https://qory.example\"}\n", true},
		{"server: {url: \"https://qory.example\", access_key_id: ak_0123456789abcdef, apiary_public_key: " + pin + "}\n", true},
		{"instance: {name: build-01}\n", true},
		{"server: {access_key_id: ak_0123456789abcdef}\n", false},
		{"server: {url: \"https://qory.example\", access_key_id: AK_0123456789ABCDEF}\n", false},
		{"server: {url: \"https://qory.example\", apiary_public_key: []}\n", false},
		{"server: {url: \"https://qory.example\", apiary_public_key: [{alg: rsa, public_key: abc}]}\n", false},
		{"server: {url: \"https://qory.example\", access_key: ak_0123456789abcdef}\n", false},
		{"server: {url: \"https://qory.example\", secret: sixteen-characters-at-least}\n", false},
		{"instance: {name: \"-build\"}\n", false},
		{"instance: {id: i_x}\n", false},
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

// TestTheDocsExampleServerReads is the server section of the runner file docs/run.md
// shows: it reads, its access key id is one, and its pin is a key the runner verifies
// under that is no published fixture's.
func TestTheDocsExampleServerReads(t *testing.T) {
	hermetic(t)
	data, err := os.ReadFile(filepath.Join("..", "..", "docs", "run.md"))
	if err != nil {
		t.Fatal(err)
	}
	const start = "```yaml\n# ~/.config/qory/runner.yaml\n"
	doc := string(data)
	i := strings.Index(doc, start)
	if i < 0 {
		t.Fatal("docs/run.md shows no runner.yaml")
	}
	example, _, _ := strings.Cut(doc[i+len(start):], "```")
	var section []string
	for _, line := range strings.SplitAfter(example, "\n") {
		if strings.HasPrefix(line, "server:") || len(section) > 0 && strings.HasPrefix(line, " ") {
			section = append(section, line)
		} else if len(section) > 0 {
			break
		}
	}
	if len(section) == 0 {
		t.Fatal("the runner.yaml of docs/run.md has no server section")
	}
	runnerFile(t, "apiVersion: qory.dev/v1alpha1\n"+strings.Join(section, ""))
	c, err := config.Load(t.TempDir(), true)
	if err != nil {
		t.Fatalf("the docs' server section: %v\n%s", err, strings.Join(section, ""))
	}
	s := c.Runner.Server
	if s == nil || accesskey.CheckID(s.AccessKeyID) != nil || len(s.Pin) == 0 || s.Pin.Check() != nil || s.Pin.Fixture() {
		t.Errorf("the docs' server section: %+v", s)
	}
}
