package configfile

import (
	"cmp"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/home-operations/kritika/internal/rolemap"
)

// Auth configures how people sign in to the dashboard and what each can do:
// a local admin, an OIDC issuer, GitHub, and a Forgejo, a Gitea and a
// GitLab instance, each optional, and each provider's role mapping. Secret
// values use explicit environment references.
type Auth struct {
	// SessionTTL is how long a dashboard session lasts. Zero means
	// DefaultSessionTTL.
	SessionTTL time.Duration `yaml:"sessionTTL,omitempty"`
	// Admin is the local admin, signed in with a password. It exists only
	// while a password is set.
	Admin LocalAdmin `yaml:"admin,omitempty"`
	// OIDC signs in through an OpenID Connect issuer.
	OIDC *SignIn `yaml:"oidc,omitempty"`
	// GitHub signs in through a GitHub OAuth App, or a GitHub App's own
	// client, on github.com.
	GitHub *SignIn `yaml:"github,omitempty"`
	// Forgejo signs in through an OAuth2 application of the Forgejo
	// instance at its Host.
	Forgejo *SignIn `yaml:"forgejo,omitempty"`
	// Gitea signs in through an OAuth2 application of the Gitea instance
	// at its Host.
	Gitea *SignIn `yaml:"gitea,omitempty"`
	// GitLab signs in through an OAuth application of the GitLab instance
	// at its Host.
	GitLab *SignIn `yaml:"gitlab,omitempty"`
}

// LocalAdmin is the username and password that sign in as an admin. It is
// for a first run, or is a way in when every provider is down.
type LocalAdmin struct {
	// User is the admin's name. It is DefaultAdminUser when unset.
	User     string    `yaml:"user,omitempty"`
	Password SecretRef `yaml:"password,omitempty"`

	password Secret
}

// DefaultAdminUser is the local admin's name when none is set.
const DefaultAdminUser = "admin"

// SignInType selects how a sign-in authenticates. It is also the sign-in's
// name in the dashboard's URLs, /auth/login/<type>, since there is at most
// one of each.
type SignInType string

// Sign-in types. SignInLocal is the local admin's password form.
const (
	SignInLocal   SignInType = "local"
	SignInOIDC    SignInType = "oidc"
	SignInGitHub  SignInType = "github"
	SignInForgejo SignInType = "forgejo"
	SignInGitea   SignInType = "gitea"
	SignInGitLab  SignInType = "gitlab"
)

// SignIn is one identity provider people sign in through.
type SignIn struct {
	// Name labels an OIDC sign-in on the sign-in page. It is DefaultOIDCName
	// when unset. A GitHub sign-in is labeled GitHub.
	Name string `yaml:"name,omitempty"`
	// Issuer is an OIDC sign-in's discovery issuer URL, https only.
	Issuer string `yaml:"issuer,omitempty"`
	// Host is a forge instance sign-in's hostname, reached over HTTPS: the
	// instance whose accounts the sign-in places people on.
	Host         string     `yaml:"host,omitempty"`
	ClientID     ValueOrRef `yaml:"clientId"`
	ClientSecret SecretRef  `yaml:"clientSecret"`
	Scopes       []string   `yaml:"scopes,omitempty"`
	// RolesClaim names the OIDC claim whose values, as a list or as the keys
	// of a map, the role mapping sees as roles.
	RolesClaim string `yaml:"rolesClaim,omitempty"`
	// RoleMappingExpr is a CEL expression that decides what a person can do.
	// See package rolemap. A key whose value is CEL ends in Expr.
	RoleMappingExpr string `yaml:"roleMappingExpr,omitempty"`
	// DefaultRole is what an OIDC sign-in the mapping does not place gets:
	// none, refused, unless set to member, which reads every account.
	DefaultRole string `yaml:"defaultRole,omitempty"`

	typ          SignInType
	clientID     string
	clientSecret Secret
	mapping      *rolemap.Program
}

