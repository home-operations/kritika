package webapi

import (
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/home-operations/kritika/internal/auth"
	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/store"
)

// tokenNameMax is the longest name a token may have, in characters.
const tokenNameMax = 100

// tokenLifetimes are the days a token may last; 0 is never expiring.
var tokenLifetimes = []int{0, 7, 30, 90, 365}

// registerTokens mounts the personal access tokens a user manages, and
// those an admin lists and revokes.
func (s *Server) registerTokens(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/me/tokens", s.handler(s.listOwnTokens))
	mux.HandleFunc("POST /api/v1/me/tokens", s.handler(s.createToken))
	mux.HandleFunc("DELETE /api/v1/me/tokens/{id}", s.handler(s.revokeOwnToken))
	mux.HandleFunc("GET /api/v1/admin/tokens", s.admin(s.listAllTokens))
	mux.HandleFunc("DELETE /api/v1/admin/tokens/{id}", s.admin(s.revokeAnyToken))
}

func (s *Server) listOwnTokens(w http.ResponseWriter, r *http.Request) error {
	return s.listTokens(w, r, auth.PrincipalFrom(r.Context()).User.ID)
}

func (s *Server) listAllTokens(w http.ResponseWriter, r *http.Request) error {
	return s.listTokens(w, r, "")
}

// listTokens serves the tokens of the user, or every user's with their
// owners when userID is "".
func (s *Server) listTokens(w http.ResponseWriter, r *http.Request, userID string) error {
	ctx := r.Context()
	var tokens []store.APIToken
	if err := s.store.WithAccount(ctx, "", func(tx pgx.Tx) error {
		var err error
		tokens, err = store.ListAPITokens(ctx, tx, userID)
		return err
	}); err != nil {
		return err
	}
	file, now := s.current.Get(), s.now()
	out := make([]APIToken, len(tokens))
	for i, t := range tokens {
		out[i] = tokenInfo(file, t, now, userID == "")
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// createToken creates a token for the signed-in user from what their
// session reads, and serves it with its value, once.
func (s *Server) createToken(w http.ResponseWriter, r *http.Request) error {
	var req CreateTokenRequest
	if err := readBody(r, &req); err != nil {
		return err
	}
	name := strings.TrimSpace(req.Name)
	if name == "" || utf8.RuneCountInString(name) > tokenNameMax {
		return errBadRequest(CodeBadRequest, "name must be 1 to 100 characters")
	}
	if !slices.Contains(tokenLifetimes, req.ExpiresInDays) {
		return errBadRequest(CodeBadRequest, "expiresInDays must be 7, 30, 90, 365, or 0 for never")
	}
	ctx, p, file, now := r.Context(), auth.PrincipalFrom(r.Context()), s.current.Get(), s.now()
	g, err := tokenGrant(file, p, req.Accounts)
	if err != nil {
		return err
	}
	g.Key, _ = auth.GrantKey(file, p.Identity.Provider)
	t := store.APIToken{
		User: p.User, Name: name, Identity: store.SignInIdentity{Provider: p.Identity.Provider, Origin: p.Identity.Origin},
		Grant: g, CreatedAt: now,
	}
	if req.ExpiresInDays > 0 {
		expires := now.Add(time.Duration(req.ExpiresInDays) * 24 * time.Hour)
		t.ExpiresAt = &expires
	}
	var token string
	if err := s.store.WithAccount(ctx, "", func(tx pgx.Tx) error {
		var err error
		if token, t.ID, err = store.CreateAPIToken(ctx, tx, t); err != nil {
			return err
		}
		info := tokenInfo(file, t, now, false)
		detail := map[string]any{"id": t.ID, "allAccounts": info.AllAccounts, "accounts": info.Accounts, "expiresAt": t.ExpiresAt}
		return record(ctx, tx, p, "", AuditTokenCreate, name, detail)
	}); err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, CreatedToken{APIToken: tokenInfo(file, t, now, false), Token: token})
	return nil
}

func (s *Server) revokeOwnToken(w http.ResponseWriter, r *http.Request) error {
	return s.revokeToken(w, r, auth.PrincipalFrom(r.Context()).User.ID)
}

func (s *Server) revokeAnyToken(w http.ResponseWriter, r *http.Request) error {
	return s.revokeToken(w, r, "")
}

// revokeToken deletes a token, only the user's own unless userID is "",
// and records it.
func (s *Server) revokeToken(w http.ResponseWriter, r *http.Request, userID string) error {
	ctx, id, p := r.Context(), r.PathValue("id"), auth.PrincipalFrom(r.Context())
	// Postgres's own spelling, the one the API hands out: the audit
	// records the id.
	if u, err := uuid.Parse(id); err != nil || u.String() != id {
		return errNotFound("token")
	}
	err := s.store.WithAccount(ctx, "", func(tx pgx.Tx) error {
		t, err := store.DeleteAPIToken(ctx, tx, id, userID)
		if err != nil {
			return err
		}
		return record(ctx, tx, p, "", AuditTokenRevoke, t.Name, map[string]any{"id": id, "owner": t.User.ID})
	})
	if err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// tokenGrant is the read-only grant of a token p creates for the account
// slugs: the keys of those accounts, each one p reads; else every account,
// when p reads every account, or the keys of all p reads.
func tokenGrant(file *configfile.File, p *auth.Principal, slugs []string) (store.SessionGrant, error) {
	g := store.SessionGrant{Role: store.RoleMember, Accounts: []string{}}
	reads := readable(file, p)
	if len(slugs) == 0 && (p.Admin || p.AllAccounts) {
		g.AllAccounts = true
		return g, nil
	}
	for _, a := range reads {
		if len(slugs) == 0 || slices.Contains(slugs, a.Slug()) {
			g.Accounts = append(g.Accounts, a.Key())
		}
	}
	for _, slug := range slugs {
		if !slices.ContainsFunc(reads, func(a *configfile.Account) bool { return a.Slug() == slug }) {
			return g, errBadRequest(CodeBadRequest, "accounts must be accounts you read: "+slug)
		}
	}
	if len(g.Accounts) == 0 {
		return g, errBadRequest(CodeBadRequest, "a token must read at least one account")
	}
	return g, nil
}

// tokenInfo is t as a list shows it at now, with its owner when withOwner.
func tokenInfo(file *configfile.File, t store.APIToken, now time.Time, withOwner bool) APIToken {
	info := APIToken{
		ID: t.ID, Name: t.Name, AllAccounts: t.Grant.AllAccounts, Accounts: []string{}, Status: TokenActive,
		CreatedAt: t.CreatedAt, ExpiresAt: t.ExpiresAt, LastUsedAt: t.LastUsedAt,
	}
	for i := range file.Accounts {
		if a := &file.Accounts[i]; slices.Contains(t.Grant.Accounts, a.Key()) {
			info.Accounts = append(info.Accounts, a.Slug())
		}
	}
	switch {
	case t.ExpiresAt != nil && !now.Before(*t.ExpiresAt):
		info.Status = TokenExpired
	default:
		if _, ok := auth.SessionPrincipal(file, t.Session()); !ok {
			info.Status = TokenStale
		}
	}
	if withOwner {
		u := toUser(t.User)
		info.Owner = &u
	}
	return info
}
