package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// Tests the default configuration is valid.
func TestConfig_Default(t *testing.T) {
	conf := Default()
	assert.NoError(t, conf.Validate())
}

// Tests a port's group is validated up front, rather than when connecting.
func TestPortConfig_ValidateGroup(t *testing.T) {
	port := PortConfig{Addr: "3000", EndpointID: "my-endpoint", Group: "branch-1"}
	assert.NoError(t, port.Validate())

	port.Group = "branch 1"
	assert.ErrorContains(t, port.Validate(), "group")
}
