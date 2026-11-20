package runtimeinstall

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"
)

// NpmRegistry is the npm registry that publishes Claude Code and Codex.
const NpmRegistry = "https://registry.npmjs.org"

// npmPackage is where npm publishes a runtime: tags is the package whose dist-tag latest
// is the newest version, and platform returns the package and the version of the
// document of a version's build for cpu, x64 or arm64.
type npmPackage struct {
	tags     string
	platform func(version, cpu string) (pkg, docVersion string)
}

// npmPackages are the npm packages of each runtime that [NpmFetch] installs. Claude Code
// publishes each platform's build as a separate package,
// @anthropic-ai/claude-code-linux-x64 and -arm64, at the same version. Codex publishes
// it as a version of @openai/codex itself, <version>-linux-x64 and -linux-arm64.
var npmPackages = map[string]npmPackage{
	"claude": {tags: "@anthropic-ai/claude-code", platform: func(version, cpu string) (string, string) {
		return "@anthropic-ai/claude-code-linux-" + cpu, version
	}},
	"codex": {tags: "@openai/codex", platform: func(version, cpu string) (string, string) {
		return "@openai/codex", version + "-linux-" + cpu
	}},
}

// Runtimes are the runtimes that [Install] installs, by Forager's descriptor names, in
// alphabetical order: those of [npmPackages]. The runtime cache of a registry holds npm's
// tarballs of the same runtimes.
func Runtimes() []string {
	return slices.Sorted(maps.Keys(npmPackages))
}

// npmCPU is npm's name of each architecture qory installs for.
var npmCPU = map[string]string{"amd64": "x64", "arm64": "arm64"}

// signedName is what npm signs for version of runtime's build for linux/arch,
// <package>@<version>, see [npmPackages]; false for a runtime or an architecture that
// qory does not install.
func signedName(runtime, version, arch string) (string, bool) {
	pkg, ok := npmPackages[runtime]
	cpu, known := npmCPU[arch]
	if !ok || !known {
		return "", false
	}
	platform, docVersion := pkg.platform(version, cpu)
	return platform + "@" + docVersion, true
}

// The most bytes of npm's documents that NpmFetch reads.
const (
	maxDistTags = 64 << 10
	maxVersion  = 4 << 20
)

// NpmFetch is a [Fetch] from the npm registry at base, such as [NpmRegistry], through
// client, for the platform linux/arch. It reads, in this order, the dist-tags of the
// runtime's package, whose latest is the version, unless a version is pinned; the
// document of that version's build for the platform, see [npmPackages], whose
// dist.integrity is npm's sha512 checksum and whose dist.tarball is the tarball; and the
// tarball, when Install opens it. A 404 of a pinned version's document is a
// [*NotPublishedError]. The tarball must be on base's host, and no request follows a
// redirect to another host. Install checks the tarball against the checksum before it
// extracts anything.
func NpmFetch(client *http.Client, base, arch string) Fetch {
	return func(ctx context.Context, runtime, pinned string) (Release, error) {
		pkg, ok := npmPackages[runtime]
		if !ok {
			return Release{}, fmt.Errorf("Install %s some other way\nqory installs only %s from npm", runtime, strings.Join(Runtimes(), " and "))
		}
		cpu, ok := npmCPU[arch]
		if !ok {
			return Release{}, fmt.Errorf("Run on an amd64 or arm64 machine\nnpm has %s for linux/amd64 and linux/arm64, and this machine is %s", runtime, arch)
		}
		root, err := url.Parse(strings.TrimSuffix(base, "/"))
		if err != nil || root.Host == "" || root.Scheme != "https" && !(root.Scheme == "http" && loopback(root.Hostname())) {
			return Release{}, fmt.Errorf("Set the npm registry to an https URL\nThe npm registry's address is not an https URL:\n  %q", base)
		}
		n := &npm{client: sameHost(client, root), root: root, runtime: runtime}
		version := pinned
		if version == "" {
			var tags map[string]string
			if err := n.document(ctx, "/-/package/"+pkg.tags+"/dist-tags", maxDistTags, "the dist-tags of "+pkg.tags, &tags); err != nil {
				return Release{}, err
			}
			version = tags["latest"]
			if !versionShape.MatchString(version) {
				return Release{}, fmt.Errorf(textRelease+"The dist-tag latest of %s is %q, and a version is 1 to 64 letters, digits, ., + or -, the first a letter or a digit", pkg.tags, version)
			}
		}
		platform, docVersion := pkg.platform(version, cpu)
		var doc struct {
			Version string `json:"version"`
			Dist    struct {
				Integrity  string          `json:"integrity"`
				Tarball    string          `json:"tarball"`
				Signatures json.RawMessage `json:"signatures"`
			} `json:"dist"`
		}
		what := platform + " " + docVersion
		if err := n.document(ctx, "/"+platform+"/"+url.PathEscape(docVersion), maxVersion, what, &doc); err != nil {
			var missing *npmNotFound
			if pinned != "" && errors.As(err, &missing) {
				return Release{}, &NotPublishedError{Runtime: runtime, Version: pinned}
			}
			return Release{}, err
		}
		if doc.Version != docVersion {
			return Release{}, fmt.Errorf(textRelease+"The document of %s has the version %q", what, doc.Version)
		}
		tarball, err := url.Parse(doc.Dist.Tarball)
		if err != nil || tarball.Scheme != root.Scheme || tarball.Host != root.Host || tarball.User != nil {
			return Release{}, fmt.Errorf(textRelease+"The tarball of %s is not on %s", what, root.Host)
		}
		// npm may add members to a signature, so the decode ignores them. Install refuses a
		// document with no signatures.
		var sigs []Signature
		if len(doc.Dist.Signatures) > 0 && string(doc.Dist.Signatures) != "null" {
			if sigs, err = decodeSignatures(doc.Dist.Signatures, false); err != nil {
				return Release{}, fmt.Errorf(textRelease+"The dist.signatures of %s\n  are not npm's signatures: %v", what, err)
			}
		}
		rel := Release{
			Version:    version,
			Integrity:  doc.Dist.Integrity,
			Signed:     platform + "@" + docVersion,
			Signatures: sigs,
			Open: func(ctx context.Context) (io.ReadCloser, error) {
				resp, err := n.get(ctx, tarball.String(), "", "the tarball of "+what)
				if err != nil {
					return nil, err
				}
				return resp.Body, nil
			},
		}
		rel.Program, rel.Folder = layout(runtime, arch)
		return rel, nil
	}
}

