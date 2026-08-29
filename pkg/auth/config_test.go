package auth

import (
	"testing"

	"github.com/spf13/pflag"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConfig_Load(t *testing.T) {
	t.Run("hmac", func(t *testing.T) {
		config := Config{
			HMACSecretKey: "my-secret-key",
		}
		assert.True(t, config.Enabled())

		loaded, err := config.Load(t.Context())
		assert.NoError(t, err)

		assert.Equal(t, []byte("my-secret-key"), loaded.HMACSecretKey)
	})

	t.Run("rsa", func(t *testing.T) {
		config := Config{
			RSAPublicKey: `-----BEGIN RSA PUBLIC KEY-----
MIIBCgKCAQEA+xGZ/wcz9ugFpP07Nspo6U17l0YhFiFpxxU4pTk3Lifz9R3zsIsu
ERwta7+fWIfxOo208ett/jhskiVodSEt3QBGh4XBipyWopKwZ93HHaDVZAALi/2A
+xTBtWdEo7XGUujKDvC2/aZKukfjpOiUI8AhLAfjmlcD/UZ1QPh0mHsglRNCmpCw
mwSXA9VNmhz+PiB+Dml4WWnKW/VHo2ujTXxq7+efMU4H2fny3Se3KYOsFPFGZ1TN
QSYlFuShWrHPtiLmUdPoP6CV2mML1tk+l7DIIqXrQhLUKDACeM5roMx0kLhUWB8P
+0uj1CNlNN4JRZlC7xFfqiMbFRU9Z4N6YwIDAQAB
-----END RSA PUBLIC KEY-----
`,
		}

		assert.True(t, config.Enabled())

		loaded, err := config.Load(t.Context())
		assert.NoError(t, err)

		assert.NotNil(t, loaded.RSAPublicKey)
	})

	t.Run("ecdsa", func(t *testing.T) {
		config := Config{
			ECDSAPublicKey: `-----BEGIN PUBLIC KEY-----
MFkwEwYHKoZIzj0CAQYIKoZIzj0DAQcDQgAEYD54V/vp+54P9DXarYqx4MPcm+HK
RIQzNasYSoRQHQ/6S6Ps8tpMcT+KvIIC8W/e9k0W7Cm72M1P9jU7SLf/vg==
-----END PUBLIC KEY-----
`,
		}

		assert.True(t, config.Enabled())

		loaded, err := config.Load(t.Context())
		assert.NoError(t, err)

		assert.NotNil(t, loaded.ECDSAPublicKey)
	})

	t.Run("jwks", func(t *testing.T) {
		config := Config{
			JWKS: JWKSConfig{
				Endpoint: "http://localhost/.well-known/jwks.json",
			},
		}

		assert.True(t, config.Enabled())

		loaded, err := config.Load(t.Context())
		assert.NoError(t, err)

		assert.NotNil(t, loaded.JWKS.KeyFunc)
	})
}

func TestConfig_EndpointsClaimFlag(t *testing.T) {
	parse := func(t *testing.T, args ...string) Config {
		var config Config
		fs := pflag.NewFlagSet("piko", pflag.ContinueOnError)
		config.RegisterFlags(fs, "upstream")
		require.NoError(t, fs.Parse(args))
		return config
	}

	t.Run("comma separated claims", func(t *testing.T) {
		config := parse(
			t, "--upstream.auth.endpoints-claim=licensee,endpoint_id",
		)
		assert.Equal(
			t, []string{"licensee", "endpoint_id"}, config.EndpointsClaim,
		)
	})

	t.Run("dots are kept within a claim", func(t *testing.T) {
		config := parse(
			t, "--upstream.auth.endpoints-claim=piko.endpoints,endpoint_id",
		)
		assert.Equal(
			t,
			[]string{"piko.endpoints", "endpoint_id"},
			config.EndpointsClaim,
		)
	})

	t.Run("single claim", func(t *testing.T) {
		config := parse(t, "--upstream.auth.endpoints-claim=endpoint_id")
		assert.Equal(t, []string{"endpoint_id"}, config.EndpointsClaim)
	})

	t.Run("unset", func(t *testing.T) {
		config := parse(t)
		assert.Empty(t, config.EndpointsClaim)
	})
}