// DefaultOIDCName labels an OIDC sign-in that names none.
const DefaultOIDCName = "SSO"

// Default roles that an OIDC sign-in can give a person that its mapping does
// not place.
const (
	DefaultRoleNone   = "none"
	DefaultRoleMember = "member"
)

// Type is the sign-in's type.
func (s *SignIn) Type() SignInType { return s.typ }

// Label is how the sign-in page labels the sign-in.
func (s *SignIn) Label() string {
	switch s.typ {
	case SignInGitHub:
		return "GitHub"
	case SignInForgejo:
		return "Forgejo"
	case SignInGitea:
		return "Gitea"
	case SignInGitLab:
		return "GitLab"
	}
	return cmp.Or(s.Name, DefaultOIDCName)
}

// onInstance reports whether the sign-in is on a forge instance at its
// Host.
func (s *SignIn) onInstance() bool {
	return s.typ == SignInForgejo || s.typ == SignInGitea || s.typ == SignInGitLab
}

// ClientIDValue returns the client id, inline or resolved.
func (s *SignIn) ClientIDValue() string { return s.clientID }

// ClientSecretValue returns the resolved client secret.
func (s *SignIn) ClientSecretValue() Secret { return s.clientSecret }

// Mapping is the compiled role mapping, nil when none is set.
func (s *SignIn) Mapping() *rolemap.Program { return s.mapping }

// MembersByDefault reports whether an OIDC sign-in its mapping does not
// place reads every account rather than being refused.
func (s *SignIn) MembersByDefault() bool { return s.DefaultRole == DefaultRoleMember }

// DefaultSessionTTL applies when the file sets no sessionTTL.
const DefaultSessionTTL = 12 * time.Hour

// Bounds on a configured sessionTTL.
const (
	minSessionTTL = 5 * time.Minute
	maxSessionTTL = 30 * 24 * time.Hour
)

// SessionTTLOrDefault returns the session lifetime or its default.
func (a *Auth) SessionTTLOrDefault() time.Duration {
	return cmp.Or(a.SessionTTL, DefaultSessionTTL)
}

// SignIns lists the configured providers, OIDC first.
func (a *Auth) SignIns() []*SignIn {
	var out []*SignIn
	for _, s := range []*SignIn{a.OIDC, a.GitHub, a.Forgejo, a.Gitea, a.GitLab} {
		if s != nil {
			out = append(out, s)
		}
	}
	return out
}

// SignInByType returns the provider of type typ.
func (a *Auth) SignInByType(typ SignInType) (*SignIn, bool) {
	for _, s := range a.SignIns() {
		if s.typ == typ {
			return s, true
		}
	}
	return nil, false
}

// AdminUser is the local admin's name and password, ok only while a
// password is set.
func (a *Auth) AdminUser() (user string, password Secret, ok bool) {
	if a.Admin.password.Value() == "" {
		return "", Secret{}, false
	}
	return a.adminUser(), a.Admin.password, true
}

func (a *Auth) adminUser() string {
	return cmp.Or(a.Admin.User, DefaultAdminUser)
}

// Configured reports whether any way to sign in is set.
func (a *Auth) Configured() bool {
	_, _, local := a.AdminUser()
	return local || len(a.SignIns()) > 0
}

