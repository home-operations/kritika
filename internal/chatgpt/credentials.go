// Package chatgpt implements Sign in with ChatGPT and token renewal for
// reviews that use a ChatGPT plan.
package chatgpt

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
)

// OpenAI's authorization server, and the resource a plan's tokens are for.
const (
	Issuer   = "https://auth.openai.com"
	TokenURL = Issuer + "/api/accounts/oauth/token"
	Resource = "https://api.openai.com/v1"
	// PlanScope is the scope that lets an app's requests draw on the plan;
	// a sign-in without it is an identity and nothing to review with.
	PlanScope = "chatgpt.tokens.use.direct"
)

// Credentials is one sign-in's record: the client the sign-in registered,
// its tokens and the account they are for. The access token lasts an hour; the
// refresh token thirty days, and every refresh replaces it.
type Credentials struct {
	Email        string    `json:"email,omitempty"`
	Issuer       string    `json:"issuer"`
	Subject      string    `json:"subject,omitempty"`
	ClientID     string    `json:"client_id"`
	HostID       string    `json:"ext_agent_host_id"`
	IDToken      string    `json:"id_token,omitempty"`
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	TokenType    string    `json:"token_type"`
	ExpiresIn    int64     `json:"expires_in"`
	Scopes       []string  `json:"scopes"`
	SavedAt      time.Time `json:"saved_at"`
}

// Validate checks that c names its client, carries both tokens and was
// granted plan usage.
func (c Credentials) Validate() error {
	switch {
	case c.ClientID == "":
		return errors.New("chatgpt: credentials: client_id is missing")
	case c.AccessToken == "":
		return errors.New("chatgpt: credentials: access_token is missing")
	case c.RefreshToken == "":
		return errors.New("chatgpt: credentials: refresh_token is missing")
	}
	if !slices.Contains(c.Scopes, PlanScope) {
		return fmt.Errorf("chatgpt: credentials: the sign-in did not grant %s, so the plan cannot be used", PlanScope)
	}
	return nil
}

// Expiry is when the access token expires.
func (c Credentials) Expiry() time.Time {
	return c.SavedAt.Add(time.Duration(c.ExpiresIn) * time.Second)
}

// RefreshLead is how long before the access token expires it is renewed,
// so a step's request never goes out with a token about to lapse under it.
const RefreshLead = 5 * time.Minute

// Due reports whether the access token is within RefreshLead of expiry at
// now, or past it.
func (c Credentials) Due(now time.Time) bool { return !now.Before(c.Expiry().Add(-RefreshLead)) }

// clientTimeout bounds one request of a client the package makes itself.
const clientTimeout = 30 * time.Second

// ErrSignedOut is a disconnected provider or an unusable refresh token.
// The saved client and host identify the next sign-in.
var ErrSignedOut = errors.New("chatgpt: signed out")

// signedOutCodes are the token endpoint's errors that end a session, as
// OpenAI's errors and recovery page lists them.
var signedOutCodes = []string{
	"invalid_grant", "invalid_refresh_token", "token_expired", "refresh_token_expired", "refresh_token_invalidated", "refresh_token_reused",
}

// Refresh trades c's refresh token for a new token set at tokenURL, with
// c's other fields kept and SavedAt set to now. A refresh token OpenAI
// refuses is ErrSignedOut; any other failure leaves c as it was, usable
// until its access token expires. Two refreshes of one session race its
// rotating token, so a caller serializes them.
func Refresh(ctx context.Context, client *http.Client, tokenURL string, c Credentials, now time.Time) (Credentials, error) {
	form := url.Values{
		"grant_type": {"refresh_token"}, "client_id": {c.ClientID}, "refresh_token": {c.RefreshToken}, "resource": {Resource},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return c, fmt.Errorf("chatgpt: refresh: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return c, fmt.Errorf("chatgpt: refresh: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return c, fmt.Errorf("chatgpt: refresh: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		var oauthErr struct {
			Code string `json:"error"`
		}
		// Error descriptions and raw bodies may echo credentials. Report only
		// the OAuth code; non-OAuth responses still carry their HTTP status.
		_ = json.Unmarshal(body, &oauthErr)
		if slices.Contains(signedOutCodes, oauthErr.Code) {
			return c, fmt.Errorf("%w: %s", ErrSignedOut, oauthErr.Code)
		}
		return c, fmt.Errorf("chatgpt: refresh: %s: %s", resp.Status, oauthErr.Code)
	}
	var tok struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		IDToken      string `json:"id_token"`
		TokenType    string `json:"token_type"`
		ExpiresIn    int64  `json:"expires_in"`
		Scope        string `json:"scope"`
	}
	if err := json.Unmarshal(body, &tok); err != nil {
		return c, fmt.Errorf("chatgpt: refresh: decode: %w", err)
	}
	if tok.AccessToken == "" || tok.RefreshToken == "" {
		return c, errors.New("chatgpt: refresh: the token response has no tokens")
	}
	c.AccessToken, c.RefreshToken, c.ExpiresIn, c.SavedAt = tok.AccessToken, tok.RefreshToken, tok.ExpiresIn, now
	c.IDToken = cmp.Or(tok.IDToken, c.IDToken)
	c.TokenType = cmp.Or(tok.TokenType, c.TokenType)
	if tok.Scope != "" {
		c.Scopes = strings.Fields(tok.Scope)
	}
	return c, nil
}
