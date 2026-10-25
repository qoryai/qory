// Package proxygit is the environment and the options of a git that fetches on this
// machine through a run's host proxy or stack proxy: a loopback forwarder Forager starts
// for the run, with a password. qory fetches a run's repositories through the host
// proxy, which the gateway behind it adds each token to, and the git sources of its
// harness through the stack proxy, in a run from a control plane, so no token is on
// this machine.
//
// Such a git reads no configuration of the person's or the system's, no credential
// helper, no .netrc and no proxy or certificate setting of this process's environment.
// It reaches the remote over https alone, through the proxy, and trusts the run's
// certificate authority through the bundle the run directory contains. The proxy's URL,
// with its password, goes into git's environment and never into its arguments, which any
// user of this machine can read, nor into a git configuration file.
package proxygit

import (
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
)

// Proxy is the run's host proxy or stack proxy as a git on this machine uses it.
type Proxy struct {
	// URL is the forwarder's URL with its password, http://qory:<password>@127.0.0.1:<port>.
	URL string
	// CABundle is the path of the run's CA bundle, the machine's authorities and the
	// run's certificate; "" for a run with no certificate authority.
	CABundle string
}

// Hardened are the options every git qory runs to fetch for a run gets, over the
// repository's configuration: core.hooksPath names /dev/null, where git finds no hook
// file; core.fsmonitor is empty, which turns the file system monitor off; and the
// credential helper is empty, so none is asked.
var Hardened = []string{"-c", "core.hooksPath=" + os.DevNull, "-c", "core.fsmonitor=", "-c", "credential.helper="}

// HTTPSOnly are the options that let git reach a remote over https alone: every other
// transport, ssh, git://, file:// and a local path among them, is off.
var HTTPSOnly = []string{"-c", "protocol.allow=never", "-c", "protocol.https.allow=always"}

// repositoryVariables are git's variables that point it at a repository, an index or a
// store other than the one in its directory.
var repositoryVariables = []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_COMMON_DIR", "GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_NAMESPACE"}

// droppedVariables are git's variables that add configuration, name a program git
// runs or a folder it takes programs or templates from, turn off the check of a
// host's certificate, choose the transports git may use, trace what git does, or name
// replace refs. [Env] drops each, and the ones that set what it sets. GIT_ALLOW_PROTOCOL
// wins over protocol.allow, so with it set, [HTTPSOnly] would not hold.
var droppedVariables = []string{
	"GIT_CONFIG", "GIT_CONFIG_PARAMETERS", "GIT_CONFIG_COUNT", "GIT_CONFIG_GLOBAL", "GIT_CONFIG_SYSTEM", "GIT_CONFIG_NOSYSTEM",
	"GIT_EXEC_PATH", "GIT_TEMPLATE_DIR", "GIT_SSH", "GIT_SSH_COMMAND", "GIT_PROXY_COMMAND", "GIT_SSL_NO_VERIFY",
	"GIT_ALLOW_PROTOCOL", "GIT_PROTOCOL_FROM_USER",
	"GIT_REPLACE_REF_BASE", "GIT_NO_REPLACE_OBJECTS",
	"GIT_TERMINAL_PROMPT", "GIT_ASKPASS", "SSH_ASKPASS",
}

// droppedPrefixes are the starts of the names of git's variables [Env] drops: the
// configuration of GIT_CONFIG_COUNT and git's traces.
var droppedPrefixes = []string{"GIT_CONFIG_KEY_", "GIT_CONFIG_VALUE_", "GIT_TRACE"}

// hostVariables are the variables [Env] drops besides for a git on this machine: the
// ones that choose a proxy, which would take a request past the run's, the ones that
// choose the certificates curl trusts, and the folders git and curl read a person's
// files from, such as .netrc, which holds credentials.
var hostVariables = []string{
	"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "all_proxy", "no_proxy",
	"CURL_CA_BUNDLE", "SSL_CERT_FILE", "SSL_CERT_DIR", "CURL_HOME", "NETRC",
	"HOME", "XDG_CONFIG_HOME",
}

// hostPrefixes are the starts of the names of the variables [Env] drops besides for a
// git on this machine: git's settings of TLS, of http and of the proxy.
var hostPrefixes = []string{"GIT_SSL_", "GIT_HTTP_", "GIT_PROXY_", "GIT_CURL_"}

// Env is env without the variables that point git at another repository and the ones
// that add configuration, name a program or turn a check off, and with git's prompts
// off, no terminal prompt and no askpass program, no global or system configuration,
// and no replace object: git reads the configuration of the repository in its folder
// alone.
//
// With p nil, the proxy and the CA bundle the environment names stay as given: the wall
// gives git its proxy and its certificate authority that way. With p set, for a git on
// this machine, Env drops every variable of a proxy, of the certificates and of git's
// http settings, sets HOME to /dev/null, where no .netrc or configuration file can be,
// and sets git's http.proxy to p.URL, with Basic authentication, and http.sslCAInfo to
// p.CABundle when it is set, through GIT_CONFIG_COUNT, which wins over every
// configuration file.
func Env(env []string, p *Proxy) []string {
	out := make([]string, 0, len(env)+14)
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		if slices.Contains(repositoryVariables, name) || slices.Contains(droppedVariables, name) ||
			slices.ContainsFunc(droppedPrefixes, func(pre string) bool { return strings.HasPrefix(name, pre) }) {
			continue
		}
		if p != nil && (slices.Contains(hostVariables, name) ||
			slices.ContainsFunc(hostPrefixes, func(pre string) bool { return strings.HasPrefix(name, pre) })) {
			continue
		}
		out = append(out, kv)
	}
	out = append(out, "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=", "SSH_ASKPASS=",
		"GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1", "GIT_NO_REPLACE_OBJECTS=1")
	if p == nil {
		return out
	}
	config := [][2]string{{"http.proxy", p.URL}, {"http.proxyAuthMethod", "basic"}}
	if p.CABundle != "" {
		config = append(config, [2]string{"http.sslCAInfo", p.CABundle})
	}
	out = append(out, "HOME="+os.DevNull, "GIT_CONFIG_COUNT="+strconv.Itoa(len(config)))
	for i, kv := range config {
		n := strconv.Itoa(i)
		out = append(out, "GIT_CONFIG_KEY_"+n+"="+kv[0], "GIT_CONFIG_VALUE_"+n+"="+kv[1])
	}
	return out
}

// Hide is text with the password of p's URL printed as (hidden) wherever it appears;
// text as it is for a nil p or a URL with no password.
func Hide(text string, p *Proxy) string {
	if p == nil {
		return text
	}
	u, err := url.Parse(p.URL)
	if err != nil || u.User == nil {
		return text
	}
	password, ok := u.User.Password()
	if !ok || password == "" {
		return text
	}
	return strings.ReplaceAll(text, password, "(hidden)")
}
