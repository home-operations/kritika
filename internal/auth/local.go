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
	// maxTrackedClients bounds the limiter's memory: at it, expired windows
	// are dropped, and failing that the oldest one, so a flood of addresses
	// costs the client it displaces its count rather than the process its
	// memory. In-flight attempts cannot be displaced.
	maxTrackedClients = 10_000
)

// attempts counts failed and in-flight local sign-ins per client address.
type attempts struct {
	now func() time.Time

	mu     sync.Mutex
	failed map[string]window
}

type window struct {
	start   time.Time
	n       int
	pending int
}

func newAttempts(now func() time.Time) *attempts {
	return &attempts{now: now, failed: map[string]window{}}
}

// begin reserves an attempt before reading its body, so concurrent requests
// cannot all pass the limit while their passwords are still in flight.
func (a *attempts) begin(client string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.now()
	if _, tracked := a.failed[client]; !tracked && len(a.failed) >= maxTrackedClients {
		for k, w := range a.failed {
			if w.pending == 0 && now.Sub(w.start) >= failedSignInWindow {
				delete(a.failed, k)
			}
		}
		if len(a.failed) >= maxTrackedClients {
			var oldest string
			for k, w := range a.failed {
				if w.pending == 0 && (oldest == "" || w.start.Before(a.failed[oldest].start)) {
					oldest = k
				}
			}
			if oldest == "" {
				return false
			}
			delete(a.failed, oldest)
		}
	}
	w := a.failed[client]
	if now.Sub(w.start) >= failedSignInWindow {
		w.start, w.n = now, 0
	}
	if w.n+w.pending >= maxFailedSignIns {
		return false
	}
	w.pending++
	a.failed[client] = w
	return true
}

// finish releases an admitted attempt, retaining only wrong credentials
// against the limit. Pending attempts survive a failed window expiring.
func (a *attempts) finish(client string, failed bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	w := a.failed[client]
	w.pending--
	if failed {
		now := a.now()
		if w.n == 0 || now.Sub(w.start) >= failedSignInWindow {
			w.start, w.n = now, 0
		}
		w.n++
	}
	if w.n == 0 && w.pending == 0 {
		delete(a.failed, client)
	} else {
		a.failed[client] = w
	}
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
	client := clientAddr(r)
	if !h.attempts.begin(client) {
		writeJSON(w, http.StatusTooManyRequests, errorBody{Code: codeTooManyAttempts})
		return
	}
	failed := false
	defer func() { h.attempts.finish(client, failed) }()
	var body struct {
		User     string `json:"user"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxLocalBody)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody{Code: codeInvalidRequest})
		return
	}
	// Both compared whatever the first says, so the time taken tells nothing
	// about which was wrong.
	userOK, passwordOK := equalSecret(body.User, user), equalSecret(body.Password, password.Value())
	if !userOK || !passwordOK {
		failed = true
		h.logger.WarnContext(r.Context(), "auth: local sign-in refused", "client", client)
		writeJSON(w, http.StatusUnauthorized, errorBody{Code: codeInvalidCredentials})
		return
	}
	key, _ := GrantKey(file.Auth, string(configfile.SignInLocal))
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
