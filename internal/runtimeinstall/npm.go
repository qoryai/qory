package runtimeinstall

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// NpmRegistry is the npm registry that publishes Claude Code.
const NpmRegistry = "https://registry.npmjs.org"

// npmPackages are the npm packages of each runtime that [NpmFetch] installs: the package
// whose dist-tag latest is the newest version, and the prefix of the package of each
// platform, which ends in x64 or arm64. The platform package's tarball has the program
// at package/<runtime>.
var npmPackages = map[string]struct{ tags, platform string }{
	"claude": {tags: "@anthropic-ai/claude-code", platform: "@anthropic-ai/claude-code-linux-"},
}

// The most bytes of npm's documents that NpmFetch reads.
const (
	maxDistTags = 64 << 10
	maxVersion  = 4 << 20
)

// NpmFetch is a [Fetch] from the npm registry at base, such as [NpmRegistry], through
// client, for the platform linux/arch. It reads, in this order, the dist-tags of the
// runtime's package, whose latest is the version; the document of that version of the
// platform's package, whose dist.integrity is npm's sha512 checksum and whose
// dist.tarball is the tarball; and the tarball, when Install opens it. The tarball must
// be on base's host, and no request follows a redirect to another host. Install checks
// the tarball against the checksum before it extracts anything.
func NpmFetch(client *http.Client, base, arch string) Fetch {
	return func(ctx context.Context, runtime string) (Release, error) {
		pkg, ok := npmPackages[runtime]
		if !ok {
			return Release{}, fmt.Errorf("install %s some other way: qory installs only claude from npm", runtime)
		}
		cpu, ok := map[string]string{"amd64": "x64", "arm64": "arm64"}[arch]
		if !ok {
			return Release{}, fmt.Errorf("run on an amd64 or arm64 machine: npm has %s for linux/amd64 and linux/arm64, and this machine is %s", runtime, arch)
		}
		root, err := url.Parse(strings.TrimSuffix(base, "/"))
		if err != nil || root.Host == "" || root.Scheme != "https" && !(root.Scheme == "http" && loopback(root.Hostname())) {
			return Release{}, fmt.Errorf("set the npm registry to an https URL: %q is not one", base)
		}
		n := &npm{client: sameHost(client, root.Host), root: root, runtime: runtime}
		var tags map[string]string
		if err := n.document(ctx, "/-/package/"+pkg.tags+"/dist-tags", maxDistTags, "the dist-tags of "+pkg.tags, &tags); err != nil {
			return Release{}, err
		}
		version := tags["latest"]
		if !versionShape.MatchString(version) {
			return Release{}, fmt.Errorf(textRelease+"the dist-tag latest of %s is %q, and a version is 1 to 64 letters, digits, ., + or -, the first a letter or a digit", pkg.tags, version)
		}
		platform := pkg.platform + cpu
		var doc struct {
			Version string `json:"version"`
			Dist    struct {
				Integrity string `json:"integrity"`
				Tarball   string `json:"tarball"`
			} `json:"dist"`
		}
		what := platform + " " + version
		if err := n.document(ctx, "/"+platform+"/"+url.PathEscape(version), maxVersion, what, &doc); err != nil {
			return Release{}, err
		}
		if doc.Version != version {
			return Release{}, fmt.Errorf(textRelease+"the document of %s has the version %q", what, doc.Version)
		}
		tarball, err := url.Parse(doc.Dist.Tarball)
		if err != nil || tarball.Scheme != root.Scheme || tarball.Host != root.Host || tarball.User != nil {
			return Release{}, fmt.Errorf(textRelease+"the tarball of %s is not on %s", what, root.Host)
		}
		return Release{
			Version:   version,
			Integrity: doc.Dist.Integrity,
			Open: func(ctx context.Context) (io.ReadCloser, error) {
				resp, err := n.get(ctx, tarball.String(), "", "the tarball of "+what)
				if err != nil {
					return nil, err
				}
				return resp.Body, nil
			},
		}, nil
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
		return fmt.Errorf("run again: the read of %s from %s stopped: %w", what, n.root.Host, err)
	}
	if int64(len(body)) > limit {
		return fmt.Errorf(textRelease+"%s has more than %d bytes", what, limit)
	}
	if err := json.Unmarshal(body, v); err != nil {
		return fmt.Errorf(textRelease+"%s is not JSON", what)
	}
	return nil
}

// get sends a GET of u, with Accept set to accept when it is not empty, and returns its
// answer of 200.
func (n *npm) get(ctx context.Context, u, accept, what string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf(textRelease+"%s has no URL qory can request: %w", what, err)
	}
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	resp, err := n.client.Do(req)
	switch {
	case err != nil && ctx.Err() != nil:
		return nil, fmt.Errorf("run again with more time: the run ended before %s answered %s: %w", n.root.Host, what, err)
	case err != nil:
		return nil, fmt.Errorf("check that this machine reaches %s: the request of %s failed: %w", n.root.Host, what, err)
	case resp.StatusCode == http.StatusOK:
		return resp, nil
	}
	drain(resp)
	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf(textRelease+"%s returned 404: npm has no such %s release", what, n.runtime)
	}
	return nil, fmt.Errorf("check that this machine reaches %s: %s returned %d", n.root.Host, what, resp.StatusCode)
}

// errOtherHost is the error of a redirect to a host other than npm's.
var errOtherHost = errors.New("a redirect leads to another host")

// sameHost is client with a redirect policy that follows a redirect only on host.
func sameHost(client *http.Client, host string) *http.Client {
	c := *client
	c.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if req.URL.Host != host || len(via) >= 10 {
			return errOtherHost
		}
		return nil
	}
	return &c
}
