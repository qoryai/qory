// Package user reads the name to greet a person with from the local configuration.
//
// [Name] takes git's configured user name first and the login recorded by the gh CLI
// second, and returns an empty string when neither names anybody. It reads git config and
// gh's hosts file and nothing else, so it never touches the network and never asks
// GitHub who the person is.
package user

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Name returns a name to greet the person with, or "" when nothing on this machine names
// one. It tries two sources, in this order:
//
//   - the first word of git's user.name, read by running git in the process's working
//     directory, so a checkout's own config wins over the global one
//   - the login of the github.com account in gh's hosts file
//
// The result is a first name only when git's user.name begins with one, and a gh login is
// returned whole. Name reports no error. A missing git, an unset user.name and an
// unreadable hosts file each fall through to the next source, and the last of them
// returns "".
func Name() string {
	if out, err := exec.Command("git", "config", "user.name").Output(); err == nil {
		if name := strings.TrimSpace(string(out)); name != "" {
			return strings.Fields(name)[0]
		}
	}
	return ghLogin()
}

// ghLogin reads the login of the active github.com account from gh's hosts file,
// hosts.yml under GH_CONFIG_DIR when that is set and under ~/.config/gh otherwise. It
// returns "" when the home directory is unknown, the file is missing or not YAML, or the
// file records no github.com account. It reads the file directly rather than running gh,
// so it costs nothing and works without gh installed.
func ghLogin() string {
	dir := os.Getenv("GH_CONFIG_DIR")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		dir = filepath.Join(home, ".config", "gh")
	}
	data, err := os.ReadFile(filepath.Join(dir, "hosts.yml"))
	if err != nil {
		return ""
	}
	var hosts map[string]struct {
		User string `yaml:"user"`
	}
	if err := yaml.Unmarshal(data, &hosts); err != nil {
		return ""
	}
	return hosts["github.com"].User
}
