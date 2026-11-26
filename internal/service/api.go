package service

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"mobile-use/internal/wire"
)

type Principal struct {
	Token string
	Actor wire.Actor
}

type Auth struct {
	Static                []Principal
	Issuer                string
	Audience              string
	ConsoleClient         string
	JWKS                  *oidc.IDTokenVerifier
	AuthorizationEndpoint string
	TokenEndpoint         string
}

func NewAuth(ctx context.Context, issuer, audience, client string, static []Principal) (*Auth, error) {
	auth := &Auth{Static: static, Issuer: issuer, Audience: audience, ConsoleClient: client}
	if issuer == "" {
		return auth, nil
	}
	if !strings.HasPrefix(issuer, "https://") || audience == "" {
		return nil, fmt.Errorf("OIDC requires an HTTPS issuer and an access-token audience")
	}
	provider, err := oidc.NewProvider(ctx, issuer)
	if err != nil {
		return nil, err
	}
	endpoint := provider.Endpoint()
	auth.JWKS = provider.Verifier(&oidc.Config{ClientID: audience})
	auth.AuthorizationEndpoint, auth.TokenEndpoint = endpoint.AuthURL, endpoint.TokenURL
	return auth, nil
}

func (a *Auth) identity(ctx context.Context, token string) (wire.Actor, error) {
	if len(token) < 16 || len(token) > 16384 {
		return wire.Actor{}, errUnauthorized
	}
	for _, principal := range a.Static {
		if subtle.ConstantTimeCompare([]byte(principal.Token), []byte(token)) == 1 {
			return principal.Actor, nil
		}
	}
	if a.JWKS == nil {
		return wire.Actor{}, errUnauthorized
	}
	jwt, err := a.JWKS.Verify(ctx, token)
	if err != nil {
		return wire.Actor{}, errUnauthorized
	}
	var claims struct {
		Workspace       string `json:"mobile_use_workspace"`
		Role            string `json:"mobile_use_role"`
		Type            string `json:"typ"`
		Scope           string `json:"scope"`
		AuthorizedParty string `json:"azp"`
	}
	if err := jwt.Claims(&claims); err != nil || !wire.ValidID(jwt.Subject) || !wire.ValidID(claims.Workspace) ||
		!slices.Contains([]string{wire.Admin, wire.Operator, wire.Owner, wire.Agent, wire.Auditor}, claims.Role) ||
		!slices.Contains([]string{"Bearer", "at+jwt", "access"}, claims.Type) || !slices.Contains(strings.Fields(claims.Scope), "mobile-use") {
		return wire.Actor{}, errUnauthorized
	}
	return wire.Actor{Workspace: claims.Workspace, Subject: jwt.Subject, Role: claims.Role, ExpiresAt: jwt.Expiry}, nil
}

var errUnauthorized = wire.Error("UNAUTHENTICATED", "A valid mobile-use access token is required.", 401)

type actorContextKey struct{}

func ActorFrom(ctx context.Context) (wire.Actor, bool) {
	actor, ok := ctx.Value(actorContextKey{}).(wire.Actor)
	return actor, ok
}

func (a *Auth) Require(next http.Handler, origin string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Fields(r.Header.Get("Authorization"))
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
			a.challenge(w, origin)
			writeError(w, errUnauthorized)
			return
		}
		actor, err := a.identity(r.Context(), parts[1])
		if err != nil {
			a.challenge(w, origin)
			writeError(w, err)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), actorContextKey{}, actor)))
	})
}

func (a *Auth) challenge(w http.ResponseWriter, origin string) {
	challenge := `Bearer realm="mobile-use"`
	if a.Issuer != "" {
		challenge += `, resource_metadata="` + origin + `/.well-known/oauth-protected-resource/mcp", scope="mobile-use"`
	}
	w.Header().Set("WWW-Authenticate", challenge)
}

type HTTPOptions struct {
	Origin        string
	ExtraOrigins  []string
	Auth          *Auth
	MCP           http.Handler
	Console       http.Handler
	Extra         func(*http.ServeMux)
	Video         *VideoService
	LocalExchange bool
	SystemInfo    func(wire.Actor) map[string]any
}

