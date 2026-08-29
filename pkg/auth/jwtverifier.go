package auth

import (
	"crypto/ecdsa"
	"crypto/rsa"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// DefaultEndpointsClaim is the JWT claim the verifier reads the permitted
// endpoints from when none is configured.
var DefaultEndpointsClaim = []string{"piko.endpoints"}

type PikoClaims struct {
	Endpoints []string `json:"endpoints"`
}

type JWTClaims struct {
	jwt.RegisteredClaims
	Piko PikoClaims `json:"piko"`
}

// JWTVerifier verifies client JWT tokens.
type JWTVerifier struct {
	hmacSecretKey  []byte
	rsaPublicKey   *rsa.PublicKey
	ecdsaPublicKey *ecdsa.PublicKey
	keyFunc        jwt.Keyfunc

	audience string
	issuer   string

	disableDisconnectOnExpiry bool
	requireEndpoints          bool

	// endpointsClaim are the claims holding the permitted endpoints, each
	// optionally dot-notated (e.g. ["endpoint_id", "piko.endpoints"]). The
	// endpoints found at each are combined.
	endpointsClaim []string

	// methods contains the valid JWT methods, which depends on the
	// verification keys configured.
	methods []string
}

func NewJWTVerifier(conf *LoadedConfig) *JWTVerifier {
	endpointsClaim := conf.EndpointsClaim
	if len(endpointsClaim) == 0 {
		endpointsClaim = DefaultEndpointsClaim
	}

	v := &JWTVerifier{
		audience:                  conf.Audience,
		issuer:                    conf.Issuer,
		disableDisconnectOnExpiry: conf.DisableDisconnectOnExpiry,
		requireEndpoints:          conf.RequireEndpoints,
		endpointsClaim:            endpointsClaim,
	}

	if len(conf.HMACSecretKey) > 0 {
		v.hmacSecretKey = conf.HMACSecretKey
		v.methods = append(v.methods, []string{"HS256", "HS384", "HS512"}...)
	}
	if conf.RSAPublicKey != nil {
		v.rsaPublicKey = conf.RSAPublicKey
		v.methods = append(v.methods, []string{"RS256", "RS384", "RS512"}...)
	}
	if conf.ECDSAPublicKey != nil {
		v.ecdsaPublicKey = conf.ECDSAPublicKey
		v.methods = append(v.methods, []string{"ES256", "ES384", "ES512"}...)
	}
	if conf.JWKS != nil {
		v.keyFunc = conf.JWKS.KeyFunc
	}

	return v
}

func (v *JWTVerifier) Verify(tokenString string) (*Token, error) {
	claims := jwt.MapClaims{}

	opts := []jwt.ParserOption{
		jwt.WithValidMethods(v.methods),
	}
	if v.audience != "" {
		opts = append(opts, jwt.WithAudience(v.audience))
	}
	if v.issuer != "" {
		opts = append(opts, jwt.WithIssuer(v.issuer))
	}
	token, err := jwt.ParseWithClaims(
		tokenString,
		claims,
		func(token *jwt.Token) (any, error) {
			// keyFunc (JWKS) takes precedence over the other ways of verification.
			if v.keyFunc != nil {
				return v.keyFunc(token)
			}

			switch token.Method.Alg() {
			case "HS256":
				fallthrough
			case "HS384":
				fallthrough
			case "HS512":
				return v.hmacSecretKey, nil
			case "RS256":
				fallthrough
			case "RS384":
				fallthrough
			case "RS512":
				return v.rsaPublicKey, nil
			case "ES256":
				fallthrough
			case "ES384":
				fallthrough
			case "ES512":
				return v.ecdsaPublicKey, nil
			default:
				return nil, fmt.Errorf("unsupported algorithm: %s", token.Method.Alg())
			}
		},
		opts...,
	)
	if err != nil {
		if errors.Is(err, jwt.ErrTokenExpired) {
			return nil, ErrExpiredToken
		}
		return nil, ErrInvalidToken
	}
	if !token.Valid {
		return nil, ErrInvalidToken
	}

	// Endpoints from every configured claim are combined, so a token may be
	// scoped by more than one claim (such as while migrating from one claim
	// to another).
	var endpoints []string
	for _, path := range v.endpointsClaim {
		for _, endpoint := range extractEndpoints(claims, path) {
			if !slices.Contains(endpoints, endpoint) {
				endpoints = append(endpoints, endpoint)
			}
		}
	}

	// When required, reject tokens that don't scope themselves to specific
	// endpoints. Otherwise an unscoped token would be permitted on every
	// endpoint (see Token.EndpointPermitted).
	if v.requireEndpoints && len(endpoints) == 0 {
		return nil, fmt.Errorf(
			"%w: %s", ErrMissingEndpoints,
			strings.Join(v.endpointsClaim, ", "),
		)
	}

	// Discard the expiry if DisableDisconnectOnExpiry (we've already
	// checked whether the token expired, the expiry is used to disconnect
	// the client when the token expires).
	var expiry time.Time
	if !v.disableDisconnectOnExpiry {
		// GetExpirationTime returns (nil, nil) when there is no 'exp' claim.
		if exp, err := claims.GetExpirationTime(); err == nil && exp != nil {
			expiry = exp.Time
		}
	}
	return &Token{
		Expiry:    expiry,
		Endpoints: endpoints,
	}, nil
}

// extractEndpoints walks the given claim path and coerces the value found
// there into a list of endpoint IDs.
//
// The value may be a JSON array of strings (e.g. "piko.endpoints":
// ["a", "b"]) or a single string (e.g. "endpoint_id": "a"). A missing path,
// or any other type, yields no endpoints.
//
// Each value is reduced to an endpoint ID by endpointIDFromClaim.
func extractEndpoints(claims jwt.MapClaims, path string) []string {
	var cur any = map[string]any(claims)
	for {
		key, rest, nested := strings.Cut(path, ".")
		obj, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur, ok = obj[key]
		if !ok {
			return nil
		}
		if !nested {
			break
		}
		path = rest
	}

	var values []string
	switch val := cur.(type) {
	case string:
		values = []string{val}
	case []string:
		values = val
	case []any:
		values = make([]string, 0, len(val))
		for _, item := range val {
			if s, ok := item.(string); ok {
				values = append(values, s)
			}
		}
	default:
		return nil
	}

	endpoints := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		endpoints = append(endpoints, endpointIDFromClaim(value))
	}
	return endpoints
}

// endpointIDFromClaim reduces a claim value to an endpoint ID.
//
// A token may name an endpoint by the hostname it is reached on, such as
// "my-endpoint.example.com", whereas the endpoint ID is only the
// bottom-level domain "my-endpoint". This mirrors how Piko takes the
// endpoint ID from a request's Host header (see
// proxy.EndpointIDFromRequest).
//
// A value with no domain, such as "my-endpoint", is used as-is.
func endpointIDFromClaim(value string) string {
	name, _, found := strings.Cut(value, ".")
	if !found || name == "" {
		return value
	}
	return name
}

var _ Verifier = &JWTVerifier{}
