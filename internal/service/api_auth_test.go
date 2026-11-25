package service

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/go-jose/go-jose/v4"
	"mobile-use/internal/wire"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestOIDCDiscoveryAndAccessTokenValidation(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	jwk := jose.JSONWebKey{Key: &key.PublicKey, KeyID: "test", Algorithm: "ES256", Use: "sig"}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := "https://" + r.Host
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			writeJSON(w, 200, map[string]any{"issuer": origin, "authorization_endpoint": origin + "/authorize", "token_endpoint": origin + "/token", "jwks_uri": origin + "/keys", "id_token_signing_alg_values_supported": []string{"ES256"}})
		case "/keys":
			writeJSON(w, 200, jose.JSONWebKeySet{Keys: []jose.JSONWebKey{jwk}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	ctx := oidc.ClientContext(t.Context(), server.Client())
	audience := "https://mobile-use.test/mcp"
	auth, err := NewAuth(ctx, server.URL, audience, "console", nil)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.ES256, Key: jose.JSONWebKey{Key: key, KeyID: "test"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, field string
		value       any
		valid       bool
	}{
		{name: "agent token", valid: true},
		{name: "wrong audience", field: "aud", value: "other-service"},
		{name: "id token", field: "typ", value: "ID"},
		{name: "expired", field: "exp", value: time.Now().Add(-time.Minute).Unix()},
		{name: "missing scope", field: "scope", value: "openid"},
		{name: "unknown role", field: "mobile_use_role", value: "root"},
	} {
		t.Run(test.name, func(t *testing.T) {
			claims := map[string]any{"iss": server.URL, "aud": audience, "sub": "approved-agent", "typ": "Bearer", "mobile_use_role": "agent", "mobile_use_workspace": "team", "scope": "openid mobile-use", "exp": time.Now().Add(time.Minute).Unix()}
			if test.field != "" {
				claims[test.field] = test.value
			}
			signed, err := signer.Sign([]byte(wire.Marshal(claims)))
			if err != nil {
				t.Fatal(err)
			}
			token, err := signed.CompactSerialize()
			if err != nil {
				t.Fatal(err)
			}
			actor, err := auth.identity(ctx, token)
			if test.valid && (err != nil || actor.Workspace != "team" || actor.Role != wire.Agent) {
				t.Fatalf("valid token rejected: %v", err)
			}
			if !test.valid && err == nil {
				t.Fatal("invalid token accepted")
			}
		})
	}
}