func (s *Service) HTTP(options HTTPOptions) http.Handler {
	router := http.NewServeMux()
	protected := http.NewServeMux()
	if options.Video != nil {
		options.Video.Routes(protected)
	}
	protected.HandleFunc("GET /v1/me", withActor(func(w http.ResponseWriter, r *http.Request, actor wire.Actor) { writeJSON(w, 200, actor) }))
	protected.HandleFunc("GET /v1/system/info", withActor(func(w http.ResponseWriter, r *http.Request, actor wire.Actor) {
		if options.SystemInfo != nil {
			writeJSON(w, 200, options.SystemInfo(actor))
			return
		}
		writeJSON(w, 200, map[string]any{
			"mode":              "remote",
			"origin":            options.Origin,
			"companion_enabled": s.Hub.Companion != nil,
		})
	}))
	protected.HandleFunc("GET /v1/devices", withActor(func(w http.ResponseWriter, r *http.Request, actor wire.Actor) {
		devices, err := s.Store.Devices(r.Context(), actor, time.Now())
		respond(w, 200, devices, err)
	}))
	protected.HandleFunc("GET /v1/devices/{id}", withActor(func(w http.ResponseWriter, r *http.Request, actor wire.Actor) {
		devices, err := s.Store.Devices(r.Context(), actor, time.Now())
		if err != nil {
			writeError(w, err)
			return
		}
		for _, device := range devices {
			if device.ID == r.PathValue("id") {
				writeJSON(w, 200, device)
				return
			}
		}
		writeError(w, wire.ErrNotFound)
	}))
	protected.HandleFunc("PUT /v1/devices/{id}", withActor(func(w http.ResponseWriter, r *http.Request, actor wire.Actor) {
		var request wire.Enrollment
		if !readJSON(w, r, &request) {
			return
		}
		device, stopped, err := s.Store.Enroll(r.Context(), actor, r.PathValue("id"), request, time.Now())
		if err == nil && stopped.Generation > 0 {
			s.stop(r.Context(), stopped)
		}
		respond(w, 200, device, err)
	}))
	protected.HandleFunc("DELETE /v1/devices/{id}", withActor(func(w http.ResponseWriter, r *http.Request, actor wire.Actor) {
		target, err := s.Store.RemoveDevice(r.Context(), actor, r.PathValue("id"), time.Now())
		if err != nil {
			writeError(w, err)
			return
		}
		if target.Session != "" || strings.HasPrefix(target.Connector, "companion.") {
			s.stop(r.Context(), target)
		}
		writeJSON(w, http.StatusOK, map[string]bool{"removed": true})
	}))
	protected.HandleFunc("GET /v1/sessions", withActor(func(w http.ResponseWriter, r *http.Request, actor wire.Actor) {
		sessions, err := s.Store.Sessions(r.Context(), actor)
		respond(w, 200, sessions, err)
	}))
	protected.HandleFunc("POST /v1/sessions", withActor(func(w http.ResponseWriter, r *http.Request, actor wire.Actor) {
		var request wire.StartSession
		if !readJSON(w, r, &request) {
			return
		}
		session, err := s.Store.Start(r.Context(), actor, request, time.Now())
		respond(w, 201, session, err)
	}))
	protected.HandleFunc("GET /v1/sessions/{id}", withActor(func(w http.ResponseWriter, r *http.Request, actor wire.Actor) {
		session, err := s.Store.Session(r.Context(), actor, r.PathValue("id"))
		respond(w, 200, session, err)
	}))
	protected.HandleFunc("POST /v1/sessions/{id}/{verb}", withActor(func(w http.ResponseWriter, r *http.Request, actor wire.Actor) {
		session, err := s.Change(r.Context(), actor, r.PathValue("id"), r.PathValue("verb"))
		respond(w, 200, session, err)
	}))
	protected.HandleFunc("POST /v1/sessions/{id}/operations", withActor(func(w http.ResponseWriter, r *http.Request, actor wire.Actor) {
		var request wire.Operation
		if !readJSON(w, r, &request) {
			return
		}
		result, err := s.Operation(r.Context(), actor, r.PathValue("id"), request)
		respond(w, 200, result, err)
	}))
	protected.HandleFunc("GET /v1/audit", withActor(func(w http.ResponseWriter, r *http.Request, actor wire.Actor) {
		events, err := s.Store.Audit(r.Context(), actor, 100)
		respond(w, 200, events, err)
	}))
	protected.HandleFunc("GET /v1/metrics", withActor(func(w http.ResponseWriter, r *http.Request, actor wire.Actor) {
		if actor.Role != wire.Admin && actor.Role != wire.Auditor {
			writeError(w, wire.ErrForbidden)
			return
		}
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		_, _ = fmt.Fprintf(w, "# TYPE mobile_use_operations_total counter\nmobile_use_operations_total{state=\"admitted\"} %d\nmobile_use_operations_total{state=\"succeeded\"} %d\nmobile_use_operations_total{state=\"failed\"} %d\nmobile_use_operations_total{state=\"unknown\"} %d\n", s.Admitted.Load(), s.Succeeded.Load(), s.Failed.Load(), s.Unknown.Load())
	}))
	router.Handle("/v1/", options.Auth.Require(protected, options.Origin))
	router.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, 200, map[string]bool{"ok": true}) })
	router.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), time.Second)
		defer cancel()
		if s.Store.Ping(ctx) != nil {
			writeJSON(w, 503, map[string]bool{"ready": false})
			return
		}
		writeJSON(w, 200, map[string]bool{"ready": true})
	})
	router.HandleFunc("GET /auth/config", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 200, map[string]any{"issuer": options.Auth.Issuer, "client_id": options.Auth.ConsoleClient, "authorization_endpoint": options.Auth.AuthorizationEndpoint, "token_endpoint": options.Auth.TokenEndpoint, "resource": options.Auth.Audience, "companion_enabled": s.Hub.Companion != nil, "local_exchange": options.LocalExchange})
	})
	if options.Auth.Issuer != "" {
		metadata := func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, 200, map[string]any{"resource": options.Origin + "/mcp", "authorization_servers": []string{options.Auth.Issuer}, "scopes_supported": []string{"mobile-use"}, "bearer_methods_supported": []string{"header"}})
		}
		router.HandleFunc("GET /.well-known/oauth-protected-resource", metadata)
		router.HandleFunc("GET /.well-known/oauth-protected-resource/mcp", metadata)
	}
	if options.MCP != nil {
		router.Handle("/mcp", options.Auth.Require(options.MCP, options.Origin))
	}
	if options.Extra != nil {
		options.Extra(router)
	}
	if options.Console != nil {
		router.Handle("/", options.Console)
	}
	origins := append([]string{options.Origin}, options.ExtraOrigins...)
	return perimeterHosts(router, origins, options.Auth.TokenEndpoint)
}

