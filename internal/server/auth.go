package server

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// Access rules:
//   - The page served by this binary (same origin) gets the API without a token while the server
//     listens on loopback. That is the normal local use.
//   - A page from another origin (for example the frontend hosted on a website) must be on the
//     --allow-origin list AND send the pairing token: "Authorization: Bearer <token>", or
//     ?token=<token> for the WebSocket. Anything else gets 403 and no CORS headers.
//   - The Host header must name this machine (loopback names or the bind address), which stops
//     DNS rebinding.
//   - Listening on a non-loopback address makes the token required for every API call.

// Auth holds the pairing token and the origin allowlist.
type Auth struct {
	Token        string
	AllowOrigins []string // exact origins, e.g. https://example.com
	Loopback     bool     // listening on a loopback address only
	Port         string
	path         string
}

// LoadAuth reads or creates the pairing token in dir/pairing-token.
func LoadAuth(dir string, allow []string, listenAddr string) (*Auth, error) {
	a := &Auth{path: filepath.Join(dir, "pairing-token")}
	for _, o := range allow {
		if o = strings.TrimRight(strings.TrimSpace(o), "/"); o != "" {
			a.AllowOrigins = append(a.AllowOrigins, o)
		}
	}
	host, port, err := net.SplitHostPort(listenAddr)
	if err != nil {
		return nil, err
	}
	a.Port = port
	ip := net.ParseIP(host)
	a.Loopback = host == "localhost" || (ip != nil && ip.IsLoopback())
	if b, err := os.ReadFile(a.path); err == nil && len(strings.TrimSpace(string(b))) >= 32 {
		a.Token = strings.TrimSpace(string(b))
		return a, nil
	}
	return a, a.Reset()
}

// Reset makes a new token; every paired page has to pair again.
func (a *Auth) Reset() error {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return err
	}
	a.Token = hex.EncodeToString(b)
	os.MkdirAll(filepath.Dir(a.path), 0o700)
	return os.WriteFile(a.path, []byte(a.Token+"\n"), 0o600)
}

func (a *Auth) selfOrigin(origin string) bool {
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	h, p := u.Hostname(), u.Port()
	if p == "" {
		p = "80"
	}
	return p == a.Port && loopbackName(h)
}

func loopbackName(h string) bool {
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(strings.Trim(h, "[]"))
	return ip != nil && ip.IsLoopback()
}

func (a *Auth) allowed(origin string) bool {
	for _, o := range a.AllowOrigins {
		if o == origin {
			return true
		}
	}
	return false
}

func (a *Auth) tokenOK(r *http.Request) bool {
	t := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if t == "" {
		t = r.URL.Query().Get("token")
	}
	return t != "" && subtle.ConstantTimeCompare([]byte(t), []byte(a.Token)) == 1
}

// SameOrigin reports whether r comes from the page this server serves.
func (a *Auth) SameOrigin(r *http.Request) bool {
	o := r.Header.Get("Origin")
	return o == "" || a.selfOrigin(o)
}

// Wrap applies the rules to every request.
func (a *Auth) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.Host)
		if err != nil {
			host = r.Host
		}
		if a.Loopback && !loopbackName(host) {
			http.Error(w, "this server only answers to localhost", http.StatusForbidden)
			return
		}
		origin := r.Header.Get("Origin")
		api := strings.HasPrefix(r.URL.Path, "/api/")
		if origin != "" && !a.selfOrigin(origin) {
			if !a.allowed(origin) {
				http.Error(w, "origin not allowed (start the server with --allow-origin "+origin+")", http.StatusForbidden)
				return
			}
			h := w.Header()
			h.Set("Access-Control-Allow-Origin", origin)
			h.Set("Vary", "Origin")
			h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
			h.Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			h.Set("Access-Control-Expose-Headers", "Content-Disposition")
			if r.Method == http.MethodOptions {
				// Chrome's Private Network Access preflight for a public page calling localhost.
				h.Set("Access-Control-Allow-Private-Network", "true")
				w.WriteHeader(http.StatusNoContent)
				return
			}
			if api && !a.tokenOK(r) {
				http.Error(w, `{"error":"pairing token required"}`, http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		if api && !a.Loopback && !a.tokenOK(r) {
			http.Error(w, `{"error":"pairing token required"}`, http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}
