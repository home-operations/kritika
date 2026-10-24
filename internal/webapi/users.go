package webapi

import (
	"cmp"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/home-operations/kritika/internal/auth"
	"github.com/home-operations/kritika/internal/store"
)

// registerUsers mounts the users an admin lists and signs out.
func (s *Server) registerUsers(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/admin/users", s.admin(s.listUsers))
	mux.HandleFunc("DELETE /api/v1/admin/users/{id}/sessions", s.admin(s.signOutUser))
}

// listUsers serves every user, the most recently signed in first, with
// what their newest honoured session lets them read.
func (s *Server) listUsers(w http.ResponseWriter, r *http.Request) error {
	users, err := s.store.ListUsers(r.Context(), s.now())
	if err != nil {
		return err
	}
	file := s.current.Get()
	out := make([]AdminUser, len(users))
	for i, u := range users {
		a := AdminUser{User: toUser(u.User), SignIns: []UserSignIn{}, FirstSeenAt: u.CreatedAt, LastSignInAt: u.LastSignInAt}
		for _, id := range u.Identities {
			a.SignIns = append(a.SignIns, UserSignIn{Provider: id.Provider, Login: id.Login})
		}
		for _, sess := range u.Sessions {
			p, ok := auth.SessionPrincipal(file, sess)
			if !ok {
				continue
			}
			a.Sessions++
			if a.Access != nil {
				continue
			}
			a.Access = &UserAccess{Admin: p.Admin, AllAccounts: p.AllAccounts, Accounts: []string{}}
			if !p.Admin && !p.AllAccounts {
				for _, t := range readable(file, p) {
					a.Access.Accounts = append(a.Access.Accounts, t.Slug())
				}
			}
		}
		out[i] = a
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// signOutUser ends every session of a user, the admin's own included, and
// records it.
func (s *Server) signOutUser(w http.ResponseWriter, r *http.Request) error {
	ctx, id := r.Context(), r.PathValue("id")
	// Postgres's own spelling, the one the API hands out: uuid.Validate
	// allows spellings $1::uuid refuses, and the audit records the id.
	if u, err := uuid.Parse(id); err != nil || u.String() != id {
		return errNotFound("user")
	}
	p := auth.PrincipalFrom(ctx)
	err := s.store.WithAccount(ctx, "", func(tx pgx.Tx) error {
		u, err := store.GetUser(ctx, tx, id)
		if err != nil {
			return err
		}
		n, err := store.DeleteUserSessions(ctx, tx, id)
		if err != nil {
			return err
		}
		return record(ctx, tx, p, "", AuditUserSignOut, cmp.Or(u.DisplayName, u.Email, u.ID), map[string]any{"user": id, "sessions": n})
	})
	if err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}
