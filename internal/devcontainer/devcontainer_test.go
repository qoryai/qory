package devcontainer_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/devcontainer"
)

// write writes content to root/rel, making its folders.
func write(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestParseReadsTheMembers is a configuration with each member read, comments and
// trailing commas among them, and members ignored.
func TestParseReadsTheMembers(t *testing.T) {
	data := "\xef\xbb\xbf" + `// The development container of the shop.
{
	/* The image is built,
	   not pulled. */
	"name": "shop", // ignored
	"build": {
		"dockerfile": "Dockerfile",
		"context": "..",
		"args": {"VARIANT": "1.27", /* trailing */},
		"target": "dev",
		"cacheFrom": ["ignored"],
	},
	"image": "example.com/acme/shop:1",
	"workspaceFolder": "/workspaces/shop",
	"remoteUser": "dev",
	"containerUser": "root",
	"containerEnv": {"SHOP_MODE": "test"},
	"remoteEnv": {"EDITOR": "vi", "PAGER": null, "LESS": null},
	"features": {
		"ghcr.io/devcontainers/features/node:1": {"version": "lts"},
		"ghcr.io/devcontainers/features/go:1": {},
		"./local-feature": {},
	},
	"url": "https://example.com/a//b/*c*/",
	"onCreateCommand": "make setup // not a comment",
	"updateContentCommand": ["npm", "ci",],
	"postCreateCommand": {
		"server": "npm start",
		"empty": "",
		"db": ["make", "db"],
	},
	"postStartCommand": null,
	"initializeCommand": "echo host",
	"postAttachCommand": "",
	"init": true,
	"privileged": false,
	"capAdd": [],
	"mounts": ["source=a,target=b,type=bind"],
	"runArgs": ["--network=host"],
}
`
	c, err := devcontainer.Parse(".devcontainer/devcontainer.json", []byte(data))
	if err != nil {
		t.Fatal(err)
	}
	want := &devcontainer.Config{
		Path:          ".devcontainer/devcontainer.json",
		OnCreate:      devcontainer.Command{Member: "onCreateCommand", Steps: []devcontainer.Step{{Line: "make setup // not a comment"}}},
		UpdateContent: devcontainer.Command{Member: "updateContentCommand", Steps: []devcontainer.Step{{Args: []string{"npm", "ci"}}}},
		PostCreate: devcontainer.Command{Member: "postCreateCommand", Named: true, Steps: []devcontainer.Step{
			{Name: "server", Line: "npm start"},
			{Name: "db", Args: []string{"make", "db"}},
		}},
		PostStart:       devcontainer.Command{Member: "postStartCommand"},
		Initialize:      true,
		Image:           "example.com/acme/shop:1",
		Build:           &devcontainer.Build{Dockerfile: "Dockerfile", Context: "..", Args: map[string]string{"VARIANT": "1.27"}, Target: "dev"},
		WorkspaceFolder: "/workspaces/shop",
		RemoteUser:      "dev",
		ContainerUser:   "root",
		ContainerEnv:    map[string]string{"SHOP_MODE": "test"},
		RemoteEnv:       map[string]string{"EDITOR": "vi"},
		RemoteUnset:     []string{"LESS", "PAGER"},
		Features:        []string{"ghcr.io/devcontainers/features/node:1", "ghcr.io/devcontainers/features/go:1", "./local-feature"},
		HostAccess:      []string{"mounts", "runArgs"},
		Init:            true,
	}
	if !reflect.DeepEqual(devcontainer.Members(c), want) {
		t.Errorf("got  %+v\nwant %+v", c, want)
	}
	if c.PostStart.Empty() != true || c.PostCreate.Empty() {
		t.Error("Empty")
	}
	if got := len(c.Commands()); got != 4 {
		t.Errorf("Commands: %d", got)
	}
	err = c.CheckHostAccess()
	if err == nil || err.Error() != ".devcontainer/devcontainer.json sets mounts and runArgs, which give the container access to the host; no image is built from a configuration that sets them" {
		t.Errorf("CheckHostAccess: %v", err)
	}
}

// TestHostAccessNamesEachMemberSet is each member that gives access to the host, set and
// not set, and init, which is reported only.
func TestHostAccessNamesEachMemberSet(t *testing.T) {
	c, err := devcontainer.Parse(".devcontainer.json", []byte(`{
		"privileged": true, "capAdd": ["SYS_PTRACE"], "securityOpt": ["seccomp=unconfined"],
		"mounts": [{"source": "a", "target": "b", "type": "bind"}], "runArgs": ["--privileged"],
		"workspaceMount": "source=/,target=/w,type=bind", "init": false}`))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(c.HostAccess, devcontainer.HostAccessMembers) || c.Init {
		t.Errorf("all set: %v, init %v", c.HostAccess, c.Init)
	}
	want := ".devcontainer.json sets privileged, capAdd, securityOpt, mounts, runArgs and workspaceMount, which give the container access to the host; no image is built from a configuration that sets them"
	if err := c.CheckHostAccess(); err == nil || err.Error() != want {
		t.Errorf("all set: %v", err)
	}
	c, err = devcontainer.Parse(".devcontainer.json", []byte(`{
		"privileged": false, "capAdd": [], "securityOpt": null, "mounts": [],
		"runArgs": [], "workspaceMount": "", "init": true, "features": {}}`))
	if err != nil {
		t.Fatal(err)
	}
	if c.HostAccess != nil || !c.Init || c.CheckHostAccess() != nil || c.Features == nil || len(c.Features) != 0 {
		t.Errorf("none set: %+v", c)
	}
}

// TestParseRefuses is each refusal of Parse, with its words.
func TestParseRefuses(t *testing.T) {
	const p = ".devcontainer.json"
	for _, c := range []struct{ data, want string }{
		{"{\"image\": \"a\xffb\"}", p + " is not valid UTF-8"},
		{`{"image": "a"} /* open`, p + " has a /* comment that is never closed"},
		{"{\n  \"image\": \"a\"\n  \"x\": 1\n}", p + " is not JSON with comments: the error is at line 3, column 3"},
		{`{"image": "a"`, p + " is not JSON with comments: the error is at line 1, column 14"},
		{`{"image": "a"} {}`, p + " is not JSON with comments: the error is at line 1, column 16"},
		{`[1,,]`, p + " is not JSON with comments: the error is at line 1, column 4"},
		{`[,]`, p + " is not JSON with comments: the error is at line 1, column 2"},
		{`{,}`, p + " is not JSON with comments: the error is at line 1, column 2"},
		{`["a"]`, p + " does not hold one JSON object"},
		{`{"image": "a", "image": "b"}`, p + `: the object at / names "image" twice`},
		{`{"postCreateCommand": {"a": "x", "a": "y"}}`, p + `: the object at /postCreateCommand names "a" twice`},
		{`{"remoteEnv": {"A": "\ud800"}}`, p + `: the string at /remoteEnv/A holds a \u escape of a lone surrogate`},
		{`{"x": ["\udc00"]}`, p + `: the string at /x/0 holds a \u escape of a lone surrogate`},
		{`{"\ud800x": 1}`, p + `: the member name at /` + "�x" + ` holds a \u escape of a lone surrogate`},
		{`{"image": 1}`, p + ": image is not a string"},
		{`{"build": "Dockerfile"}`, p + ": build is not an object"},
		{`{"build": {"args": {"A": 1}}}`, p + ": build.args is not an object of strings"},
		{`{"containerEnv": {"A": null}}`, p + ": containerEnv is not an object of strings"},
		{`{"remoteEnv": {"A": 1}}`, p + ": remoteEnv is not an object of strings or null"},
		{`{"features": ["a"]}`, p + ": features is not an object"},
		{`{"onCreateCommand": 1}`, p + ": onCreateCommand is not a string, an array of strings, or an object of them"},
		{`{"onCreateCommand": ["a", 1]}`, p + ": onCreateCommand is not a string, an array of strings, or an object of them"},
		{`{"onCreateCommand": ["a", null]}`, p + ": onCreateCommand is not a string, an array of strings, or an object of them"},
		{`{"postStartCommand": {"a": {"b": "c"}}}`, p + ": postStartCommand.a is not a string or an array of strings"},
	} {
		_, err := devcontainer.Parse(p, []byte(c.data))
		if err == nil || err.Error() != c.want {
			t.Errorf("%q: %v\nwant %s", c.data, err, c.want)
		}
	}
	if _, err := devcontainer.Parse(p, []byte(`{"image":"`+strings.Repeat("a", devcontainer.MaxSize)+`"}`)); err == nil || err.Error() != p+" is more than 1 MiB; a development container configuration is a few kilobytes" {
		t.Errorf("too big: %v", err)
	}
}

// TestParseKeepsWhatLooksLikeACommentInAString is comment and comma characters inside
// strings, escaped quotes among them, kept as written.
func TestParseKeepsWhatLooksLikeACommentInAString(t *testing.T) {
	c, err := devcontainer.Parse(".devcontainer.json", []byte(`{
		"onCreateCommand": "echo \"/* x */\" // y, ]",
		"updateContentCommand": ["a\\", "// b", "c,}"], // done
		"postCreateCommand": "😀 ok",
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := c.OnCreate.Steps[0].Line; got != `echo "/* x */" // y, ]` {
		t.Errorf("onCreateCommand: %q", got)
	}
	if got := c.UpdateContent.Steps[0].Args; !reflect.DeepEqual(got, []string{`a\`, "// b", "c,}"}) {
		t.Errorf("updateContentCommand: %q", got)
	}
	if got := c.PostCreate.Steps[0].Line; got != "\U0001F600 ok" {
		t.Errorf("postCreateCommand: %q", got)
	}
}

// TestParseEndsALineCommentAtACarriageReturn is a // comment ended by a carriage
// return alone, as the Development Containers CLI's reader ends one: the members after
// it are read, so a member that gives access to the host and a command are not hidden.
func TestParseEndsALineCommentAtACarriageReturn(t *testing.T) {
	c, err := devcontainer.Parse(".devcontainer.json", []byte("{\"image\":\"x\", // note\r\"privileged\": true, \"postCreateCommand\": \"evil\" // c\n}"))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(c.HostAccess, []string{"privileged"}) {
		t.Errorf("the members that give access to the host are %v, want privileged", c.HostAccess)
	}
	if c.PostCreate.Empty() || c.PostCreate.Steps[0].Line != "evil" {
		t.Errorf("postCreateCommand: %+v", c.PostCreate)
	}
	if c.Image != "x" {
		t.Errorf("image: %q", c.Image)
	}
}

// TestExpandReplacesTheVariables is each variable a command may use, in each form.
func TestExpandReplacesTheVariables(t *testing.T) {
	c, err := devcontainer.Parse(".devcontainer.json", []byte(`{
		"onCreateCommand": "cd ${containerWorkspaceFolder} && echo ${containerWorkspaceFolderBasename} ${HOME} ${containerEnv:MODE} ${containerEnv:UNSET:fallback:x} ${containerEnv:UNSET} ${unknown:a} $${x} ${open",
		"postCreateCommand": {"a": ["${containerEnv:MODE}", "${containerWorkspaceFolder}/b"]}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	v := devcontainer.Vars{WorkspaceFolder: "/workspaces/shop", WorkspaceFolderBasename: "shop", Env: []string{"MODE=first", "OTHER=x", "MODE=last"}}
	on, err := c.OnCreate.Expand(v)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := on.Steps[0].Line, "cd /workspaces/shop && echo shop ${HOME} last fallback:x  ${unknown:a} $${x} ${open"; got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
	post, err := c.PostCreate.Expand(v)
	if err != nil {
		t.Fatal(err)
	}
	if got := post.Steps[0].Args; !reflect.DeepEqual(got, []string{"last", "/workspaces/shop/b"}) || post.Steps[0].Name != "a" {
		t.Errorf("named: %+v", post.Steps[0])
	}
	if c.PostCreate.Steps[0].Args[0] != "${containerEnv:MODE}" {
		t.Error("Expand changed the command it was called on")
	}
}

// TestExpandRefusesTheHostsVariables is each variable of the host and ${devcontainerId}
// refused, naming the command and never its text or a value.
func TestExpandRefusesTheHostsVariables(t *testing.T) {
	for _, c := range []struct{ command, want string }{
		{`"echo secret-text ${localEnv:SECRET_NAME}"`, "postStartCommand uses ${localEnv:…}, a value of the host; no value of the host reaches the container"},
		{`["echo", "secret-text", "${env:SECRET_NAME}"]`, "postStartCommand uses ${env:…}, a value of the host; no value of the host reaches the container"},
		{`{"web": "secret-text ${localWorkspaceFolder}"}`, "postStartCommand's command web uses ${localWorkspaceFolder}, a value of the host; no value of the host reaches the container"},
		{`"secret-text ${localWorkspaceFolderBasename}"`, "postStartCommand uses ${localWorkspaceFolderBasename}, a value of the host; no value of the host reaches the container"},
		{`"secret-text ${devcontainerId}"`, "postStartCommand uses ${devcontainerId}, which is given no value here"},
		{`"secret-text ${containerEnv}"`, "postStartCommand uses ${containerEnv} without the name of a variable"},
		{`"secret-text ${containerEnv::d}"`, "postStartCommand uses ${containerEnv} without the name of a variable"},
	} {
		cfg, err := devcontainer.Parse(".devcontainer.json", []byte(`{"postStartCommand": `+c.command+`}`))
		if err != nil {
			t.Fatal(err)
		}
		_, err = cfg.PostStart.Expand(devcontainer.Vars{Env: []string{"SECRET_NAME=secret-value"}})
		if err == nil || err.Error() != c.want {
			t.Errorf("%s: %v\nwant %s", c.command, err, c.want)
		}
		if err != nil && (strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "SECRET")) {
			t.Errorf("%s: the error quotes the command: %v", c.command, err)
		}
	}
}
