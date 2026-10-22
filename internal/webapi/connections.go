package webapi

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"slices"
	"strconv"
	"sync"

	"github.com/jackc/pgx/v5"

	"github.com/home-operations/kritika/internal/auth"
	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/forge/forgejo"
	"github.com/home-operations/kritika/internal/forge/github"
	"github.com/home-operations/kritika/internal/store"
)

// registerConnections mounts the admin's view of the running connections
// and of the accounts a GitHub App's connection is installed on, where an
// installation no connection serves can be removed.
func (s *Server) registerConnections(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/admin/connections", s.admin(s.listConnections))
	mux.HandleFunc("GET /api/v1/admin/connections/{name}/installations", s.admin(s.listInstallations))
	mux.HandleFunc("DELETE /api/v1/admin/connections/{name}/installations/{id}", s.admin(s.uninstall))
}

func (s *Server) listConnections(w http.ResponseWriter, r *http.Request) error {
	var webhooks map[string]store.WebhookDeliveries
	if err := s.store.WithAccount(r.Context(), "", func(tx pgx.Tx) error {
		var err error
		webhooks, err = store.ReadWebhookDeliveries(r.Context(), tx)
		return err
	}); err != nil {
		return err
	}
	f := s.current.Get()
	out := make([]Connection, 0, len(f.Connections))
	for i := range f.Connections {
		in := &f.Connections[i]
		c := connection(in)
		c.delivered(webhooks[in.ID()])
		out = append(out, c)
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// connectionApp is the running connection r names and its App. A
// connection of another forge has none: the installation routes are a
// GitHub App's.
func (s *Server) connectionApp(r *http.Request) (*configfile.Connection, *github.App, error) {
	in, ok := s.current.Get().Connection(r.PathValue("name"))
	if !ok {
		return nil, nil, errNotFound("connection")
	}
	if in.Forge != configfile.ForgeGitHub {
		return nil, nil, errStatus(http.StatusConflict, CodeNotAnApp, "connection "+in.Name+" is a "+string(in.Forge)+" bot, not a GitHub App")
	}
	app, err := s.apps.get(in, s.githubAPI)
	if err != nil {
		return nil, nil, errForge(err)
	}
	return in, app, nil
}

// appCache keeps one App per connection, by what it is built from, so the
// admin's requests share the installation tokens it minted rather than
// parsing the key and minting one per request. The configuration is read
// once per process, so the cache holds at most one App per connection.
type appCache struct {
	mu   sync.Mutex
	apps map[string]*github.App
}

func (c *appCache) get(in *configfile.Connection, apiBase string) (*github.App, error) {
	sum := sha256.Sum256([]byte(in.ID() + "\x00" + in.App.ClientIDValue() + "\x00" + in.App.PrivateKeyValue().Value()))
	key := hex.EncodeToString(sum[:])
	c.mu.Lock()
	defer c.mu.Unlock()
	if app, ok := c.apps[key]; ok {
		return app, nil
	}
	app, err := github.NewApp(in.App.ClientIDValue(), in.App.PrivateKeyValue().Value(), apiBase)
	if err != nil {
		return nil, err
	}
	if c.apps == nil {
		c.apps = map[string]*github.App{}
	}
	c.apps[key] = app
	return app, nil
}

// errForge is the forge failing or refusing a call the admin asked for.
func errForge(err error) error {
	return errStatus(http.StatusBadGateway, CodeForgeError, err.Error())
}

// reach lists what the running connection r names reaches on each account
// it serves: the repositories its App's installation there sees, or those
// its bot can see.
func (s *Server) reach(r *http.Request) (*configfile.Connection, []reachedAccount, error) {
	in, ok := s.current.Get().Connection(r.PathValue("name"))
	if !ok {
		return nil, nil, errNotFound("connection")
	}
	var out []reachedAccount
	switch in.Forge {
	case configfile.ForgeGitHub:
		app, err := s.apps.get(in, s.githubAPI)
		if err != nil {
			return nil, nil, errForge(err)
		}
		accounts, err := app.Reach(r.Context(), in.Accounts)
		if err != nil {
			return nil, nil, errForge(err)
		}
		for _, a := range accounts {
			ra := reachedAccount{Account: a.Account}
			for _, x := range a.Repositories {
				ra.Repositories = append(ra.Repositories, reachedRepository(x.FullName, x.DefaultBranch, x.Archived, x.Fork, x.Visibility))
			}
			out = append(out, ra)
		}
	case configfile.ForgeForgejo:
		issuer, err := forgejo.NewIssuer(forgejo.IssuerURL(s.webURL), in.Forgejo.PrivateKeyValue().Value())
		if err != nil {
			return nil, nil, errForge(err)
		}
		c, err := forgejo.NewClient(in.Forgejo.Host, issuer, in.Forgejo.AudienceValue(), "", nil)
		if err != nil {
			return nil, nil, errForge(err)
		}
		for _, account := range in.Accounts {
			listed, err := c.Repositories(r.Context(), account)
			if err != nil {
				return nil, nil, errForge(err)
			}
			ra := reachedAccount{Account: account}
			for _, x := range listed {
				ra.Repositories = append(ra.Repositories, reachedRepository(x.FullName, x.DefaultBranch, x.Archived, x.Fork, x.Visibility))
			}
			out = append(out, ra)
		}
	}
	return in, out, nil
}

// reachedAccount is one account a connection serves and what it reaches
// there.
type reachedAccount struct {
	Account      string
	Repositories []store.ReachedRepository
}

func reachedRepository(fullName, defaultBranch string, archived, fork bool, visibility string) store.ReachedRepository {
	return store.ReachedRepository{
		FullName: fullName, DefaultBranch: defaultBranch,
		Traits: &configfile.RepoTraits{Archived: archived, Fork: fork, Visibility: visibility},
	}
}

func (s *Server) listInstallations(w http.ResponseWriter, r *http.Request) error {
	in, app, err := s.connectionApp(r)
	if err != nil {
		return err
	}
	insts, err := app.Installations(r.Context())
	if err != nil {
		return errForge(err)
	}
	out := make([]AppInstallation, 0, len(insts))
	for _, x := range insts {
		out = append(out, AppInstallation{
			ID: x.ID, Account: x.Account, AccountType: x.AccountType, AllRepositories: x.AllRepositories,
			Suspended: x.Suspended, Served: in.Serves(x.Account), URL: x.HTMLURL,
		})
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// uninstall removes an installation of a connection's App from an account
// the connection does not serve, audited in the same transaction.
func (s *Server) uninstall(w http.ResponseWriter, r *http.Request) error {
	p := auth.PrincipalFrom(r.Context())
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		return errNotFound("installation")
	}
	in, app, err := s.connectionApp(r)
	if err != nil {
		return err
	}
	insts, err := app.Installations(r.Context())
	if err != nil {
		return errForge(err)
	}
	i := slices.IndexFunc(insts, func(x github.Installation) bool { return x.ID == id })
	if i < 0 {
		return errNotFound("installation")
	}
	inst := &insts[i]
	if in.Serves(inst.Account) {
		return errStatus(http.StatusConflict, CodeInstallationServed,
			"connection "+in.Name+" serves "+inst.Account+": remove the account from its accounts first")
	}
	err = s.store.WithAccount(r.Context(), "", func(tx pgx.Tx) error {
		detail := map[string]any{"installation": id}
		if err := record(r.Context(), tx, p, "", AuditAppUninstall, in.Name+"/"+inst.Account, detail); err != nil {
			return err
		}
		if err := app.Uninstall(r.Context(), id); err != nil {
			return errForge(err)
		}
		return nil
	})
	if err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// delivered sets when c's webhook last delivered a verified request and an
// unsigned one.
func (c *Connection) delivered(d store.WebhookDeliveries) {
	c.LastWebhookAt, c.LastUnsignedWebhookAt = d.Verified, d.Unsigned
}
