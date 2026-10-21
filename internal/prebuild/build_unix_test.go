//go:build unix

package prebuild_test

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/qoryai/forager/wall"

	"github.com/qoryai/qory/internal/prebuild"
)

// agentImage is an agent image by digest.
const agentImage = "example.com/acme/agent@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// testName is what the temporary container and images are named after.
const testName = "qory-prebuild-test"

// secret is a value of the test's environment that must reach no program.
const secret = "s3cr3t-value-of-the-job"

// fakes are the fake docker and devcontainer programs of a test, and what they log.
type fakes struct {
	dir string
}

// path is a file of the fakes' folder.
func (f fakes) path(name string) string { return filepath.Join(f.dir, name) }

// read is a file of the fakes' folder, "" when it is not there.
func (f fakes) read(name string) string {
	data, _ := os.ReadFile(f.path(name))
	return string(data)
}

// set writes a file of the fakes' folder, which changes how a fake answers.
func (f fakes) set(t *testing.T, name, content string) {
	t.Helper()
	if err := os.WriteFile(f.path(name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// calls are the command lines of one fake, one per line.
func (f fakes) calls(program string) []string {
	return strings.Split(strings.TrimSpace(f.read(program+".log")), "\n")
}

// called reports whether the fake ran a command line that starts with start.
func (f fakes) called(program, start string) bool {
	return slices.ContainsFunc(f.calls(program), func(l string) bool { return strings.HasPrefix(l, start) })
}

// writeFake writes an executable script.
func writeFake(t *testing.T, path, script string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
}

// newFakes puts fake docker and devcontainer programs first on the PATH, and a secret
// in the environment. Each logs its command lines and its environment. devcontainer
// reports version 0.89.0, or the file version; read-configuration prints the file
// merged as the merged configuration, {} without it; and build prints success. Each
// keeps the configuration its --config names, in the file read-configuration.config or
// build.config. docker runs an engine for linux/amd64, or the file platform; image inspect prints the file
// config as a configuration, and the file size, 1 MiB without it, as a size; cp keeps
// the tar stream; start runs the file start.sh, or prints the seed step's lines.
func newFakes(t *testing.T) fakes {
	t.Helper()
	f := fakes{dir: t.TempDir()}
	d := f.dir
	writeFake(t, f.path("devcontainer"), `echo "$*" >> `+d+`/devcontainer.log
env >> `+d+`/devcontainer.env
config=; prev=
for a; do [ "$prev" = --config ] && config=$a; prev=$a; done
if [ -n "$config" ]; then cat "$config" > `+d+`/$1.config; fi
case "$1" in
--version) if [ -f `+d+`/version ]; then cat `+d+`/version; else echo 0.89.0; fi ;;
read-configuration)
	echo "[read] some log line" >&2
	printf '{"configuration":{},"mergedConfiguration":%s}\n' "$(cat `+d+`/merged 2>/dev/null || echo '{}')" ;;
build)
	if [ -f `+d+`/build.sh ]; then . `+d+`/build.sh; fi
	echo '{"outcome":"success","imageName":["x"]}' ;;
*) exit 64 ;;
esac
`)
	writeFake(t, f.path("docker"), `echo "$*" >> `+d+`/docker.log
env >> `+d+`/docker.env
echo "$DOCKER_CONFIG" > `+d+`/docker-config-path
cat "$DOCKER_CONFIG/config.json" > `+d+`/docker-config.json
for l in contexts cli-plugins; do [ -L "$DOCKER_CONFIG/$l" ] && [ -d "$DOCKER_CONFIG/$l/" ] && echo $l; done > `+d+`/docker-config-links
for last; do :; done
case "$1" in
version) if [ -f `+d+`/platform ]; then cat `+d+`/platform; else echo linux/amd64; fi ;;
build) cat "$5" >> `+d+`/dockerfiles ;;
image)
	case "$2" in
	inspect)
		case "$*" in
		*.Size*) if [ -f `+d+`/size ]; then cat `+d+`/size; else echo 1048576; fi ;;
		*) ref=$(echo "$last" | tr ':/' '__')
			if [ -f `+d+`/config-$ref ]; then cat `+d+`/config-$ref; elif [ -f `+d+`/config ]; then cat `+d+`/config; else echo '{"Entrypoint":null,"Cmd":["/bin/sh"],"Env":["PATH=/usr/bin"]}'; fi ;;
		esac ;;
	rm) ;;
	*) exit 64 ;;
	esac ;;
create) echo c0ffee ;;
cp) cat > `+d+`/tree.tar ;;
start)
	if [ -f `+d+`/start.sh ]; then . `+d+`/start.sh; fi
	echo "qory run prebuild: ran onCreateCommand in 1.0s"
	echo "qory run prebuild: moved /workspace to /qory/seed in 0.1s" ;;
commit|rm) ;;
*) exit 64 ;;
esac
`)
	t.Setenv("PATH", d+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("DOCKER_CONFIG", t.TempDir())
	t.Setenv("QORY_TEST_TOKEN", secret)
	t.Setenv("GITHUB_TOKEN", secret)
	return f
}

// staticELF writes the header of a static Linux executable for x86-64, as
// image.HelperArch reads one.
func staticELF(t *testing.T) string {
	t.Helper()
	h := make([]byte, 64)
	copy(h, []byte{0x7f, 'E', 'L', 'F', 2, 1, 1})
	h[16], h[18], h[20] = 2, 0x3e, 1
	h[52], h[54], h[58] = 64, 56, 64
	path := filepath.Join(t.TempDir(), "qory-linux")
	if err := os.WriteFile(path, h, 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// repository writes a repository whose configuration is config, with a file, a
// folder, a link and a .git folder.
func repository(t *testing.T, config string) string {
	t.Helper()
	root := t.TempDir()
	for name, content := range map[string]string{
		".devcontainer/devcontainer.json": config,
		"README.md":                       "the shop\n",
		"bin/run.sh":                      "#!/bin/sh\n",
		".git/HEAD":                       "ref: refs/heads/main\n",
	} {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(filepath.Join(root, "bin", "run.sh"), 0o4755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("README.md", filepath.Join(root, "LINK")); err != nil {
		t.Fatal(err)
	}
	return root
}

// shopConfig is a configuration built from an image, with commands.
const shopConfig = `{
	// The shop.
	"image": "example.com/acme/shop-dev:1",
	"containerEnv": {"SHOP_MODE": "test"},
	"onCreateCommand": "make setup",
}`

// options are the options of a build of repo, its lines and the programs' output
// going to out and log.
func options(t *testing.T, repo string, out, log io.Writer) prebuild.Options {
	return prebuild.Options{
		Repository: repo,
		AgentImage: agentImage,
		User:       "1234:2345",
		Tag:        "acme/shop:prebuild",
		Helper:     staticELF(t),
		SeedArgs:   []string{"run", "seed"},
		Env:        os.Environ(),
		Out:        out,
		Log:        log,
		Name:       testName,
	}
}

// TestBuildBuildsTheImage is a build that succeeds: the lines in order, the programs
// called in order with their arguments, and the clean-up of the temporary container
// and images, not of the image made.
func TestBuildBuildsTheImage(t *testing.T) {
	f := newFakes(t)
	var out, log bytes.Buffer
	o := options(t, repository(t, shopConfig), &out, &log)
	if err := prebuild.Build(t.Context(), o); err != nil {
		t.Fatalf("Build: %v\n%s\n%s", err, out.String(), log.String())
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	wantStarts := []string{
		"qory run prebuild: read .devcontainer/devcontainer.json in ",
		"qory run prebuild: read the configuration merged with its features and base image in ",
		"qory run prebuild: built the development container image in ",
		"qory run prebuild: added the agent image's /qory/agent in ",
		"qory run prebuild: copied the repository to /workspace in ",
		"qory run prebuild: ran onCreateCommand in 1.0s",
		"qory run prebuild: moved /workspace to /qory/seed in 0.1s",
		"qory run prebuild: committed the image in ",
		"qory run prebuild: built acme/shop:prebuild in ",
	}
	if len(lines) != len(wantStarts) {
		t.Fatalf("printed %d lines, want %d:\n%s", len(lines), len(wantStarts), out.String())
	}
	for i, w := range wantStarts {
		if !strings.HasPrefix(lines[i], w) {
			t.Errorf("line %d is %q, want it to start with %q", i+1, lines[i], w)
		}
	}
	if !strings.HasSuffix(lines[len(lines)-1], ": 1.0 MiB (1048576 bytes)") {
		t.Errorf("the last line gives no size: %q", lines[len(lines)-1])
	}

	repo := o.Repository
	cfg := filepath.Join(repo, ".devcontainer", "devcontainer.json")
	wantDev := []string{
		"--version",
		"read-configuration --workspace-folder " + repo + " --config " + cfg + " --include-merged-configuration --log-format text",
		"build --workspace-folder " + repo + " --config " + cfg + " --image-name " + testName + ":base --no-lockfile --log-format text",
	}
	if got := f.calls("devcontainer"); !slices.Equal(got, wantDev) {
		t.Errorf("devcontainer ran\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(wantDev, "\n"))
	}
	create := "create --name " + testName + " --pull never --user 1234:2345 --workdir /workspace --cap-drop ALL --security-opt no-new-privileges --mount type=bind,source=" + o.Helper + ",target=" + wall.HelperPath + ",readonly --entrypoint /qory/qory " + testName + ":top run seed"
	wantDocker := []string{
		"version --format {{.Server.Os}}/{{.Server.Arch}}",
		"image inspect --format {{json .Config}} " + testName + ":base",
		"build --tag " + testName + ":top",
		"image inspect --format {{json .Config}} " + testName + ":top",
		create,
		"cp --archive - " + testName + ":/",
		"start --attach " + testName,
		"commit --change USER 1234:2345 --change WORKDIR /workspace " + testName + " " + testName + ":seeded",
		"build --tag acme/shop:prebuild",
		"image inspect --format {{json .Config}} acme/shop:prebuild",
		"image inspect --format {{.Size}} acme/shop:prebuild",
		"rm --force --volumes " + testName,
		"image rm --force " + testName + ":base " + testName + ":top " + testName + ":seeded",
	}
	got := f.calls("docker")
	if len(got) != len(wantDocker) {
		t.Fatalf("docker ran\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(wantDocker, "\n"))
	}
	for i, w := range wantDocker {
		if !strings.HasPrefix(got[i], w) {
			t.Errorf("docker call %d is %q, want it to start with %q", i+1, got[i], w)
		}
	}
	for _, l := range got {
		for _, bad := range []string{"--privileged", "docker.sock", "--network", "--cap-add", "--init", "push", "login", "-e ", "--env"} {
			if strings.Contains(l, bad) {
				t.Errorf("docker %s holds %q", l, bad)
			}
		}
	}
	wantFiles := "FROM " + testName + ":base\nCOPY --from=" + agentImage + " /qory/agent /qory/agent\nUSER 1234:2345\nENV HOME=/home/agent\n" +
		"FROM " + testName + ":seeded\nENTRYPOINT []\nCMD [\"/bin/sh\"]\n"
	if got := f.read("dockerfiles"); got != wantFiles {
		t.Errorf("the Dockerfiles are\n%s\nwant\n%s", got, wantFiles)
	}
	if strings.Contains(log.String(), "[read] some log line") == false {
		t.Errorf("the devcontainer command's log is not passed on: %q", log.String())
	}
	if _, err := os.Stat(filepath.Join(repo, ".devcontainer", "devcontainer-lock.json")); err == nil {
		t.Error("the build wrote a lock file into the repository")
	}
}

// TestBuildCopiesTheRepositoryAsTheWallUser is the tar stream docker cp extracts at the
// container's root: /workspace and /qory/seed, then the repository with its .git and
// its link, every entry the wall user's, and no setuid bit.
func TestBuildCopiesTheRepositoryAsTheWallUser(t *testing.T) {
	f := newFakes(t)
	var out bytes.Buffer
	o := options(t, repository(t, shopConfig), &out, io.Discard)
	if err := prebuild.Build(t.Context(), o); err != nil {
		t.Fatalf("Build: %v\n%s", err, out.String())
	}
	r := tar.NewReader(strings.NewReader(f.read("tree.tar")))
	got := map[string]*tar.Header{}
	var names []string
	for {
		h, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		got[h.Name] = h
		names = append(names, h.Name)
	}
	if len(names) < 3 || names[0] != "workspace/" || names[1] != "qory/seed/" || names[2] != "home/agent/" {
		t.Errorf("the stream starts with %v", names)
	}
	if h := got["home/agent/"]; h != nil && h.Mode != 0o700 {
		t.Errorf("home/agent/ has mode %o", h.Mode)
	}
	for _, name := range []string{"workspace/README.md", "workspace/.git/HEAD", "workspace/.devcontainer/devcontainer.json", "workspace/bin/run.sh", "workspace/LINK"} {
		if got[name] == nil {
			t.Errorf("the stream lacks %s: %v", name, names)
		}
	}
	for name, h := range got {
		if h.Uid != 1234 || h.Gid != 2345 {
			t.Errorf("%s belongs to %d:%d", name, h.Uid, h.Gid)
		}
		if h.Mode&^0o777 != 0 {
			t.Errorf("%s has mode %o", name, h.Mode)
		}
	}
	if h := got["workspace/LINK"]; h != nil && (h.Typeflag != tar.TypeSymlink || h.Linkname != "README.md") {
		t.Errorf("the link is %v to %s", h.Typeflag, h.Linkname)
	}
}

// TestBuildPassesNoSecret is a build whose environment holds a token and a secret: no
// program it runs sees either, and each sees the PATH.
func TestBuildPassesNoSecret(t *testing.T) {
	f := newFakes(t)
	t.Setenv("DOCKER_HOST", "unix:///run/example.sock")
	o := options(t, repository(t, shopConfig), io.Discard, io.Discard)
	if err := prebuild.Build(t.Context(), o); err != nil {
		t.Fatalf("Build: %v", err)
	}
	for _, program := range []string{"docker", "devcontainer"} {
		env := f.read(program + ".env")
		if env == "" {
			t.Fatalf("%s logged no environment", program)
		}
		if strings.Contains(env, secret) || strings.Contains(env, "GITHUB_TOKEN") || strings.Contains(env, "QORY_TEST_TOKEN") {
			t.Errorf("%s saw a secret:\n%s", program, env)
		}
		for _, line := range strings.Split(strings.TrimSpace(env), "\n") {
			name, _, _ := strings.Cut(line, "=")
			if !slices.Contains(prebuild.AllowedEnv, name) && name != "DOCKER_BUILDKIT" && name != "PWD" && name != "SHLVL" && name != "_" && name != "OLDPWD" {
				t.Errorf("%s saw %s, which is not allowed", program, name)
			}
		}
		if !strings.Contains(env, "DOCKER_HOST=unix:///run/example.sock") || !strings.Contains(env, "PATH=") {
			t.Errorf("%s saw neither DOCKER_HOST nor PATH:\n%s", program, env)
		}
	}
}

// TestBuildRefusesBeforeBuilding is each refusal: a refusal, and nothing built, created
// or run.
func TestBuildRefusesBeforeBuilding(t *testing.T) {
	cases := []struct {
		name   string
		config string
		setup  func(t *testing.T, f fakes, o *prebuild.Options)
		want   string
	}{
		{name: "agent image by tag", setup: func(_ *testing.T, _ fakes, o *prebuild.Options) { o.AgentImage = "example.com/acme/agent:1" },
			want: "--agent-image names the agent image by digest alone"},
		{name: "short digest", setup: func(_ *testing.T, _ fakes, o *prebuild.Options) { o.AgentImage = "example.com/acme/agent@sha256:0123" },
			want: "--agent-image names the agent image by digest alone"},
		{name: "root", setup: func(_ *testing.T, _ fakes, o *prebuild.Options) { o.User = "0:1000" }, want: "--user is uid:gid"},
		{name: "root written long", setup: func(_ *testing.T, _ fakes, o *prebuild.Options) { o.User = "00:0" }, want: "--user is uid:gid"},
		{name: "root's group", setup: func(_ *testing.T, _ fakes, o *prebuild.Options) { o.User = "1000:0" },
			want: "--user is uid:gid, two numbers, neither of them 0"},
		{name: "root's group written long", setup: func(_ *testing.T, _ fakes, o *prebuild.Options) { o.User = "1000:000" }, want: "--user is uid:gid"},
		{name: "a name", setup: func(_ *testing.T, _ fakes, o *prebuild.Options) { o.User = "agent:agent" }, want: "--user is uid:gid"},
		{name: "a uid alone", setup: func(_ *testing.T, _ fakes, o *prebuild.Options) { o.User = "1000" }, want: "--user is uid:gid"},
		{name: "tag with digest", setup: func(_ *testing.T, _ fakes, o *prebuild.Options) { o.Tag = agentImage }, want: "--tag is the name of the image to make"},
		{name: "tag as flag", setup: func(_ *testing.T, _ fakes, o *prebuild.Options) { o.Tag = "--privileged" }, want: "--tag is the name of the image to make"},
		{name: "no repository", setup: func(_ *testing.T, _ fakes, o *prebuild.Options) { o.Repository = "" }, want: "--repository is the folder of the repository"},
		{name: "repository missing", setup: func(_ *testing.T, _ fakes, o *prebuild.Options) { o.Repository = filepath.Join(o.Repository, "nope") },
			want: "is not a folder"},
		{name: "legacy dockerFile", config: `{"dockerFile": "Dockerfile"}`, want: ".devcontainer/devcontainer.json sets dockerFile, an earlier form of build.dockerfile"},
		{name: "legacy context with image", config: `{"image": "alpine", "context": ".."}`, want: "sets context, an earlier form"},
		{name: "compose", config: `{"dockerComposeFile": "compose.yaml", "service": "app"}`, want: "sets dockerComposeFile"},
		{name: "build options", config: `{"build": {"dockerfile": "Dockerfile", "options": ["--network=host"]}}`, want: "sets build.options"},
		{name: "neither image nor build", config: `{"onCreateCommand": "make"}`, want: "sets neither image nor build"},
		{name: "host variable in build.args", config: `{"build": {"dockerfile": "Dockerfile", "args": {"TOKEN": "${localEnv:GITHUB_TOKEN}"}}}`,
			want: "build.args.TOKEN uses ${localEnv:…}, a value of the host"},
		{name: "host variable in image", config: `{"image": "${env:IMAGE}"}`, want: "image uses ${env:…}"},
		{name: "host variable in containerEnv", config: `{"image": "alpine", "containerEnv": {"A": "${localEnv:HOME}"}}`, want: "containerEnv.A uses ${localEnv:…}"},
		{name: "host variable in remoteEnv", config: `{"image": "alpine", "remoteEnv": {"A": "${localWorkspaceFolder}"}}`, want: "remoteEnv.A uses ${localWorkspaceFolder}"},
		{name: "host variable in workspaceFolder", config: `{"image": "alpine", "workspaceFolder": "${localWorkspaceFolderBasename}"}`, want: "workspaceFolder uses"},
		{name: "containerEnv widens the agent", config: `{"image": "alpine", "containerEnv": {"PATH": "/x", "NODE_OPTIONS": "--require /x.js", "CLAUDE_CONFIG_DIR": "/x"}}`,
			want: ".devcontainer/devcontainer.json sets CLAUDE_CONFIG_DIR and NODE_OPTIONS in containerEnv, which change what the agent does; no image is built from it"},
		{name: "remoteEnv widens the agent", config: `{"image": "alpine", "remoteEnv": {"LD_LIBRARY_PATH": "/x", "QORY_RUN_KEY": "k"}}`,
			want: ".devcontainer/devcontainer.json sets QORY_RUN_KEY in remoteEnv, which change what the agent does"},
		{name: "context the root", config: `{"build": {"dockerfile": "Dockerfile", "context": "/"}}`,
			want: ".devcontainer/devcontainer.json sets build.context to a place outside the repository; the image is built only from the repository's files"},
		{name: "context above", config: `{"build": {"dockerfile": "Dockerfile", "context": "../.."}}`, want: "sets build.context to a place outside the repository"},
		{name: "context far above", config: `{"build": {"dockerfile": "Dockerfile", "context": "../../../.."}}`, want: "sets build.context to a place outside"},
		{name: "dockerfile absolute", config: `{"build": {"dockerfile": "/etc/passwd", "context": ".."}}`, want: "sets build.dockerfile to a place outside the repository"},
		{name: "dockerfile above", config: `{"build": {"dockerfile": "../../Dockerfile", "context": ".."}}`, want: "sets build.dockerfile to a place outside the repository"},
		{name: "context through a link", config: `{"build": {"dockerfile": "Dockerfile", "context": "../out"}}`,
			setup: func(t *testing.T, _ fakes, o *prebuild.Options) {
				if err := os.Symlink(t.TempDir(), filepath.Join(o.Repository, "out")); err != nil {
					t.Fatal(err)
				}
			}, want: "sets build.context to a place outside the repository"},
		{name: "dockerfile through a link", config: `{"build": {"dockerfile": "../out/Dockerfile", "context": ".."}}`,
			setup: func(t *testing.T, _ fakes, o *prebuild.Options) {
				if err := os.Symlink(t.TempDir(), filepath.Join(o.Repository, "out")); err != nil {
					t.Fatal(err)
				}
			}, want: "sets build.dockerfile to a place outside the repository"},
		{name: "context through a link that leads nowhere", config: `{"build": {"dockerfile": "Dockerfile", "context": "../out"}}`,
			setup: func(t *testing.T, _ fakes, o *prebuild.Options) {
				if err := os.Symlink("/nowhere/at/all", filepath.Join(o.Repository, "out")); err != nil {
					t.Fatal(err)
				}
			}, want: ".devcontainer/devcontainer.json sets build.context to a path through a link that does not resolve; the build cannot tell whether it stays in the repository, so it takes no such path"},
		{name: "dockerfile through a link that leads nowhere inside", config: `{"build": {"dockerfile": "../in/Dockerfile", "context": ".."}}`,
			setup: func(t *testing.T, _ fakes, o *prebuild.Options) {
				if err := os.Symlink("bin/missing", filepath.Join(o.Repository, "in")); err != nil {
					t.Fatal(err)
				}
			}, want: ".devcontainer/devcontainer.json sets build.dockerfile to a path through a link that does not resolve"},
		{name: "context through the workspace folder's basename", config: `{"workspaceFolder": "/w/..", "build": {"dockerfile": "Dockerfile", "context": "${containerWorkspaceFolderBasename}/${containerWorkspaceFolderBasename}/../.."}}`,
			want: ".devcontainer/devcontainer.json uses ${ in build.context; devcontainer build gives a variable there a value the build cannot check, so it takes the Dockerfile and the context only as paths written out"},
		{name: "context through a containerEnv the build leaves as written", config: `{"build": {"dockerfile": "Dockerfile", "context": "${containerEnv:X:a}"}}`,
			setup: func(t *testing.T, _ fakes, o *prebuild.Options) {
				if err := os.Symlink("../../../..", filepath.Join(o.Repository, ".devcontainer", "${containerEnv:X:a}")); err != nil {
					t.Fatal(err)
				}
			}, want: ".devcontainer/devcontainer.json uses ${ in build.context;"},
		{name: "dockerfile through a containerEnv the build leaves as written", config: `{"build": {"dockerfile": "${containerEnv:X:Dockerfile}"}}`,
			setup: func(t *testing.T, _ fakes, o *prebuild.Options) {
				for name, content := range map[string]string{"Dockerfile": "FROM alpine:3.20\n", "${containerEnv:X:Dockerfile}": "# syntax=frontend.example.invalid/evil\nFROM alpine:3.20\n"} {
					if err := os.WriteFile(filepath.Join(o.Repository, ".devcontainer", name), []byte(content), 0o644); err != nil {
						t.Fatal(err)
					}
				}
			}, want: ".devcontainer/devcontainer.json uses ${ in build.dockerfile;"},
		{name: "dockerfile through the workspace folder's basename", config: `{"workspaceFolder": "/w/x", "build": {"dockerfile": "${containerWorkspaceFolderBasename}/Dockerfile"}}`,
			setup: func(t *testing.T, _ fakes, o *prebuild.Options) {
				for name, content := range map[string]string{"workspace/Dockerfile": "FROM alpine:3.20\n", "x/Dockerfile": "# syntax=frontend.example.invalid/evil\nFROM alpine:3.20\n"} {
					p := filepath.Join(o.Repository, ".devcontainer", filepath.FromSlash(name))
					if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
						t.Fatal(err)
					}
				}
			}, want: ".devcontainer/devcontainer.json uses ${ in build.dockerfile;"},
		{name: "another frontend", config: `{"build": {"dockerfile": "Dockerfile"}}`,
			setup: func(t *testing.T, _ fakes, o *prebuild.Options) {
				p := filepath.Join(o.Repository, ".devcontainer", "Dockerfile")
				if err := os.WriteFile(p, []byte("# syntax=example.com/evil\nFROM alpine:3.20\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}, want: ".devcontainer/devcontainer.json builds from .devcontainer/Dockerfile, whose syntax directive names a Dockerfile frontend other than docker/dockerfile"},
		{name: "another frontend in build.args", config: `{"build": {"dockerfile": "Dockerfile", "args": {"BUILDKIT_SYNTAX": "example.com/evil"}}}`,
			want: ".devcontainer/devcontainer.json sets build.args.BUILDKIT_SYNTAX to a Dockerfile frontend other than docker/dockerfile"},
		{name: "no Dockerfile", config: `{"build": {"dockerfile": "Dockerfile"}}`,
			want: ".devcontainer/devcontainer.json sets build.dockerfile to .devcontainer/Dockerfile, which cannot be read"},
		{name: "local cache", config: `{"build": {"dockerfile": "Dockerfile", "cacheFrom": "type=local,src=/var/lib/docker"}}`,
			want: ".devcontainer/devcontainer.json sets build.cacheFrom to a cache other than an image in a registry; the build takes its cache only from a registry"},
		{name: "local cache in an array", config: `{"build": {"dockerfile": "Dockerfile", "cacheFrom": ["acme/shop:cache", "type=local,src=/"]}}`,
			want: "sets build.cacheFrom to a cache other than an image in a registry"},
		{name: "privileged", config: `{"image": "alpine", "privileged": true}`, want: "sets privileged, which give the container access to the host"},
		{name: "run args", config: `{"image": "alpine", "runArgs": ["--network=host"]}`, want: "sets runArgs"},
		{name: "not JSON", config: `{"image": `, want: "is not JSON with comments"},
		{name: "another version", setup: func(t *testing.T, f fakes, _ *prebuild.Options) { f.set(t, "version", "0.88.0\n") },
			want: "the devcontainer command is 0.88.0, and the build runs @devcontainers/cli 0.89.0 exactly"},
		{name: "no devcontainer command", setup: func(t *testing.T, f fakes, _ *prebuild.Options) {
			if err := os.Remove(f.path("devcontainer")); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", f.dir)
		}, want: "no devcontainer command on the PATH"},
		{name: "helper not Linux", setup: func(t *testing.T, f fakes, o *prebuild.Options) { o.Helper = f.path("docker") },
			want: "qory's Linux build"},
		{name: "helper for another architecture", setup: func(t *testing.T, f fakes, _ *prebuild.Options) { f.set(t, "platform", "linux/arm64\n") },
			want: "is for amd64, and the engine runs linux/arm64"},
		{name: "feature asks for privileged", setup: func(t *testing.T, f fakes, _ *prebuild.Options) {
			f.set(t, "merged", `{"privileged":true,"capAdd":["SYS_PTRACE"],"entrypoints":["/x.sh"],"init":true}`)
		}, want: "the features or the base image of .devcontainer/devcontainer.json ask for privileged, capAdd, entrypoint and init"},
		{name: "feature asks for a mount", setup: func(t *testing.T, f fakes, _ *prebuild.Options) {
			f.set(t, "merged", `{"mounts":[{"source":"/var/run/docker.sock","target":"/var/run/docker.sock","type":"bind"}],"securityOpt":["seccomp=unconfined"]}`)
		}, want: "ask for securityOpt and mounts"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFakes(t)
			config := c.config
			if config == "" {
				config = shopConfig
			}
			var out bytes.Buffer
			o := options(t, repository(t, config), &out, io.Discard)
			if c.setup != nil {
				c.setup(t, f, &o)
			}
			err := prebuild.Build(t.Context(), o)
			var refusal *prebuild.RefusedError
			if !errors.As(err, &refusal) {
				t.Fatalf("Build: %v, not a refusal", err)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("the refusal is %q, want %q in it", err, c.want)
			}
			if f.called("devcontainer", "build") || f.called("docker", "build") || f.called("docker", "create") || f.called("docker", "start") {
				t.Errorf("something was built or run:\n%s\n%s", f.read("devcontainer.log"), f.read("docker.log"))
			}
			if strings.Contains(err.Error(), secret) {
				t.Error("the refusal quotes a value")
			}
		})
	}
}

// TestBuildRefusesAnEmptyDockerComposeFile is dockerComposeFile set to nothing beside
// an image: the reference implementation builds from the default Compose files then,
// so it is refused before any program runs.
func TestBuildRefusesAnEmptyDockerComposeFile(t *testing.T) {
	for _, value := range []string{`[]`, `""`, `null`} {
		t.Run(value, func(t *testing.T) {
			f := newFakes(t)
			repo := repository(t, `{"image": "alpine", "dockerComposeFile": `+value+`, "service": "app"}`)
			err := prebuild.Build(t.Context(), options(t, repo, io.Discard, io.Discard))
			var refusal *prebuild.RefusedError
			if !errors.As(err, &refusal) || err.Error() != ".devcontainer/devcontainer.json sets dockerComposeFile; an image is built only from image or build, and Docker Compose can give the container access to the host" {
				t.Fatalf("Build: %v", err)
			}
			if f.read("devcontainer.log") != "" || f.read("docker.log") != "" {
				t.Errorf("a program ran:\n%s\n%s", f.read("devcontainer.log"), f.read("docker.log"))
			}
		})
	}
}

// TestBuildRefusesAConfigurationsInitOnlyFromFeatures is init set by the configuration
// itself, which the merged configuration carries too: it is no refusal.
func TestBuildRefusesAConfigurationsInitOnlyFromFeatures(t *testing.T) {
	f := newFakes(t)
	f.set(t, "merged", `{"init":true}`)
	o := options(t, repository(t, `{"image": "alpine", "init": true}`), io.Discard, io.Discard)
	if err := prebuild.Build(t.Context(), o); err != nil {
		t.Fatalf("Build: %v", err)
	}
}

// TestBuildRefusesAnOddFile is a repository holding a named pipe, refused before the
// build.
func TestBuildRefusesAnOddFile(t *testing.T) {
	f := newFakes(t)
	repo := repository(t, shopConfig)
	if err := syscall.Mkfifo(filepath.Join(repo, "pipe"), 0o644); err != nil {
		t.Skip("no named pipe here:", err)
	}
	err := prebuild.Build(t.Context(), options(t, repo, io.Discard, io.Discard))
	var refusal *prebuild.RefusedError
	if !errors.As(err, &refusal) || !strings.Contains(err.Error(), "the repository's pipe is neither a file, a folder nor a link") {
		t.Fatalf("Build: %v", err)
	}
	if f.called("devcontainer", "build") {
		t.Error("the image was built")
	}
}

// TestBuildPrintsAWorkspaceFolderElsewhere is a workspaceFolder other than /workspace:
// a line, and the build goes on.
func TestBuildPrintsAWorkspaceFolderElsewhere(t *testing.T) {
	newFakes(t)
	var out bytes.Buffer
	o := options(t, repository(t, `{"image": "alpine", "workspaceFolder": "/workspaces/shop"}`), &out, io.Discard)
	if err := prebuild.Build(t.Context(), o); err != nil {
		t.Fatalf("Build: %v", err)
	}
	want := "qory run prebuild: workspaceFolder is /workspaces/shop; the image is built at /workspace, where a run mounts its working folder, and the lifecycle commands see ${containerWorkspaceFolder} as /workspace\n"
	if !strings.Contains(out.String(), want) {
		t.Errorf("printed\n%s\nwant\n%s", out.String(), want)
	}
}

// TestBuildWithoutConfiguration is a repository with none: one line, nothing run.
func TestBuildWithoutConfiguration(t *testing.T) {
	f := newFakes(t)
	var out bytes.Buffer
	err := prebuild.Build(t.Context(), options(t, t.TempDir(), &out, io.Discard))
	if !errors.Is(err, prebuild.ErrNoConfiguration) {
		t.Fatalf("Build: %v", err)
	}
	want := "qory run prebuild: the repository has no development container configuration, .devcontainer/devcontainer.json or .devcontainer.json; no image is built\n"
	if out.String() != want {
		t.Errorf("printed %q, want %q", out.String(), want)
	}
	if f.read("docker.log") != "" || f.read("devcontainer.log") != "" {
		t.Error("a program ran")
	}
}

// TestBuildRefusesTheLabelOfTheBuiltImage is a Dockerfile that sets a label asking for
// access to the host, which only the built image shows: refused, and the images the
// build made removed.
func TestBuildRefusesTheLabelOfTheBuiltImage(t *testing.T) {
	f := newFakes(t)
	f.set(t, "config", `{"Labels":{"devcontainer.metadata":"[{\"id\":\"x\"},{\"capAdd\":[\"NET_ADMIN\"]}]"}}`)
	err := prebuild.Build(t.Context(), options(t, repository(t, shopConfig), io.Discard, io.Discard))
	if err == nil || err.Error() != "the built image's devcontainer.metadata asks for capAdd, which give the container access to the host; the image is removed" {
		t.Fatalf("Build: %v", err)
	}
	if f.called("docker", "create") || !f.called("docker", "image rm --force "+testName+":base") {
		t.Errorf("docker ran\n%s", f.read("docker.log"))
	}
}

// TestBuildFailsWithTheSeedStepsLine is a lifecycle command that failed in the
// container: its line, printed once by the step, is the failure, and the container and
// the images are removed.
func TestBuildFailsWithTheSeedStepsLine(t *testing.T) {
	f := newFakes(t)
	f.set(t, "start.sh", `echo "qory run prebuild: onCreateCommand exited with status 2"; echo "make: no rule" >&2; exit 1
`)
	var out bytes.Buffer
	err := prebuild.Build(t.Context(), options(t, repository(t, shopConfig), &out, io.Discard))
	var shown *prebuild.ShownError
	if !errors.As(err, &shown) || shown.Line != "onCreateCommand exited with status 2" {
		t.Fatalf("Build: %v", err)
	}
	if strings.Count(out.String(), "onCreateCommand exited with status 2") != 1 || strings.Contains(out.String(), "make setup") {
		t.Errorf("printed\n%s", out.String())
	}
	if !f.called("docker", "rm --force --volumes "+testName) || !f.called("docker", "image rm --force "+testName+":base "+testName+":top") || f.called("docker", "commit") {
		t.Errorf("docker ran\n%s", f.read("docker.log"))
	}
}

// TestBuildFailsWhenTheContainerDies is a container that exited without the step's
// line: its status.
func TestBuildFailsWhenTheContainerDies(t *testing.T) {
	f := newFakes(t)
	f.set(t, "start.sh", "exit 137\n")
	err := prebuild.Build(t.Context(), options(t, repository(t, shopConfig), io.Discard, io.Discard))
	if err == nil || err.Error() != "the container of the lifecycle commands exited with status 137" {
		t.Fatalf("Build: %v", err)
	}
}

// TestBuildRefusesAnImageOverTheLimit is an image of more than 4 GiB: refused, and
// removed with the temporary images.
func TestBuildRefusesAnImageOverTheLimit(t *testing.T) {
	f := newFakes(t)
	f.set(t, "size", strconv.Itoa(4<<30+1)+"\n")
	var out bytes.Buffer
	err := prebuild.Build(t.Context(), options(t, repository(t, shopConfig), &out, io.Discard))
	var refusal *prebuild.RefusedError
	if !errors.As(err, &refusal) || err.Error() != "the image is 4.0 GiB (4294967297 bytes), more than the limit of 4 GiB; it is removed" {
		t.Fatalf("Build: %v", err)
	}
	if !f.called("docker", "image rm --force "+testName+":base "+testName+":top "+testName+":seeded acme/shop:prebuild") {
		t.Errorf("the image is not removed:\n%s", f.read("docker.log"))
	}
	if strings.Contains(out.String(), "built acme/shop:prebuild") {
		t.Error("the image is said built")
	}
}

// TestBuildStopsAtItsLimit is a build still running at its limit: the program running
// and what it started are ended, and the container and the images are removed. The
// test ends the build's context with [prebuild.ErrLimit] as its cause once the
// container's program runs, as the limit of [prebuild.Build] does when it passes, so
// the limit comes while the program runs however long the steps before it take.
func TestBuildStopsAtItsLimit(t *testing.T) {
	prebuild.ShortenGrace(t, 200*time.Millisecond)
	f := newFakes(t)
	f.set(t, "start.sh", `sleep 60 &
echo $! > `+f.path("pid.new")+`
mv `+f.path("pid.new")+` `+f.path("pid")+`
wait
`)
	ctx, limit := context.WithCancelCause(t.Context())
	reached := make(chan time.Time, 1)
	go func() {
		for {
			if _, err := os.Stat(f.path("pid")); err == nil || ctx.Err() != nil {
				reached <- time.Now()
				limit(prebuild.ErrLimit)
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
	}()
	err := prebuild.Build(ctx, options(t, repository(t, shopConfig), io.Discard, io.Discard))
	if !errors.Is(err, prebuild.ErrLimit) {
		t.Fatalf("Build: %v", err)
	}
	if took := time.Since(<-reached); took > 20*time.Second {
		t.Errorf("Build took %s past its limit", took)
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(f.read("pid")))
	if pid == 0 {
		t.Fatal("the fake start logged no process")
	}
	deadline := time.Now().Add(20 * time.Second)
	for !ended(pid) && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if !ended(pid) {
		_ = syscall.Kill(pid, syscall.SIGKILL)
		t.Error("the process docker start started is left running")
	}
	if !f.called("docker", "rm --force --volumes "+testName) || !f.called("docker", "image rm --force "+testName+":base "+testName+":top") {
		t.Errorf("docker ran\n%s", f.read("docker.log"))
	}
}

// ended is true when pid is gone or is a zombie. A PID 1 that reaps no child, as
// tail in a GitHub Actions job container, keeps an ended orphan as a zombie, and
// kill(pid, 0) succeeds on a zombie. Without /proc, as on macOS, only kill counts.
func ended(pid int) bool {
	if syscall.Kill(pid, 0) != nil {
		return true
	}
	stat, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return false
	}
	// The state follows the command name, which is in parentheses and may hold spaces.
	i := bytes.LastIndexByte(stat, ')')
	return i >= 0 && i+2 < len(stat) && stat[i+2] == 'Z'
}

// TestBuildStopsAtTheLimitOfItsOptions is a build whose Options.Limit has passed when
// it starts: it stops with [prebuild.ErrLimit] before it creates a container.
func TestBuildStopsAtTheLimitOfItsOptions(t *testing.T) {
	f := newFakes(t)
	o := options(t, repository(t, shopConfig), io.Discard, io.Discard)
	o.Limit = time.Nanosecond
	if err := prebuild.Build(t.Context(), o); !errors.Is(err, prebuild.ErrLimit) {
		t.Fatalf("Build: %v", err)
	}
	if f.called("docker", "create") {
		t.Errorf("docker ran\n%s", f.read("docker.log"))
	}
}

// TestBuildStopsOnCancel is a build whose context ends, as on a signal, during the
// image's build: stopped, and the images removed.
func TestBuildStopsOnCancel(t *testing.T) {
	prebuild.ShortenGrace(t, 200*time.Millisecond)
	f := newFakes(t)
	f.set(t, "build.sh", "touch "+f.path("building")+"\nsleep 60\n")
	ctx, cancel := context.WithCancel(t.Context())
	go func() {
		for {
			if _, err := os.Stat(f.path("building")); err == nil || ctx.Err() != nil {
				cancel()
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
	}()
	err := prebuild.Build(ctx, options(t, repository(t, shopConfig), io.Discard, io.Discard))
	if !errors.Is(err, prebuild.ErrStopped) {
		t.Fatalf("Build: %v", err)
	}
	if !f.called("docker", "image rm --force "+testName+":base") || f.called("docker", "create") {
		t.Errorf("docker ran\n%s", f.read("docker.log"))
	}
}

// TestBuildRefusesAnImageEnvironmentThatWidensTheAgent is a variable that changes what
// the agent does in the environment of the image devcontainer build made, of the image
// the commands run in, and of the final image: each refused, named without its value,
// and every image the build made removed.
func TestBuildRefusesAnImageEnvironmentThatWidensTheAgent(t *testing.T) {
	cases := []struct {
		name, ref string
		created   bool
	}{
		{name: "built", ref: testName + "_base"},
		{name: "with the agent", ref: testName + "_top"},
		{name: "final", ref: "acme_shop_prebuild", created: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFakes(t)
			f.set(t, "config-"+c.ref, `{"Entrypoint":null,"Cmd":["/bin/sh"],"Env":["PATH=/usr/bin","LD_LIBRARY_PATH=/opt/lib","OTEL_EXPORTER_OTLP_ENDPOINT=https://otel.example.com","LD_PRELOAD=/opt/`+secret+`.so"]}`)
			err := prebuild.Build(t.Context(), options(t, repository(t, shopConfig), io.Discard, io.Discard))
			var refusal *prebuild.RefusedError
			want := "the image sets OTEL_EXPORTER_OTLP_ENDPOINT and LD_PRELOAD in its environment, which change what the agent does; the image is removed"
			if !errors.As(err, &refusal) || err.Error() != want {
				t.Fatalf("Build: %v, want %q", err, want)
			}
			if f.called("docker", "create") != c.created {
				t.Errorf("docker ran\n%s", f.read("docker.log"))
			}
			removed := "image rm --force " + testName + ":base"
			if c.created {
				removed = "image rm --force " + testName + ":base " + testName + ":top " + testName + ":seeded acme/shop:prebuild"
			}
			if !f.called("docker", removed) {
				t.Errorf("the images are not removed:\n%s", f.read("docker.log"))
			}
		})
	}
}

// TestBuildPassesTheProxies is a machine with proxies and certificate authorities: the
// devcontainer command and docker get them, but a proxy whose URL carries a user, which
// a line names and no output quotes; and docker create gets none, so the commands'
// container has none.
func TestBuildPassesTheProxies(t *testing.T) {
	f := newFakes(t)
	t.Setenv("HTTPS_PROXY", "http://proxy.example.com:3128")
	t.Setenv("no_proxy", "localhost,.example.com")
	t.Setenv("SSL_CERT_FILE", "/etc/ssl/certs/ca.pem")
	t.Setenv("HTTP_PROXY", "http://agent:pw-"+secret+"@proxy.example.com:3128")
	t.Setenv("all_proxy", "socks5://agent-"+secret+"@proxy.example.com:1080")
	var out, log bytes.Buffer
	err := prebuild.Build(t.Context(), options(t, repository(t, shopConfig), &out, &log))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	lines := strings.Split(out.String(), "\n")
	want := []string{
		"qory run prebuild: HTTP_PROXY holds an @, which can put a user name or a password in a URL, a secret; the build runs without it",
		"qory run prebuild: all_proxy holds an @, which can put a user name or a password in a URL, a secret; the build runs without it",
	}
	if len(lines) < 2 || !slices.Equal(lines[:2], want) {
		t.Errorf("printed\n%s", out.String())
	}
	if strings.Contains(out.String()+log.String(), secret) {
		t.Error("the output quotes a proxy's value")
	}
	for _, program := range []string{"docker", "devcontainer"} {
		env := strings.Split(f.read(program+".env"), "\n")
		for _, kv := range []string{"HTTPS_PROXY=http://proxy.example.com:3128", "no_proxy=localhost,.example.com", "SSL_CERT_FILE=/etc/ssl/certs/ca.pem"} {
			if !slices.Contains(env, kv) {
				t.Errorf("%s did not get %s", program, kv)
			}
		}
		for _, kv := range env {
			if strings.Contains(kv, secret) || strings.HasPrefix(kv, "HTTP_PROXY=") || strings.HasPrefix(kv, "all_proxy=") {
				t.Errorf("%s got %s", program, kv)
			}
		}
	}
	for _, l := range f.calls("docker") {
		if strings.HasPrefix(l, "create") && strings.Contains(strings.ToLower(l), "proxy") {
			t.Errorf("docker %s", l)
		}
	}
}

// TestBuildKeepsTheImagesHome is an image whose environment sets HOME: the build sets
// none and makes no folder for it.
func TestBuildKeepsTheImagesHome(t *testing.T) {
	f := newFakes(t)
	f.set(t, "config-"+testName+"_base", `{"Entrypoint":null,"Cmd":["/bin/sh"],"Env":["PATH=/usr/bin","HOME=/home/dev"]}`)
	if err := prebuild.Build(t.Context(), options(t, repository(t, shopConfig), io.Discard, io.Discard)); err != nil {
		t.Fatalf("Build: %v", err)
	}
	if strings.Contains(f.read("dockerfiles"), "HOME") {
		t.Errorf("the Dockerfiles set HOME:\n%s", f.read("dockerfiles"))
	}
	r := tar.NewReader(strings.NewReader(f.read("tree.tar")))
	for {
		h, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(h.Name, "home/") {
			t.Errorf("the stream holds %s", h.Name)
		}
	}
}

// dockerConfigWithProxies is an original docker configuration with the registries'
// logins, a credential store, a current context and proxies that carry a password,
// named in two cases.
const dockerConfigWithProxies = `{
	"auths": {"registry.example.com": {"auth": "YWdlbnQ6cGFzcw=="}},
	"credsStore": "pass",
	"currentContext": "shop",
	"proxies": {"default": {"httpProxy": "http://agent:` + secret + `@proxy.example.com:3128"}},
	"Proxies": {"default": {"noProxy": "*"}}
}`

// originalDockerConfig writes an original docker configuration folder holding
// config.json, and contexts and cli-plugins folders.
func originalDockerConfig(t *testing.T, config string) string {
	t.Helper()
	dir := t.TempDir()
	for _, sub := range []string{"contexts/meta", "cli-plugins"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestBuildHandsDockerAConfigurationWithoutProxies is the docker configuration every
// program of the build reads: the original's, from DOCKER_CONFIG or from HOME, without
// its proxies in any case, with its logins, credential store and current context, and
// its contexts and plugins linked; a folder of the build's, mode 0700, removed after.
func TestBuildHandsDockerAConfigurationWithoutProxies(t *testing.T) {
	for _, from := range []string{"DOCKER_CONFIG", "HOME"} {
		t.Run(from, func(t *testing.T) {
			f := newFakes(t)
			original := originalDockerConfig(t, dockerConfigWithProxies)
			if from == "HOME" {
				home := t.TempDir()
				if err := os.Rename(original, filepath.Join(home, ".docker")); err != nil {
					t.Fatal(err)
				}
				original = filepath.Join(home, ".docker")
				t.Setenv("HOME", home)
				t.Setenv("DOCKER_CONFIG", "")
				os.Unsetenv("DOCKER_CONFIG")
			} else {
				t.Setenv("DOCKER_CONFIG", original)
			}
			var out, log bytes.Buffer
			if err := prebuild.Build(t.Context(), options(t, repository(t, shopConfig), &out, &log)); err != nil {
				t.Fatalf("Build: %v", err)
			}
			var got map[string]json.RawMessage
			if err := json.Unmarshal([]byte(f.read("docker-config.json")), &got); err != nil {
				t.Fatalf("docker read %q: %v", f.read("docker-config.json"), err)
			}
			for name := range got {
				if strings.EqualFold(name, "proxies") {
					t.Errorf("docker read %s", name)
				}
			}
			for _, name := range []string{"auths", "credsStore", "currentContext"} {
				if got[name] == nil {
					t.Errorf("docker did not read %s", name)
				}
			}
			if links := f.read("docker-config-links"); links != "contexts\ncli-plugins\n" {
				t.Errorf("the links are %q", links)
			}
			folder := strings.TrimSpace(f.read("docker-config-path"))
			if folder == "" || folder == original {
				t.Errorf("docker read DOCKER_CONFIG %q", folder)
			}
			if _, err := os.Stat(folder); err == nil {
				t.Error("the build's docker configuration is left")
			}
			if !strings.Contains(f.read("devcontainer.env"), "DOCKER_CONFIG="+folder+"\n") {
				t.Error("devcontainer read another docker configuration")
			}
			if data, _ := os.ReadFile(filepath.Join(original, "config.json")); string(data) != dockerConfigWithProxies {
				t.Error("the original changed")
			}
			if strings.Contains(out.String()+log.String(), secret) {
				t.Error("the output quotes the proxy")
			}
		})
	}
}

// TestBuildWithoutADockerConfiguration is a machine with none: an empty one.
func TestBuildWithoutADockerConfiguration(t *testing.T) {
	f := newFakes(t)
	if err := prebuild.Build(t.Context(), options(t, repository(t, shopConfig), io.Discard, io.Discard)); err != nil {
		t.Fatalf("Build: %v", err)
	}
	if got := f.read("docker-config.json"); got != "{}" {
		t.Errorf("docker read %q", got)
	}
}

// TestBuildRefusesADockerConfigurationThatDoesNotRead is an original that is not a JSON
// object: refused, named, never quoted, before docker runs.
func TestBuildRefusesADockerConfigurationThatDoesNotRead(t *testing.T) {
	for name, config := range map[string]string{"not JSON": `{"auths": "` + secret, "an array": `["` + secret + `"]`} {
		t.Run(name, func(t *testing.T) {
			f := newFakes(t)
			original := originalDockerConfig(t, config)
			t.Setenv("DOCKER_CONFIG", original)
			err := prebuild.Build(t.Context(), options(t, repository(t, shopConfig), io.Discard, io.Discard))
			var refusal *prebuild.RefusedError
			want := "the docker configuration " + filepath.Join(original, "config.json") + " does not read as a JSON object; the build reads it for the registries' logins"
			if !errors.As(err, &refusal) || err.Error() != want {
				t.Fatalf("Build: %v", err)
			}
			if f.read("docker.log") != "" || f.read("devcontainer.log") != "" {
				t.Error("a program ran")
			}
		})
	}
}

// TestBuildRefusesProxiesTheCommandsLeft is a final image whose environment sets a
// proxy the image before the commands did not: refused, named, and the images removed.
func TestBuildRefusesProxiesTheCommandsLeft(t *testing.T) {
	f := newFakes(t)
	f.set(t, "config-"+testName+"_top", `{"Entrypoint":null,"Cmd":["/bin/sh"],"Env":["PATH=/usr/bin","HOME=/home/agent","NO_PROXY=localhost"]}`)
	f.set(t, "config-acme_shop_prebuild", `{"Entrypoint":null,"Cmd":["/bin/sh"],"Env":["PATH=/usr/bin","HOME=/home/agent","NO_PROXY=localhost","https_proxy=http://agent:`+secret+`@proxy.example.com","FTP_PROXY=x"]}`)
	var out bytes.Buffer
	err := prebuild.Build(t.Context(), options(t, repository(t, shopConfig), &out, io.Discard))
	var refusal *prebuild.RefusedError
	want := "the final image sets https_proxy and FTP_PROXY in its environment to a value the image before the commands did not; the image is removed"
	if !errors.As(err, &refusal) || err.Error() != want {
		t.Fatalf("Build: %v", err)
	}
	if !f.called("docker", "image rm --force "+testName+":base "+testName+":top "+testName+":seeded acme/shop:prebuild") {
		t.Errorf("the images are not removed:\n%s", f.read("docker.log"))
	}
	if strings.Contains(out.String(), secret) {
		t.Error("the output quotes the proxy")
	}
}

// TestBuildTakesAContextInsideTheRepository is a context of .. from .devcontainer, the
// repository's root, and a link that stays inside it, and a cache from a registry:
// built.
func TestBuildTakesAContextInsideTheRepository(t *testing.T) {
	for name, config := range map[string]string{
		"..":             `{"build": {"dockerfile": "Dockerfile", "context": ".."}}`,
		"no context":     `{"build": {"dockerfile": "Dockerfile"}}`,
		"inside link":    `{"build": {"dockerfile": "Dockerfile", "context": "../in"}}`,
		"dockerfile ..":  `{"build": {"dockerfile": "../Dockerfile", "context": "."}}`,
		"registry cache": `{"build": {"dockerfile": "Dockerfile", "cacheFrom": ["acme/shop:cache", "type=registry,ref=example.com/acme/shop:cache"]}}`,
	} {
		t.Run(name, func(t *testing.T) {
			newFakes(t)
			repo := repository(t, config)
			if err := os.Symlink("bin", filepath.Join(repo, "in")); err != nil {
				t.Fatal(err)
			}
			for _, p := range []string{".devcontainer/Dockerfile", "Dockerfile"} {
				if err := os.WriteFile(filepath.Join(repo, p), []byte("FROM alpine:3.20\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if err := prebuild.Build(t.Context(), options(t, repo, io.Discard, io.Discard)); err != nil {
				t.Fatalf("Build: %v", err)
			}
		})
	}
}

// TestBuildKeepsTheEnginesCertificates is a TLS engine: docker reads its certificates
// from DOCKER_CERT_PATH, else from DOCKER_CONFIG's folder, which is the build's; with
// none set, DOCKER_CERT_PATH is the original folder, and one set is kept.
func TestBuildKeepsTheEnginesCertificates(t *testing.T) {
	t.Run("not set", func(t *testing.T) {
		f := newFakes(t)
		original := originalDockerConfig(t, `{}`)
		t.Setenv("DOCKER_CONFIG", original)
		t.Setenv("DOCKER_TLS_VERIFY", "1")
		if err := prebuild.Build(t.Context(), options(t, repository(t, shopConfig), io.Discard, io.Discard)); err != nil {
			t.Fatalf("Build: %v", err)
		}
		for _, program := range []string{"docker", "devcontainer"} {
			env := f.read(program + ".env")
			if !strings.Contains(env, "DOCKER_CERT_PATH="+original+"\n") || !strings.Contains(env, "DOCKER_TLS_VERIFY=1\n") {
				t.Errorf("%s got\n%s", program, env)
			}
		}
	})
	t.Run("set", func(t *testing.T) {
		f := newFakes(t)
		t.Setenv("DOCKER_TLS_VERIFY", "1")
		t.Setenv("DOCKER_CERT_PATH", "/etc/docker/certs")
		if err := prebuild.Build(t.Context(), options(t, repository(t, shopConfig), io.Discard, io.Discard)); err != nil {
			t.Fatalf("Build: %v", err)
		}
		env := f.read("docker.env")
		if !strings.Contains(env, "DOCKER_CERT_PATH=/etc/docker/certs\n") || strings.Count(env, "DOCKER_CERT_PATH=") != strings.Count(env, "DOCKER_CERT_PATH=/etc/docker/certs") {
			t.Errorf("docker got\n%s", env)
		}
	})
}

// TestBuildRefusesAProxyTheCommandsReplaced is a base image that sets HTTP_PROXY whose
// value the final image's differs from, as docker create's replacement leaves it:
// refused, and one the final image keeps as it was is not.
func TestBuildRefusesAProxyTheCommandsReplaced(t *testing.T) {
	top := `{"Entrypoint":null,"Cmd":["/bin/sh"],"Env":["PATH=/usr/bin","HOME=/home/agent","HTTP_PROXY=http://proxy.example.com:3128"]}`
	t.Run("replaced", func(t *testing.T) {
		f := newFakes(t)
		f.set(t, "config-"+testName+"_top", top)
		f.set(t, "config-acme_shop_prebuild", `{"Entrypoint":null,"Cmd":["/bin/sh"],"Env":["PATH=/usr/bin","HOME=/home/agent","HTTP_PROXY=http://agent:`+secret+`@proxy.example.com:3128"]}`)
		var out bytes.Buffer
		err := prebuild.Build(t.Context(), options(t, repository(t, shopConfig), &out, io.Discard))
		want := "the final image sets HTTP_PROXY in its environment to a value the image before the commands did not; the image is removed"
		if err == nil || err.Error() != want {
			t.Fatalf("Build: %v", err)
		}
		if strings.Contains(out.String()+err.Error(), secret) {
			t.Error("the output quotes the proxy")
		}
	})
	t.Run("kept", func(t *testing.T) {
		f := newFakes(t)
		f.set(t, "config-"+testName+"_top", top)
		f.set(t, "config-acme_shop_prebuild", top)
		if err := prebuild.Build(t.Context(), options(t, repository(t, shopConfig), io.Discard, io.Discard)); err != nil {
			t.Fatalf("Build: %v", err)
		}
	})
}

// TestBuildPinsBuildKit is a job that turns BuildKit off: every program of the build
// gets DOCKER_BUILDKIT=1 alone.
func TestBuildPinsBuildKit(t *testing.T) {
	for _, value := range []string{"0", "false", ""} {
		t.Run("set to "+strconv.Quote(value), func(t *testing.T) {
			f := newFakes(t)
			t.Setenv("DOCKER_BUILDKIT", value)
			if err := prebuild.Build(t.Context(), options(t, repository(t, shopConfig), io.Discard, io.Discard)); err != nil {
				t.Fatalf("Build: %v", err)
			}
			for _, program := range []string{"docker", "devcontainer"} {
				env := "\n" + f.read(program+".env")
				calls := len(f.calls(program))
				if strings.Count(env, "\nDOCKER_BUILDKIT=1\n") != calls || strings.Count(env, "\nDOCKER_BUILDKIT=") != calls {
					t.Errorf("%s ran %d times and got\n%s", program, calls, env)
				}
			}
		})
	}
}
