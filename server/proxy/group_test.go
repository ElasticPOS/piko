package proxy

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGroupFromRequest(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{path: "/", want: ""},
		{path: "/foo/bar", want: ""},
		{path: "/_g/branch-1/foo/bar", want: "branch-1"},
		// A group without a path addresses the root of the group.
		{path: "/_g/branch-1", want: "branch-1"},
		{path: "/_g/branch-1/", want: "branch-1"},
		// Neither names a group, so both are ordinary paths.
		{path: "/_g/", want: ""},
		{path: "/_g//foo", want: ""},
		// Only a prefix addresses a group.
		{path: "/foo/_g/branch-1/bar", want: ""},
		// A group is never escaped when it registers, though a client may
		// still escape it.
		{path: "/_g/branch%2D1/foo", want: "branch-1"},
		// An escaped separator can't forge a group, as a registered group
		// can never contain one.
		{path: "/_g/branch%2F1/foo", want: "branch/1"},
	}
	for _, test := range tests {
		r, err := http.NewRequest(http.MethodGet, "http://example.com"+test.path, nil)
		require.NoError(t, err)

		assert.Equal(t, test.want, GroupFromRequest(r), test.path)
	}
}

func TestStripGroupPath(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{path: "/_g/branch-1/foo/bar", want: "/foo/bar"},
		{path: "/_g/branch-1", want: "/"},
		{path: "/_g/branch-1/", want: "/"},
		// Paths that don't address a group are left alone.
		{path: "/foo/bar", want: "/foo/bar"},
		{path: "/_g/", want: "/_g/"},
		// The remaining path keeps its encoding, as that's what the upstream
		// service expects.
		{path: "/_g/branch-1/foo%20bar", want: "/foo%20bar"},
		{path: "/_g/branch-1/foo%2Fbar", want: "/foo%2Fbar"},
	}
	for _, test := range tests {
		u, err := url.Parse("http://example.com" + test.path)
		require.NoError(t, err)

		stripGroupPath(u)

		assert.Equal(t, test.want, u.EscapedPath(), test.path)
	}
}

// Tests the group prefix is stripped from a copy of the request, leaving the
// original request (which the access log reads) with the client's path.
func TestWithoutGroupPath(t *testing.T) {
	r, err := http.NewRequest(http.MethodGet, "http://example.com/_g/branch-1/foo", nil)
	require.NoError(t, err)

	stripped := withoutGroupPath(r)

	assert.Equal(t, "/foo", stripped.URL.Path)
	assert.Equal(t, "/_g/branch-1/foo", r.URL.Path)
}
