package main

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/oauth2"
)

//go:embed static/login.html
var loginPageFS embed.FS

const (
	oauthStateCookieName = "pajaros_oauth_state"
	oauthStateTTL        = 10 * time.Minute
)

// userContextKey is the context key under which require() stores the
// authenticated user's stable id.
type userContextKey struct{}

// Auth owns the login gate: the session cookie signer, the persisted user
// store, and the configured OAuth providers.
type Auth struct {
	signer    *cookieSigner
	users     *UserStore
	providers []oauthProvider
	byName    map[string]oauthProvider
	loginTmpl *template.Template
}

func newAuth(env oauthEnv, users *UserStore, signer *cookieSigner) (*Auth, error) {
	apple, err := newAppleProvider(env)
	if err != nil {
		// Apple's misconfiguration is only fatal for Apple; Google and the
		// login gate must still work.
		log.Printf("Sign in with Apple disabled: %v", err)
	}
	providers := []oauthProvider{
		newGoogleProvider(env),
		apple,
	}
	byName := make(map[string]oauthProvider, len(providers))
	for _, p := range providers {
		byName[p.name()] = p
	}

	tmpl, err := template.ParseFS(loginPageFS, "static/login.html")
	if err != nil {
		return nil, fmt.Errorf("parse login template: %w", err)
	}
	return &Auth{
		signer:    signer,
		users:     users,
		providers: providers,
		byName:    byName,
		loginTmpl: tmpl,
	}, nil
}

