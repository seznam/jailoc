package proxy

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"html/template"
	"net/http"
)

var loginPage = template.Must(template.New("login").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width"><title>jailoc login</title></head>
<body><main><h1>{{.Host}}</h1>{{if .SignedIn}}
<p>Signed in.</p><form method="post" action="/.jailoc/logout"><button type="submit">Log out</button></form>
{{else}}<form method="post" action="/.jailoc/login">
<label for="code">Login code</label><input id="code" name="code" type="password" required autocomplete="off" autofocus>
<button type="submit">Log in</button></form>{{end}}</main></body></html>`))

func (g *Server) loginHandler(w http.ResponseWriter, r *http.Request, host string) {
	w.Header().Set("Cache-Control", "no-store")
	// same-origin, not no-referrer: with no-referrer browsers send
	// "Origin: null" on the login form POST, and the exact-Origin check
	// refuses it. Cross-origin requests still get no referrer.
	w.Header().Set("Referrer-Policy", "same-origin")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	switch r.URL.Path {
	case "/.jailoc/login":
		switch r.Method {
		case http.MethodGet:
			var page bytes.Buffer
			data := struct {
				Host     string
				SignedIn bool
			}{Host: host, SignedIn: g.authenticated(r, host) != nil}
			if err := loginPage.Execute(&page, data); err != nil {
				http.Error(w, "login unavailable", http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			// A disconnected client cannot receive an error after a partial page.
			_, _ = w.Write(page.Bytes())
		case http.MethodPost:
			g.login(w, r, host)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	case "/.jailoc/logout":
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if s := g.authenticated(r, host); s != nil {
			s.close()
		}
		setSessionCookie(w, "")
		http.Redirect(w, r, "/.jailoc/login", http.StatusSeeOther)
	default:
		http.NotFound(w, r)
	}
}

func (g *Server) login(w http.ResponseWriter, r *http.Request, host string) {
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid login form", http.StatusBadRequest)
		return
	}
	values := r.PostForm["code"]
	if len(values) != 1 {
		http.Error(w, "invalid login form", http.StatusBadRequest)
		return
	}
	hash := sha256.Sum256([]byte(values[0]))
	g.mu.Lock()
	route, ok := g.routes[host]
	code := g.codes[host]
	if !ok || !route.Auth || code == nil || subtle.ConstantTimeCompare(hash[:], code.hash[:]) != 1 || code.used || !g.now().Before(code.expires) {
		g.mu.Unlock()
		http.Error(w, "invalid or expired login code", http.StatusUnauthorized)
		return
	}
	if g.base == nil || g.base.Err() != nil {
		g.mu.Unlock()
		http.Error(w, "proxy stopped", http.StatusServiceUnavailable)
		return
	}
	token, err := randomToken()
	if err != nil {
		g.mu.Unlock()
		http.Error(w, "login unavailable", http.StatusInternalServerError)
		return
	}
	ctx, cancel := context.WithTimeout(g.base, g.sessionTTL)
	s := &session{host: host, expires: g.now().Add(g.sessionTTL), ctx: ctx, cancel: cancel, conns: make(map[*sessionConn]struct{})}
	key := sha256.Sum256([]byte(token))
	code.used = true
	g.sessions[key] = s
	context.AfterFunc(ctx, func() {
		s.close()
		g.mu.Lock()
		delete(g.sessions, key)
		g.mu.Unlock()
	})
	g.mu.Unlock()
	setSessionCookie(w, token)
	http.Redirect(w, r, g.landing(host), http.StatusSeeOther) //nolint:gosec // G710: landing returns "/" or the validated same-origin Route.StartPath
}

// landing is where a signed-in browser of host starts: the route's start path,
// else the root.
func (g *Server) landing(host string) string {
	if r, ok := g.route(host); ok && r.StartPath != "" {
		return r.StartPath
	}
	return "/"
}
