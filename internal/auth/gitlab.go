package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"golang.org/x/oauth2"

	"github.com/home-operations/kritika/internal/configfile"
)

// gitlabScopes ask for GitLab's OpenID Connect claims alone, which its
// userinfo endpoint answers with: the user's id, username, primary email
// and its verification, and groups. Without an API scope the token reads
// nothing else.
var gitlabScopes = []string{"openid", "email"}

// gitlabUserinfo is GitLab's OpenID Connect userinfo endpoint, under the
// instance's URL.
const gitlabUserinfo = "/oauth/userinfo"

// newGitLabProvider signs in through an OAuth application of the GitLab
// instance at the sign-in's host.
func newGitLabProvider(s *configfile.SignIn, redirect string, client *http.Client) *forgeProvider {
	web := "https://" + s.Host
	endpoint := oauth2.Endpoint{AuthURL: web + "/oauth/authorize", TokenURL: web + "/oauth/token"}
	return newForgeProvider(s, endpoint, web, redirect, gitlabScopes, client, gitlabAPI{})
}

type gitlabAPI struct{}

// gitlabUser is what GitLab's userinfo endpoint says of the user. Sub is
// the user's id, which some releases send as a string and others as a
// number.
type gitlabUser struct {
	Sub           json.Number `json:"sub"`
	Nickname      string      `json:"nickname"`
	Name          string      `json:"name"`
	Email         string      `json:"email"`
	EmailVerified bool        `json:"email_verified"`
	Picture       string      `json:"picture"`
	Groups        []string    `json:"groups"`
}

func (gitlabAPI) userinfo(ctx context.Context, c apiClient) (gitlabUser, error) {
	var u gitlabUser
	if err := c.getOK(ctx, gitlabUserinfo, &u); err != nil {
		return gitlabUser{}, err
	}
	return u, nil
}

func (a gitlabAPI) identity(ctx context.Context, c apiClient) (Identity, error) {
	u, err := a.userinfo(ctx, c)
	if err != nil {
		return Identity{}, err
	}
	if u.Sub == "" || u.Nickname == "" {
		return Identity{}, fmt.Errorf("%w: userinfo has no sub or nickname", ErrForgeAPI)
	}
	return Identity{
		Subject: u.Sub.String(), Login: u.Nickname, Email: u.Email, EmailVerified: u.Email != "" && u.EmailVerified,
		DisplayName: u.Name, AvatarURL: u.Picture,
	}, nil
}

// member reports whether the user is a member of group, a top-level
// group's full path: GitLab lists every group the user is a member of,
// directly or through an ancestor, so a member of a subgroup alone is not
// one of the group.
func (a gitlabAPI) member(ctx context.Context, c apiClient, _ Identity, group string) (bool, error) {
	u, err := a.userinfo(ctx, c)
	if err != nil {
		return false, err
	}
	return slices.ContainsFunc(u.Groups, func(g string) bool { return strings.EqualFold(g, group) }), nil
}

func (a gitlabAPI) mappingVars(ctx context.Context, c apiClient, id Identity) (map[string]any, error) {
	u, err := a.userinfo(ctx, c)
	if err != nil {
		return nil, err
	}
	vars := identityVars(id, verifiedOnly(id))
	vars["groups"] = append([]string{}, u.Groups...)
	return vars, nil
}