// require wraps every protected route. A request with a valid session cookie
// proceeds with the user id attached to its context; anything else is
// redirected to the login page (browser navigation) or rejected with 401
// (API/XHR).
func (a *Auth) require(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if userID, ok := a.sessionUser(r); ok {
			ctx := context.WithValue(r.Context(), userContextKey{}, userID)
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}
		if wantsAPIResponse(r) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"authentication required"}`))
			return
		}
		next := sanitizeNext(r.URL.RequestURI())
		http.Redirect(w, r, "/login?next="+url.QueryEscape(next), http.StatusFound)
	})
}

func (a *Auth) sessionUser(r *http.Request) (string, bool) {
	c, err := r.Cookie(sessionCookieName)
	if err != nil {
		return "", false
	}
	return a.signer.verifySession(c.Value)
}

// userIDFromContext returns the authenticated user's stable id for a request
// that passed through require().
func userIDFromContext(r *http.Request) string {
	id, _ := r.Context().Value(userContextKey{}).(string)
	return id
}

// -- login page & logout -----------------------------------------------------

type loginPageData struct {
	Providers []loginProvider
	Error     string
}

type loginProvider struct {
	Name        string
	DisplayName string
	URL         string
	Disabled    bool
}

func (a *Auth) handleLoginPage(w http.ResponseWriter, r *http.Request) {
	next := sanitizeNext(r.URL.Query().Get("next"))
	data := loginPageData{}
	for _, p := range a.providers {
		data.Providers = append(data.Providers, loginProvider{
			Name:        p.name(),
			DisplayName: p.displayName(),
			URL:         "/auth/" + p.name() + "?next=" + url.QueryEscape(next),
			Disabled:    !p.configured(),
		})
	}
	if e := r.URL.Query().Get("error"); e != "" {
		data.Error = e
	}
	a.renderLogin(w, http.StatusOK, data)
}

func (a *Auth) renderLogin(w http.ResponseWriter, status int, data loginPageData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := a.loginTmpl.Execute(w, data); err != nil {
		log.Printf("render login page: %v", err)
	}
}

func (a *Auth) handleLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
	if wantsAPIResponse(r) {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.Redirect(w, r, "/login", http.StatusFound)
}

// -- OAuth: authorization redirect -------------------------------------------

// oauthState is the signed, short-lived cookie carried through a provider
// round trip. Keeping it client-side avoids a server-side pending-login
// table while still binding the callback to the browser that started it.
type oauthState struct {
	Provider string `json:"provider"`
	State    string `json:"state"`
	Verifier string `json:"verifier,omitempty"`
	Nonce    string `json:"nonce"`
	Next     string `json:"next"`
	Exp      int64  `json:"exp"`
}

func (a *Auth) handleOAuthStart(p oauthProvider) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !p.configured() {
			a.renderLogin(w, http.StatusBadRequest, loginPageData{
				Error: p.displayName() + " sign-in is not configured on this server.",
			})
			return
		}
		state := randomToken()
		nonce := randomToken()
		verifier := ""
		if p.name() == "google" {
			verifier = oauth2.GenerateVerifier()
		}
		st := oauthState{
			Provider: p.name(),
			State:    state,
			Verifier: verifier,
			Nonce:    nonce,
			Next:     sanitizeNext(r.URL.Query().Get("next")),
			Exp:      time.Now().Add(oauthStateTTL).Unix(),
		}
		payload, err := json.Marshal(st)
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		http.SetCookie(w, &http.Cookie{
			Name:     oauthStateCookieName,
			Value:    a.signer.sign(payload),
			Path:     "/",
			HttpOnly: true,
			Secure:   true,
			SameSite: http.SameSiteLaxMode,
			MaxAge:   int(oauthStateTTL.Seconds()),
		})
		http.Redirect(w, r, p.authCodeURL(state, verifier, nonce), http.StatusFound)
	}
}

// -- OAuth: callback ---------------------------------------------------------

func (a *Auth) handleOAuthCallback(p oauthProvider) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !p.configured() {
			a.renderLogin(w, http.StatusBadRequest, loginPageData{
				Error: p.displayName() + " sign-in is not configured on this server.",
			})
			return
		}
		if err := r.ParseForm(); err != nil {
			a.loginFailure(w, r, "Could not read the sign-in response.")
			return
		}

		st, err := a.consumeState(w, r, p.name(), r.FormValue("state"))
		if err != nil {
			log.Printf("%s callback state check: %v", p.name(), err)
			a.loginFailure(w, r, "Your sign-in session expired. Please try again.")
			return
		}

		code := r.FormValue("code")
		if code == "" {
			a.loginFailure(w, r, "The provider did not return an authorization code.")
			return
		}

		idToken, err := p.exchange(r.Context(), code, st.Verifier)
		if err != nil {
			log.Printf("%s token exchange: %v", p.name(), err)
			a.loginFailure(w, r, "Could not complete sign-in with the provider.")
			return
		}
		claims, err := p.verifyIDToken(r.Context(), idToken, st.Nonce)
		if err != nil {
			log.Printf("%s id token verification: %v", p.name(), err)
			a.loginFailure(w, r, "Could not verify the provider's sign-in response.")
			return
		}

		displayName := ""
		if p.name() == "apple" {
			displayName = appleDisplayName(r)
		}
		a.completeLogin(w, r, p.name(), claims, displayName, st.Next)
	}
}

// consumeState validates and clears the OAuth state cookie, returning its
// contents. It checks the provider, expiry, and a constant-time match of the
// state value echoed by the provider.
func (a *Auth) consumeState(w http.ResponseWriter, r *http.Request, provider, gotState string) (*oauthState, error) {
	c, err := r.Cookie(oauthStateCookieName)
	if err != nil {
		return nil, fmt.Errorf("missing state cookie")
	}
	defer a.clearStateCookie(w, r)
	payload, ok := a.signer.unsign(c.Value)
	if !ok {
		return nil, fmt.Errorf("state cookie signature invalid")
	}
	var st oauthState
	if err := json.Unmarshal(payload, &st); err != nil {
		return nil, fmt.Errorf("state cookie malformed")
	}
	if st.Provider != provider {
		return nil, fmt.Errorf("state cookie provider mismatch")
	}
	if time.Now().Unix() >= st.Exp {
		return nil, fmt.Errorf("state cookie expired")
	}
	if gotState == "" || !hmacEqualString(st.State, gotState) {
		return nil, fmt.Errorf("state mismatch")
	}
	return &st, nil
}

// clearStateCookie expires the OAuth state cookie; r is unused but kept so
// the signature mirrors the other cookie helpers.
func (a *Auth) clearStateCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     oauthStateCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}

func (a *Auth) completeLogin(w http.ResponseWriter, r *http.Request, provider string, claims *idTokenClaims, displayName, next string) {
	user, err := a.users.Upsert(provider, claims.Subject, claims.Email, displayName)
	if err != nil {
		log.Printf("persist user %s:%s: %v", provider, claims.Subject, err)
		a.loginFailure(w, r, "Could not save your account.")
		return
	}
	token, err := a.signer.issueSession(user.ID, sessionTTL)
	if err != nil {
		log.Printf("issue session for %s: %v", user.ID, err)
		a.loginFailure(w, r, "Could not start your session.")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(sessionTTL.Seconds()),
	})
	http.Redirect(w, r, sanitizeNext(next), http.StatusFound)
}

func (a *Auth) loginFailure(w http.ResponseWriter, r *http.Request, msg string) {
	http.Redirect(w, r, "/login?error="+url.QueryEscape(msg), http.StatusFound)
}

// appleDisplayName extracts the name Apple sends only on the very first
// authorization, delivered as a JSON "user" form field rather than in the ID
// token. Most subsequent logins have no name at all, which is fine.
func appleDisplayName(r *http.Request) string {
	raw := r.FormValue("user")
	if raw == "" {
		return ""
	}
	var u struct {
		Name struct {
			FirstName string `json:"firstName"`
			LastName  string `json:"lastName"`
		} `json:"name"`
	}
	if err := json.Unmarshal([]byte(raw), &u); err != nil {
		return ""
	}
	return strings.TrimSpace(u.Name.FirstName + " " + u.Name.LastName)
}

// -- helpers -----------------------------------------------------------------

func randomToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand failing is unrecoverable; surface it rather than
		// returning a predictable token.
		panic(fmt.Sprintf("crypto/rand: %v", err))
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// wantsAPIResponse decides between a 401 and a login redirect. API paths and
// explicit JSON/XHR requests get the 401; everything else is assumed to be a
// browser navigation.
func wantsAPIResponse(r *http.Request) bool {
	if strings.HasPrefix(r.URL.Path, "/api/") {
		return true
	}
	if r.Header.Get("X-Requested-With") == "XMLHttpRequest" {
		return true
	}
	return strings.Contains(r.Header.Get("Accept"), "application/json")
}

// sanitizeNext constrains post-login redirects to local paths, preventing an
// open redirect via a crafted ?next= value. Backslashes are rejected too
// because browsers normalize "\" to "/", so "/\evil.example" would otherwise
// become a protocol-relative redirect.
func sanitizeNext(next string) string {
	if next == "" || !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") {
		return "/"
	}
	if strings.ContainsAny(next, "\\") {
		return "/"
	}
	u, err := url.Parse(next)
	if err != nil || u.IsAbs() || u.Host != "" {
		return "/"
	}
	if strings.HasPrefix(u.Path, "/login") || strings.HasPrefix(u.Path, "/auth/") {
		return "/"
	}
	return next
}
