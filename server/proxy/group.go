package proxy

import (
	"net/http"
	"net/url"
	"strings"
)

// groupPathPrefix prefixes a request path that addresses a group of upstreams
// within an endpoint, such as '/_g/branch-1/api/orders' which addresses group
// 'branch-1'.
const groupPathPrefix = "/_g/"

// GroupFromRequest returns the upstream group addressed by the request, or an
// empty string if the request doesn't address a group.
//
// Only upstreams that registered themselves in the group serve the request,
// whereas a request without a group is load balanced among every upstream on
// the endpoint (see upstream.Manager.Select).
//
// The prefix addresses Piko rather than the upstream service, so it is
// stripped from the path before the request is proxied to an upstream (see
// stripGroupPath).
func GroupFromRequest(r *http.Request) string {
	group, _ := cutGroupPath(r.URL.EscapedPath())
	if group == "" {
		return ""
	}

	// A group is limited to unreserved characters when it registers, so it is
	// never escaped, though a client may still escape it.
	unescaped, err := url.PathUnescape(group)
	if err != nil {
		return group
	}
	return unescaped
}

// withoutGroupPath returns a shallow copy of the request with the group prefix
// removed from its path, leaving the given request unchanged (like
// http.StripPrefix).
func withoutGroupPath(r *http.Request) *http.Request {
	r2 := new(http.Request)
	*r2 = *r
	r2.URL = new(url.URL)
	*r2.URL = *r.URL
	stripGroupPath(r2.URL)
	return r2
}

// stripGroupPath removes the group prefix from the URL, leaving the path the
// upstream service expects.
func stripGroupPath(u *url.URL) {
	group, path := cutGroupPath(u.EscapedPath())
	if group == "" {
		return
	}

	unescaped, err := url.PathUnescape(path)
	if err != nil {
		// EscapedPath only returns a valid encoding, so this can't happen.
		return
	}
	u.Path = unescaped
	u.RawPath = path
}

// cutGroupPath splits the group prefix from the given escaped path, returning
// the group it addresses and the path to forward to the upstream.
//
// Returns an empty group and the path unchanged if the path doesn't address a
// group.
func cutGroupPath(escapedPath string) (group string, path string) {
	if !strings.HasPrefix(escapedPath, groupPathPrefix) {
		return "", escapedPath
	}

	var found bool
	group, path, found = strings.Cut(escapedPath[len(groupPathPrefix):], "/")
	if group == "" {
		// '/_g/' addresses no group, so treat it as an ordinary path.
		return "", escapedPath
	}
	if !found {
		// '/_g/branch-1' addresses the root of the group.
		return group, "/"
	}
	return group, "/" + path
}
