// Package group validates the groups upstreams join within an endpoint.
package group

import (
	"fmt"
)

// MaxLen is the maximum length of a group, in bytes.
const MaxLen = 64

// Validate returns an error if the group can't be used as a routing key.
//
// A group is addressed by a '/_g/<group>/' request path prefix and registered
// with the cluster alongside its endpoint ID, so it's limited to the
// unreserved URL characters (RFC 3986) to keep both unambiguous. '.' and '..'
// are excluded as they aren't path segments a request can address.
//
// An empty group is valid and means no group.
func Validate(group string) error {
	if len(group) > MaxLen {
		return fmt.Errorf("group exceeds %d bytes", MaxLen)
	}
	if group == "." || group == ".." {
		return fmt.Errorf("group cannot be %q", group)
	}
	for _, r := range group {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '-', r == '_', r == '.', r == '~':
		default:
			return fmt.Errorf(
				"group contains %q; limited to letters, digits, '-', '_', '.' and '~'",
				r,
			)
		}
	}
	return nil
}
