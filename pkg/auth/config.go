package auth

import (
	"context"
	"crypto/ecdsa"
	"crypto/rsa"
	"fmt"

	"github.com/golang-jwt/jwt/v5"
	"github.com/spf13/pflag"
)

// Config configures how to verify client JWT tokens.
type Config struct {
	// HMACSecretKey is the secret key to authenticate HMAC endpoint
	// connection JWTs.
	HMACSecretKey string `json:"hmac_secret_key" yaml:"hmac_secret_key"`

	// RSAPublicKey is the public key to authenticate RSA endpoint
	// connection JWTs.
	RSAPublicKey string `json:"rsa_public_key" yaml:"rsa_public_key"`

	// ECDSAPublicKey is the public key to authenticate ECDSA endpoint
	// connection JWTs.
	ECDSAPublicKey string `json:"ecdsa_public_key" yaml:"ecdsa_public_key"`

	// Audience is the required 'aud' claim of the authenticated JWTs.
	//
	// If not given the 'aud' claim will be ignored.
	Audience string `json:"audience" yaml:"audience"`

	// Issuer is the required 'iss' claim of the authenticated JWTs.
	//
	// If not given the 'iss' claim will be ignored.
	Issuer string `json:"issuer" yaml:"issuer"`

	// DisableDisconnectOnExpiry disables disconnecting the client when their
	// token expires.
	//
	// Piko still verifies the token expiry when the client first connects.
	DisableDisconnectOnExpiry bool `json:"disable_disconnect_on_expiry" yaml:"disable_disconnect_on_expiry"`

	// EndpointsClaim is the dot-notation path into the JWT claims that holds
	// the endpoints the token is permitted to access.
	//
	// Defaults to 'piko.endpoints' when empty. Set it to match where your
	// token issuer puts the endpoints, e.g. 'endpoint_id' for a root-level
	// claim. The value may be an array of strings or a single string.
	EndpointsClaim string `json:"endpoints_claim" yaml:"endpoints_claim"`

	// RequireEndpoints rejects tokens that don't include a non-empty
	// endpoints claim (see EndpointsClaim).
	//
	// By default a token with no endpoints claim is permitted to access all
	// endpoints. When enabled, every token must scope itself to one or more
	// endpoints, so an unscoped token can never connect.
	RequireEndpoints bool `json:"require_endpoints" yaml:"require_endpoints"`

	// JWKS is the JSON Web Key Set to use for verifying JWTs.
	//
	// If provided, it will take precedence over the other keys.
	JWKS JWKSConfig `json:"jwks" yaml:"jwks"`
}

// LoadedConfig is the same as Config except it parses the RSA, ECDSA keys and JWKS.
type LoadedConfig struct {
	HMACSecretKey             []byte
	RSAPublicKey              *rsa.PublicKey
	ECDSAPublicKey            *ecdsa.PublicKey
	Audience                  string
	Issuer                    string
	DisableDisconnectOnExpiry bool
	EndpointsClaim            string
	RequireEndpoints          bool
	JWKS                      *LoadedJWKS
}

// Enabled returns whether authentication is enabled.
//
// It is enabled when at least one verification key is configured.
func (c *Config) Enabled() bool {
	return c.HMACSecretKey != "" || c.RSAPublicKey != "" || c.ECDSAPublicKey != "" || c.JWKS.Endpoint != ""
}

func (c *Config) Load(ctx context.Context) (*LoadedConfig, error) {
	config := LoadedConfig{
		HMACSecretKey:             []byte(c.HMACSecretKey),
		Audience:                  c.Audience,
		Issuer:                    c.Issuer,
		DisableDisconnectOnExpiry: c.DisableDisconnectOnExpiry,
		EndpointsClaim:            c.EndpointsClaim,
		RequireEndpoints:          c.RequireEndpoints,
	}

	if c.RSAPublicKey != "" {
		rsaPublicKey, err := jwt.ParseRSAPublicKeyFromPEM(
			[]byte(c.RSAPublicKey),
		)
		if err != nil {
			return nil, fmt.Errorf("parse rsa public key: %w", err)
		}
		config.RSAPublicKey = rsaPublicKey
	}
	if c.ECDSAPublicKey != "" {
		ecdsaPublicKey, err := jwt.ParseECPublicKeyFromPEM(
			[]byte(c.ECDSAPublicKey),
		)
		if err != nil {
			return nil, fmt.Errorf("parse ecdsa public key: %w", err)
		}
		config.ECDSAPublicKey = ecdsaPublicKey
	}

	if c.JWKS.Endpoint != "" {
		loadedJWKS, err := c.JWKS.Load(ctx)
		if err != nil {
			return nil, fmt.Errorf("load JWKS configuration: %w", err)
		}

		config.JWKS = loadedJWKS

		// Avoid accidental misconfiguration by not allowing JWKS to be
		// set together with other verification keys.
		if c.HMACSecretKey != "" || c.RSAPublicKey != "" || c.ECDSAPublicKey != "" {
			return nil, fmt.Errorf("no other verification key can be set when JWKS.Endpoint is set")
		}
	}

	return &config, nil
}

func (c *Config) RegisterFlags(fs *pflag.FlagSet, prefix string) {
	prefix += ".auth."

	fs.StringVar(
		&c.HMACSecretKey,
		prefix+"hmac-secret-key",
		c.HMACSecretKey,
		`
Secret key to authenticate HMAC endpoint connection JWTs.`,
	)
	fs.StringVar(
		&c.RSAPublicKey,
		prefix+"rsa-public-key",
		c.RSAPublicKey,
		`
Public key to authenticate RSA endpoint connection JWTs.`,
	)
	fs.StringVar(
		&c.ECDSAPublicKey,
		prefix+"ecdsa-public-key",
		c.ECDSAPublicKey,
		`
Public key to authenticate ECDSA endpoint connection JWTs.`,
	)
	fs.StringVar(
		&c.Audience,
		prefix+"audience",
		c.Audience,
		`
Audience of endpoint connection JWT  to verify.

If given the JWT 'aud' claim must match the given audience. Otherwise it
is ignored.`,
	)
	fs.StringVar(
		&c.Issuer,
		prefix+"issuer",
		c.Issuer,
		`
Issuer of endpoint connection JWT  to verify.

If given the JWT 'iss' claim must match the given issuer. Otherwise it
is ignored.`,
	)
	fs.BoolVar(
		&c.DisableDisconnectOnExpiry,
		prefix+"disable-disconnect-on-expiry",
		c.DisableDisconnectOnExpiry,
		`
Disables disconnecting the client when their token expires.

Piko still verifies the token expiry when the client first connects.`,
	)
	fs.StringVar(
		&c.EndpointsClaim,
		prefix+"endpoints-claim",
		c.EndpointsClaim,
		`
Dot-notation path into the JWT claims holding the permitted endpoints.

Defaults to 'piko.endpoints'. Set it to match where your token issuer puts
the endpoints, such as 'endpoint_id' for a root-level claim. The value may be
an array of strings or a single string.`,
	)
	fs.BoolVar(
		&c.RequireEndpoints,
		prefix+"require-endpoints",
		c.RequireEndpoints,
		`
Reject tokens that don't include a non-empty 'piko.endpoints' claim.

By default a token with no endpoints claim may access all endpoints. When
enabled, every token must scope itself to one or more endpoints, so an
unscoped token can never connect.`,
	)

	c.JWKS.RegisterFlags(fs, prefix)
}