func (a *Auth) resolve(sec *secrets) error {
	if !a.Admin.Password.empty() {
		v, err := sec.read(a.Admin.Password)
		if err != nil {
			return fmt.Errorf("configfile: auth.admin.password: %w", err)
		}
		a.Admin.password = v
	}
	for _, s := range []struct {
		signIn *SignIn
		typ    SignInType
		kind   rolemap.Kind
	}{
		{a.OIDC, SignInOIDC, rolemap.OIDC}, {a.GitHub, SignInGitHub, rolemap.GitHub},
		{a.Forgejo, SignInForgejo, rolemap.GitHub}, {a.Gitea, SignInGitea, rolemap.GitHub}, {a.GitLab, SignInGitLab, rolemap.GitLab},
	} {
		if s.signIn == nil {
			continue
		}
		s.signIn.typ = s.typ
		where := "auth." + string(s.typ)
		clientID, err := s.signIn.ClientID.resolve(sec)
		if err != nil {
			return fmt.Errorf("configfile: %s.clientId: %w", where, err)
		}
		s.signIn.clientID = clientID
		v, err := sec.read(s.signIn.ClientSecret)
		if err != nil {
			return fmt.Errorf("configfile: %s.clientSecret: %w", where, err)
		}
		s.signIn.clientSecret = v
		if s.signIn.RoleMappingExpr != "" {
			if s.signIn.mapping, err = rolemap.Compile(s.kind, s.signIn.RoleMappingExpr); err != nil {
				return fmt.Errorf("configfile: %s.roleMappingExpr: %w", where, err)
			}
		}
	}
	return nil
}

func (a *Auth) validate() error {
	if a.SessionTTL != 0 && (a.SessionTTL < minSessionTTL || a.SessionTTL > maxSessionTTL) {
		return fmt.Errorf("configfile: auth.sessionTTL must be between %s and %s", minSessionTTL, maxSessionTTL)
	}
	if !a.Admin.Password.empty() && a.Admin.password.Value() == "" {
		return errors.New("configfile: auth.admin.password resolved to an empty value")
	}
	if strings.TrimSpace(a.adminUser()) == "" {
		return errors.New("configfile: auth.admin.user must not be blank")
	}
	for _, s := range a.SignIns() {
		if err := s.validate("auth." + string(s.typ)); err != nil {
			return err
		}
	}
	// A provider without a mapping only ever admits members, so without a
	// password nobody can administer the instance.
	_, _, local := a.AdminUser()
	mapped := slices.ContainsFunc(a.SignIns(), func(s *SignIn) bool { return s.mapping != nil })
	if len(a.SignIns()) > 0 && !local && !mapped {
		return errors.New("configfile: auth: no way to sign in as an admin: set auth.admin.password, or a roleMappingExpr on a sign-in")
	}
	return nil
}

func (s *SignIn) validate(where string) error {
	if s.Host != "" && !s.onInstance() {
		return fmt.Errorf("configfile: %s.host is for forge instance sign-ins", where)
	}
	switch s.typ {
	case SignInOIDC:
		if u, err := url.Parse(s.Issuer); err != nil || u.Scheme != "https" || u.Host == "" {
			return fmt.Errorf("configfile: %s.issuer %q must be an https URL", where, s.Issuer)
		}
		switch s.DefaultRole {
		case "", DefaultRoleNone, DefaultRoleMember:
		default:
			return fmt.Errorf("configfile: %s.defaultRole must be %s or %s, got %q", where, DefaultRoleNone, DefaultRoleMember, s.DefaultRole)
		}
	case SignInForgejo, SignInGitea, SignInGitLab:
		if s.Host == "" {
			return fmt.Errorf("configfile: %s.host is required", where)
		}
		if err := checkHost(s.Host); err != nil || strings.HasPrefix(s.Host, "*") {
			return fmt.Errorf("configfile: %s.host: %q must be a lowercase hostname, without a scheme, port or path", where, s.Host)
		}
		fallthrough
	case SignInGitHub:
		for _, f := range []struct {
			key string
			set bool
		}{
			{"name", s.Name != ""}, {"issuer", s.Issuer != ""}, {"scopes", len(s.Scopes) > 0},
			{"rolesClaim", s.RolesClaim != ""}, {"defaultRole", s.DefaultRole != ""},
		} {
			if f.set {
				return fmt.Errorf("configfile: %s.%s is for oidc sign-ins", where, f.key)
			}
		}
	}
	if s.clientID == "" {
		return fmt.Errorf("configfile: %s.clientId is required", where)
	}
	if s.clientSecret.Value() == "" {
		return fmt.Errorf("configfile: %s.clientSecret resolved to an empty value", where)
	}
	return nil
}
