package auth

import (
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"time"
)

// SessionCookieName is the HttpOnly cookie name carrying the session token.
// The token is the only thing the cookie holds; the row lives in the sessions
// table (DECISIONS.md F).
const SessionCookieName = "podium_session"

// sessionTTL is how long a session stays valid. 7 days matches typical
// "remember me" expectations without leaving stale rows forever.
const sessionTTL = 7 * 24 * time.Hour

// NewSessionToken returns a fresh, URL-safe opaque token. 32 bytes of
// crypto/rand entropy → 43 base64url characters without padding. Callers
// should treat the result as unguessable; do not log it.
func NewSessionToken() (string, error) {
	var buf [32]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf[:]), nil
}

// SetSessionCookie writes the session cookie to w with the security
// attributes spec.md §7 / AGENTS.md §6 require: HttpOnly, SameSite=Lax,
// Path=/. Max-Age matches sessionTTL. If token is empty the cookie is
// cleared (Max-Age=0) — used by Logout.
func SetSessionCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(sessionTTL.Seconds()),
	})
}

// ClearSessionCookie expires the session cookie in the browser. Idempotent.
func ClearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}

// CookieFromRequest returns the session cookie value and true if the request
// carried the cookie. Returns ("", false) if absent — never an error, since
// "no cookie" is the normal state for unauthenticated requests.
func CookieFromRequest(r *http.Request) (string, bool) {
	c, err := r.Cookie(SessionCookieName)
	if err != nil || c.Value == "" {
		return "", false
	}
	return c.Value, true
}