func perimeter(next http.Handler, origin, tokenEndpoint string) http.Handler {
	return perimeterHosts(next, []string{origin}, tokenEndpoint)
}

func perimeterHosts(next http.Handler, origins []string, tokenEndpoint string) http.Handler {
	var canonicals []*url.URL
	var rawOrigins []string
	hasHTTPS := false
	for _, item := range origins {
		if item == "" {
			continue
		}
		parsed, err := url.Parse(item)
		if err != nil || parsed.Host == "" {
			continue
		}
		canonicals = append(canonicals, parsed)
		rawOrigins = append(rawOrigins, item)
		if parsed.Scheme == "https" {
			hasHTTPS = true
		}
	}
	connect := "'self'"
	if endpoint, err := url.Parse(tokenEndpoint); err == nil && endpoint.Scheme == "https" && endpoint.Host != "" {
		connect += " https://" + endpoint.Host
	}
	csp := "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' data: blob:; media-src 'self' blob:; connect-src " + connect + "; font-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'"
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", csp)
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Cache-Control", "no-store")
		if hasHTTPS {
			w.Header().Set("Strict-Transport-Security", "max-age=31536000")
		}
		hostMatch := false
		for _, canonical := range canonicals {
			if strings.EqualFold(r.Host, canonical.Host) {
				hostMatch = true
				break
			}
		}
		if !hostMatch && !(isLoopbackHost(r.Host) && (r.URL.Path == "/healthz" || r.URL.Path == "/readyz")) {
			writeError(w, wire.Error("INVALID_HOST", "The request Host does not match the configured origin.", 400))
			return
		}
		if supplied := r.Header.Get("Origin"); supplied != "" {
			originMatch := false
			for _, allowed := range rawOrigins {
				if strings.EqualFold(supplied, allowed) {
					originMatch = true
					break
				}
			}
			if !originMatch {
				writeError(w, wire.ErrForbidden)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func isLoopbackHost(raw string) bool {
	host, _, err := net.SplitHostPort(raw)
	if err != nil {
		host = strings.Trim(raw, "[]")
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func withActor(handler func(http.ResponseWriter, *http.Request, wire.Actor)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		actor, ok := ActorFrom(r.Context())
		if !ok {
			writeError(w, errUnauthorized)
			return
		}
		handler(w, r, actor)
	}
}

func readJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	defer r.Body.Close()
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil || decoder.Decode(new(json.RawMessage)) != io.EOF {
		writeError(w, wire.Error("INVALID_JSON", "Send one valid JSON object for this operation.", 400))
		return false
	}
	return true
}

func respond(w http.ResponseWriter, status int, value any, err error) {
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, status, value)
}

func writeError(w http.ResponseWriter, err error) {
	fault := wire.Public(err)
	if fault.Status == 0 {
		fault.Status = 500
	}
	if fault.Code == "INTERNAL" && !errors.Is(err, context.Canceled) {
		slog.Error("HTTP operation failed", "error", err)
	}
	writeJSON(w, fault.Status, map[string]any{"error": fault})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
