package auth

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/store"
)

// Password guessing is held to maxFailedSignIns wrong answers per client
// address in failedSignInWindow. Behind a proxy every client shares its
// address, so the limit is then the whole instance's: guessing slows for
// everyone, which is the side to err on.
const (
	maxFailedSignIns   = 10
	failedSignInWindow = 15 * time.Minute
	// maxTrackedClients bounds the limiter's memory. At this bound, the
	// limiter drops the expired windows. If that frees no room, it drops the
	// oldest window. So a flood of addresses costs the displaced client its
	// count, not the process its memory.
	maxTrackedClients = 10_000
)

// attempts counts failed local sign-ins per client address.
type attempts struct {
	now func() time.Time

	mu     sync.Mutex
	failed map[string]window
}

type window struct {
	start time.Time
	n     int
}

func newAttempts(now func() time.Time) *attempts {
	return &attempts{now: now, failed: map[string]window{}}
}

// try runs check, a password comparison, for client, unless its wrong answers
// exhausted its window. It records a wrong answer. The check runs under the
// lock, so concurrent attempts are judged one after another. Each attempt
// sees the failures recorded before it. None passes the limit on the strength
// of a count that is still to grow. A right answer, or one still in flight,
// costs the client nothing.
func (a *attempts) try(client string, check func() bool) (admitted, ok bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.now()
	w, tracked := a.failed[client]
	if tracked && now.Sub(w.start) >= failedSignInWindow {
		delete(a.failed, client)
		w, tracked = window{}, false
	}
	if w.n >= maxFailedSignIns {
		return false, false
	}
	if check() {
		return true, true
	}
	if !tracked {
		a.evict(now)
		w.start = now
	}
	w.n++
	a.failed[client] = w
	return true, false
}

// evict makes room for one more client once maxTrackedClients are
// tracked: the expired windows go, and failing that the oldest one.
func (a *attempts) evict(now time.Time) {
	if len(a.failed) < maxTrackedClients {
		return
	}
	for k, w := range a.failed {
		if now.Sub(w.start) >= failedSignInWindow {
			delete(a.failed, k)
		}
	}
	if len(a.failed) < maxTrackedClients {
		return
	}
	var oldest string
	for k, w := range a.failed {
		if oldest == "" || w.start.Before(a.failed[oldest].start) {
			oldest = k
		}
	}
	delete(a.failed, oldest)
}

// clientAddr is the address a request came from, without its port.
func clientAddr(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// maxLocalBody bounds a password sign-in's request body.
const maxLocalBody = 4 << 10

// localSignIn signs the local admin in with the password the file or the
// environment sets, and starts an admin session. It answers in JSON, since
// the dashboard posts it from the sign-in page.
func (h *Handler) localSignIn(w http.ResponseWriter, r *http.Request) {
	file := h.current.Get()
	user, password, ok := file.Auth.AdminUser()
	if !ok {
		writeJSON(w, http.StatusNotFound, errorBody{Code: codeUnknownSignIn})
		return
	}
	var body struct {
		User     string `json:"user"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxLocalBody)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody{Code: codeInvalidRequest})
		return
	}
	client := clientAddr(r)
	admitted, ok := h.attempts.try(client, func() bool {
		// Both compared whatever the first says, so the time taken tells
		// nothing about which was wrong.
		userOK, passwordOK := equalSecret(body.User, user), equalSecret(body.Password, password.Value())
		return userOK && passwordOK
	})
	switch {
	case !admitted:
		writeJSON(w, http.StatusTooManyRequests, errorBody{Code: codeTooManyAttempts})
		return
	case !ok:
		h.logger.WarnContext(r.Context(), "auth: local sign-in refused", "client", client)
		writeJSON(w, http.StatusUnauthorized, errorBody{Code: codeInvalidCredentials})
		return
	}
	key, _ := GrantKey(file, string(configfile.SignInLocal))
	id := Identity{Provider: string(configfile.SignInLocal), Origin: localOrigin, Subject: user, Login: user, DisplayName: user}
	token, expires, err := h.replaceSession(r, id, store.SessionGrant{Role: RoleAdmin, Key: key}, file.Auth.SessionTTLOrDefault())
	if err != nil {
		h.logger.ErrorContext(r.Context(), "auth: local sign-in", "error", err)
		writeJSON(w, http.StatusInternalServerError, errorBody{Code: codeInternal})
		return
	}
	http.SetCookie(w, sessionCookie(h.webURL, token, expires))
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}

// equalSecret compares two strings in time that depends on neither.
func equalSecret(a, b string) bool {
	ha, hb := sha256.Sum256([]byte(a)), sha256.Sum256([]byte(b))
	return subtle.ConstantTimeCompare(ha[:], hb[:]) == 1
}
