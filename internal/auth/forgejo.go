package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"github.com/home-operations/kritika/internal/configfile"
)

// forgejoScopes read the profile, its emails and organization memberships.
// Forgejo and Gitea limit an OAuth2 token to the API scopes it was granted,
// and grant every scope to one that asks for none.
var forgejoScopes = []string{"read:user", "read:organization"}

// newForgejoProvider signs in through an OAuth2 application of the Forgejo
// or Gitea instance at the sign-in's host: Gitea's OAuth2 provider and the
// API calls a sign-in makes are Forgejo's.
func newForgejoProvider(s *configfile.SignIn, redirect string, client *http.Client) *forgeProvider {
	web := "https://" + s.Host
	return newForgeProvider(s, loginOAuth(web), web+"/api/v1", redirect, forgejoScopes, client, forgejoAPI{})
}

type forgejoAPI struct{}

func (forgejoAPI) identity(ctx context.Context, c apiClient) (Identity, error) {
	var u struct {
		ID        int64  `json:"id"`
		Login     string `json:"login"`
		FullName  string `json:"full_name"`
		Email     string `json:"email"`
		AvatarURL string `json:"avatar_url"`
	}
	if err := c.getOK(ctx, "/user", &u); err != nil {
		return Identity{}, err
	}
	if u.ID == 0 || u.Login == "" {
		return Identity{}, fmt.Errorf("%w: /user has no id or login", ErrForgeAPI)
	}
	id := Identity{Subject: strconv.FormatInt(u.ID, 10), Login: u.Login, Email: u.Email, DisplayName: u.FullName, AvatarURL: u.AvatarURL}
	if err := verifiedEmail(ctx, c, &id); err != nil {
		return Identity{}, err
	}
	return id, nil
}

// member asks whether the user is a member of org: 204 when they are, and
// 404 when not, or when org is no organization or one hidden from them.
func (forgejoAPI) member(ctx context.Context, c apiClient, id Identity, org string) (bool, error) {
	status, _, _, err := c.get(ctx, "/orgs/"+url.PathEscape(org)+"/members/"+url.PathEscape(id.Login), nil)
	switch {
	case err != nil:
		return false, err
	case status == http.StatusNoContent:
		return true, nil
	case notMember(status):
		return false, nil
	}
	return false, fmt.Errorf("%w: organization membership: status %d", ErrForgeAPI, status)
}

// mappingVars reads the organizations the user is a member of and the teams
// they are on, for the role mapping.
func (forgejoAPI) mappingVars(ctx context.Context, c apiClient, id Identity) (map[string]any, error) {
	orgs := []string{}
	err := forgejoPages(ctx, c, "/user/orgs", func(raw json.RawMessage) (int, error) {
		var page []struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(raw, &page); err != nil {
			return 0, err
		}
		for _, o := range page {
			orgs = append(orgs, o.Name)
		}
		return len(page), nil
	})
	if err != nil {
		return nil, err
	}
	teams := []string{}
	err = forgejoPages(ctx, c, "/user/teams", func(raw json.RawMessage) (int, error) {
		var page []struct {
			Name         string `json:"name"`
			Organization struct {
				Name string `json:"name"`
			} `json:"organization"`
		}
		if err := json.Unmarshal(raw, &page); err != nil {
			return 0, err
		}
		for _, t := range page {
			teams = append(teams, t.Organization.Name+"/"+t.Name)
		}
		return len(page), nil
	})
	if err != nil {
		return nil, err
	}
	vars := identityVars(id, verifiedOnly(id))
	vars["orgs"], vars["teams"] = orgs, teams
	return vars, nil
}

// forgejoPageSize is the page size that a listing asks for, which is the
// instance's default cap. forgejoMaxPages bounds how many pages a sign-in
// reads.
const (
	forgejoPageSize = 50
	forgejoMaxPages = 20
)

// forgejoPages calls fn with each page of the list at path. It stops when the
// pages read reach the X-Total-Count that the instance sends. Without an
// X-Total-Count, it stops when a page comes back short. fn returns how many
// items its page held. A list longer than forgejoMaxPages is an error, not a
// cut list: a role mapping must not decide on part of what the person belongs
// to.
func forgejoPages(ctx context.Context, c apiClient, path string, fn func(json.RawMessage) (int, error)) error {
	read := 0
	for page := 1; page <= forgejoMaxPages; page++ {
		var raw json.RawMessage
		p := fmt.Sprintf("%s?limit=%d&page=%d", path, forgejoPageSize, page)
		status, header, _, err := c.get(ctx, p, &raw)
		switch {
		case err != nil:
			return err
		case status/100 != 2:
			return fmt.Errorf("%w: GET %s: status %d", ErrForgeAPI, p, status)
		}
		n, err := fn(raw)
		if err != nil {
			return fmt.Errorf("%w: %s: %w", ErrForgeAPI, path, err)
		}
		read += n
		if total, err := strconv.Atoi(header.Get("X-Total-Count")); err == nil {
			if n == 0 || read >= total {
				return nil
			}
			continue
		}
		if n < forgejoPageSize {
			return nil
		}
	}
	return fmt.Errorf("%w: %s: more than %d pages", ErrForgeAPI, path, forgejoMaxPages)
}
