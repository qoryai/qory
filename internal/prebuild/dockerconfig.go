package prebuild

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/qoryai/qory/internal/integration"
)

// The words of the build's docker configuration. The %s is the original's path.
const (
	textDockerConfigUnread = "the docker configuration %s could not be read; the build reads it for the registries' logins"
	textDockerConfigNotObj = "the docker configuration %s does not read as a JSON object; the build reads it for the registries' logins"
	textProxiesUnread      = "check the proxies in the docker configuration at DOCKER_CONFIG: they do not read as the docker command writes them, and a build behind a wall reaches the outside only through them"
)

// dockerConfigFile is the name of the docker command's configuration in its folder.
const dockerConfigFile = "config.json"

// dockerLinks are the folders of the original docker configuration the build's links
// to: the named contexts, so currentContext still resolves, and the command's plugins,
// buildx among them.
var dockerLinks = []string{"contexts", "cli-plugins"}

// dockerConfig makes, in the folder dir, the docker configuration every docker and
// devcontainer command of the build reads through DOCKER_CONFIG, and returns its
// folder. It is the original, the folder DOCKER_CONFIG in env names, else
// $HOME/.docker, without proxies, a member the docker command reads in any case:
// docker create puts those proxies in a container's environment, which docker commit
// keeps in the image, credentials and all, and docker build passes them to RUN as build
// arguments. Every other member is kept as it is: the registries' logins, the
// credential helpers, the current context. The original's contexts and cli-plugins
// folders are linked. An original that is not there is an empty configuration; one
// that does not read is refused, named, never quoted. It returns the original folder
// too, absolute, "" when neither variable names one, and the original's proxies member,
// nil when it has none.
func dockerConfig(dir string, env []string) (folder, original string, proxies json.RawMessage, err error) {
	original = lastValue(env, "DOCKER_CONFIG")
	if original == "" {
		if home := lastValue(env, "HOME"); home != "" {
			original = filepath.Join(home, ".docker")
		}
	}
	members := map[string]json.RawMessage{}
	if original != "" {
		path := filepath.Join(original, dockerConfigFile)
		data, err := os.ReadFile(path)
		switch {
		case errors.Is(err, fs.ErrNotExist):
		case err != nil:
			return "", "", nil, refused(fmt.Errorf(textDockerConfigUnread, integration.Printable(path)))
		case len(bytes.TrimSpace(data)) > 0:
			if json.Unmarshal(data, &members) != nil || members == nil {
				return "", "", nil, refused(fmt.Errorf(textDockerConfigNotObj, integration.Printable(path)))
			}
		}
	}
	for name, value := range members {
		if strings.EqualFold(name, "proxies") {
			if name == "proxies" {
				proxies = value
			}
			delete(members, name)
		}
	}
	if original != "" {
		if abs, err := filepath.Abs(original); err == nil {
			original = abs
		}
	}
	folder = filepath.Join(dir, "docker")
	if err := writeDockerConfig(folder, members, original); err != nil {
		return "", "", nil, err
	}
	return folder, original, proxies, nil
}

// writeDockerConfig writes members as config.json in the new folder, mode 0700, and
// links the original's contexts and cli-plugins folders into it.
func writeDockerConfig(folder string, members map[string]json.RawMessage, original string) error {
	data, err := json.Marshal(members)
	if err != nil {
		return err
	}
	if err := os.Mkdir(folder, 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(folder, dockerConfigFile), data, 0o600); err != nil {
		return err
	}
	if original == "" {
		return nil
	}
	for _, name := range dockerLinks {
		target := filepath.Join(original, name)
		if fi, err := os.Stat(target); err == nil && fi.IsDir() {
			if err := os.Symlink(target, filepath.Join(folder, name)); err != nil {
				return err
			}
		}
	}
	return nil
}

// buildConfig makes, in the folder dir, the docker configuration devcontainer build reads
// behind a wall: the build's, folder, with the original's proxies, so docker passes them
// to the build's steps as Docker's predefined build arguments, HTTP_PROXY, HTTPS_PROXY
// and NO_PROXY in both cases, which the image does not keep. It returns its folder.
func buildConfig(dir, folder, original string, proxies json.RawMessage) (string, error) {
	data, err := os.ReadFile(filepath.Join(folder, dockerConfigFile))
	if err != nil {
		return "", err
	}
	members := map[string]json.RawMessage{}
	if err := json.Unmarshal(data, &members); err != nil {
		return "", err
	}
	members["proxies"] = proxies
	out := filepath.Join(dir, "docker-build")
	return out, writeDockerConfig(out, members, original)
}

// configProxies are the default proxies of a docker configuration's proxies member, as
// the docker command reads them.
type configProxies struct {
	Default struct {
		HTTPProxy  string `json:"httpProxy"`
		HTTPSProxy string `json:"httpsProxy"`
		NoProxy    string `json:"noProxy"`
	} `json:"default"`
}

// proxyEntries are the default proxies of a docker configuration's proxies member as
// variables, NAME=value, in both cases, in the order HTTP_PROXY, HTTPS_PROXY, NO_PROXY:
// what the seed step's commands get behind a wall. A member that does not read is
// refused. A value that may carry user information, as [carriesUser] has it, is left
// out, and its name is in withheld.
func proxyEntries(proxies json.RawMessage) (entries, withheld []string, err error) {
	var p configProxies
	if err := json.Unmarshal(proxies, &p); err != nil {
		return nil, nil, refused(errors.New(textProxiesUnread))
	}
	for _, v := range []struct{ name, value string }{{"HTTP_PROXY", p.Default.HTTPProxy}, {"HTTPS_PROXY", p.Default.HTTPSProxy}, {"NO_PROXY", p.Default.NoProxy}} {
		switch {
		case v.value == "":
		case carriesUser(v.value):
			withheld = append(withheld, v.name)
		default:
			entries = append(entries, v.name+"="+v.value, strings.ToLower(v.name)+"="+v.value)
		}
	}
	return entries, withheld, nil
}

// lastValue is the value env sets name to, the last when it sets it more than once,
// as a process reads it; "" when it sets none.
func lastValue(env []string, name string) string {
	value := ""
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok && k == name {
			value = v
		}
	}
	return value
}

// withValue is env with every name= entry replaced by one name=value, at the end.
func withValue(env []string, name, value string) []string {
	var out []string
	for _, kv := range env {
		if k, _, _ := strings.Cut(kv, "="); k != name {
			out = append(out, kv)
		}
	}
	return append(out, name+"="+value)
}

// proxyNames are the names of the proxy variables, in upper case: a program reads each
// in either case.
var proxyNames = []string{"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "ALL_PROXY", "FTP_PROXY"}

// addedProxies are the names of the proxy variables, in any case, whose entry,
// name=value, after holds and before does not, in the order of after: a proxy the
// commands' container added, or one whose value docker create replaced.
func addedProxies(before, after []string) []string {
	var out []string
	for _, kv := range after {
		name, _, _ := strings.Cut(kv, "=")
		for _, p := range proxyNames {
			if strings.EqualFold(name, p) && !slices.Contains(before, kv) && !slices.Contains(out, name) {
				out = append(out, name)
			}
		}
	}
	return out
}
