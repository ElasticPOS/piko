package group

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestValidate(t *testing.T) {
	tests := []struct {
		group string
		want  bool
	}{
		// An upstream doesn't have to join a group.
		{group: "", want: true},
		{group: "branch-1", want: true},
		{group: "Branch_1.a~b", want: true},
		// A group is part of a route, so anything that would make the route
		// ambiguous is rejected rather than sanitized.
		{group: "branch/1", want: false},
		{group: "branch 1", want: false},
		{group: "branch%2F1", want: false},
		{group: "branch\r1", want: false},
		{group: "br\u00e4nch", want: false},
		{group: ".", want: false},
		{group: "..", want: false},
		{group: strings.Repeat("a", MaxLen), want: true},
		{group: strings.Repeat("a", MaxLen+1), want: false},
	}
	for _, test := range tests {
		assert.Equal(t, test.want, Validate(test.group) == nil, test.group)
	}
}
