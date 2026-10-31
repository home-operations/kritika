package webapi

import (
	"net/http"
	"strings"

	"github.com/home-operations/kritika/internal/configfile"
)

// How far the instance is from reviewing, for the Configuration page's
// checklist, and re-reading the repositories a connection's App reaches.

func (s *Server) registerSetup(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/admin/setup", s.admin(s.getSetup))
	mux.HandleFunc("POST /api/v1/admin/connections/{name}/repositories", s.admin(s.registerReached))
}

func (s *Server) getSetup(w http.ResponseWriter, r *http.Request) error {
	writeJSON(w, http.StatusOK, setupStatus(s.current.Get(), s.webURL.String()))
	return nil
}

// setupStatus is f's distance from reviewing, for the dashboard at webURL.
func setupStatus(f *configfile.File, webURL string) SetupStatus {
	base := strings.TrimSuffix(webURL, "/")
	out := SetupStatus{WebURL: base, HooksURL: base + "/hooks/", Connections: []string{}}
	for _, in := range f.Connections {
		out.Connections = append(out.Connections, in.Name)
	}
	if ref := f.Defaults.Review.Model; ref != nil {
		out.ReviewModel = string(ref.Default)
	}
	out.Embedding = f.Embedding != nil
	return out
}

// registerReached records every repository that the connection named by r
// reaches on an account that it serves. So they appear in the dashboard for
// enablement before any webhook names them. It also disables the ones that an
// account's listing no longer has, as the leader's sync does.
func (s *Server) registerReached(w http.ResponseWriter, r *http.Request) error {
	// The listing's time is the database's, which stamps the rows that a
	// webhook names meanwhile. This process's clock can differ from it.
	listedAt, err := s.store.Now(r.Context())
	if err != nil {
		return err
	}
	in, accounts, err := s.reach(r)
	if err != nil {
		return err
	}
	f := s.current.Get()
	var res RegisterResult
	for _, a := range accounts {
		acct, ok := f.Account(in.Forge, a.Account)
		if !ok || len(a.Repositories) == 0 {
			continue
		}
		added, _, err := s.store.RegisterRepositories(r.Context(), acct.ID(), a.Repositories, listedAt)
		if err != nil {
			return err
		}
		res.Added += added
	}
	writeJSON(w, http.StatusOK, res)
	return nil
}