// npm is the npm registry at root, as one fetch reads it.
type npm struct {
	client  *http.Client
	root    *url.URL
	runtime string
}

// document reads the JSON document at path into v, at most limit bytes.
func (n *npm) document(ctx context.Context, path string, limit int64, what string, v any) error {
	resp, err := n.get(ctx, n.root.String()+path, "application/json", what)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return fmt.Errorf("Run again\nThe read of %s from %s stopped:\n  %w", what, n.root.Host, err)
	}
	if int64(len(body)) > limit {
		return fmt.Errorf(textRelease+"%s has more than %d bytes", upper(what), limit)
	}
	if err := json.Unmarshal(body, v); err != nil {
		return fmt.Errorf(textRelease+"%s is not JSON", upper(what))
	}
	return nil
}

// get sends a GET of u, with Accept set to accept when it is not empty, and returns its
// response with the status 200. A CONNECT that the run's proxy returns 403 or 407 to
// gets the error [npm.noRegistry].
func (n *npm) get(ctx context.Context, u, accept, what string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf(textRelease+"%s has no URL qory can request:\n  %w", upper(what), err)
	}
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	resp, err := send(n.client, req)
	switch {
	case err != nil && ctx.Err() != nil:
		return nil, fmt.Errorf("Run again with more time\nThe run ended before %s returned\n  %s: %w", n.root.Host, what, err)
	case refusedConnect(err):
		return nil, n.noRegistry(connectRefusal(err))
	case err != nil:
		return nil, fmt.Errorf("Check that this machine reaches %s\nThe request of %s failed:\n  %w", n.root.Host, what, err)
	case resp.StatusCode == http.StatusOK:
		return resp, nil
	}
	drain(resp)
	if resp.StatusCode == http.StatusNotFound {
		return nil, &npmNotFound{fmt.Sprintf(textRelease+"%s returned 404: npm has no such %s release", upper(what), n.runtime)}
	}
	return nil, fmt.Errorf("Check that this machine reaches %s\n%s returned %d", n.root.Host, what, resp.StatusCode)
}

// npmNotFound is the error of a request that npm returned 404 to.
type npmNotFound struct{ text string }

func (e *npmNotFound) Error() string { return e.text }

// noRegistry is the error of a run whose proxy returned 403 or 407 to the CONNECT to
// npm's host, which a run with no registry reaches only through its proxy: the run's
// policy does not allow npm, or the proxy's password is wrong. Its first line is the
// fix; the second is the cause.
func (n *npm) noRegistry(c *connectRefused) error {
	return fmt.Errorf("Set up the instance's registry for this machine, or allow %s in the run's policy.\n"+
		"The run's proxy returned %d to the CONNECT to %s.", n.root.Hostname(), c.status, c.host)
}

// errOtherHost is the error of a redirect to a host or a scheme other than npm's.
var errOtherHost = errors.New("a redirect leads to another host or scheme")

// sameHost is client with a redirect policy that follows a redirect only to the scheme
// and host of root.
func sameHost(client *http.Client, root *url.URL) *http.Client {
	c := *client
	c.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if req.URL.Host != root.Host || req.URL.Scheme != root.Scheme || len(via) >= 10 {
			return errOtherHost
		}
		return nil
	}
	return &c
}
