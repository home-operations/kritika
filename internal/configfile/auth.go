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

// Auth configures how people sign in to the dashboard and what each may do:
// a local admin, an OIDC issuer and GitHub, each optional, and each
// provider's role mapping. Secret values use explicit environment
// references.
type Auth struct {
	// SessionTTL is how long a dashboard session lasts; zero means
	// DefaultSessionTTL.
	SessionTTL time.Duration `yaml:"sessionTTL,omitempty"`
	// Admin is the local admin, signed in with a password; it exists only
	// while a password is set.
	Admin LocalAdmin `yaml:"admin,omitempty"`
	// OIDC signs in through an OpenID Connect issuer.
	OIDC *SignIn `yaml:"oidc,omitempty"`
	// GitHub signs in through a GitHub OAuth App, or a GitHub App's own
	// client, on github.com.
	GitHub *SignIn `yaml:"github,omitempty"`
}

// LocalAdmin is the username and password that sign in as an admin, for a
// first run or as a way in when every provider is down.
type LocalAdmin struct {
	// User is the admin's name; DefaultAdminUser when unset.
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
	SignInLocal  SignInType = "local"
	SignInOIDC   SignInType = "oidc"
	SignInGitHub SignInType = "github"
)

// SignIn is one identity provider people sign in through.
type SignIn struct {
	// Name labels an OIDC sign-in on the sign-in page; DefaultOIDCName when
	// unset. A GitHub sign-in is labelled GitHub.
	Name string `yaml:"name,omitempty"`
	// Issuer is an OIDC sign-in's discovery issuer URL, https only.
	Issuer       string    `yaml:"issuer,omitempty"`
	ClientID     string    `yaml:"clientId"`
	ClientSecret SecretRef `yaml:"clientSecret"`
	Scopes       []string  `yaml:"scopes,omitempty"`
	// RolesClaim names the OIDC claim whose values, as a list or as the keys
	// of a map, the role mapping sees as roles.
	RolesClaim string `yaml:"rolesClaim,omitempty"`
	// RoleMappingExpr is a CEL expression deciding what a person may do;
	// see package rolemap. A key whose value is CEL ends in Expr.
	RoleMappingExpr string `yaml:"roleMappingExpr,omitempty"`
	// DefaultRole is what an OIDC sign-in the mapping does not place gets:
	// none, refused, unless set to member, which reads every account.
	DefaultRole string `yaml:"defaultRole,omitempty"`

	typ          SignInType
	clientSecret Secret
	mapping      *rolemap.Program
}

// DefaultOIDCName labels an OIDC sign-in that names none.
const DefaultOIDCName = "SSO"

// Default roles an OIDC sign-in may give a person its mapping does not place.
const (
	DefaultRoleNone   = "none"
	DefaultRoleMember = "member"
)

// Type is the sign-in's type.
func (s *SignIn) Type() SignInType { return s.typ }

// Label is how the sign-in page labels the sign-in.
func (s *SignIn) Label() string {
	if s.typ == SignInGitHub {
		return "GitHub"
	}
	return cmp.Or(s.Name, DefaultOIDCName)
}

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
	for _, s := range []*SignIn{a.OIDC, a.GitHub} {
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
	}{{a.OIDC, SignInOIDC, rolemap.OIDC}, {a.GitHub, SignInGitHub, rolemap.GitHub}} {
		if s.signIn == nil {
			continue
		}
		s.signIn.typ = s.typ
		where := "auth." + string(s.typ)
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
	// password nobody could administer the instance.
	_, _, local := a.AdminUser()
	mapped := slices.ContainsFunc(a.SignIns(), func(s *SignIn) bool { return s.mapping != nil })
	if len(a.SignIns()) > 0 && !local && !mapped {
		return errors.New("configfile: auth: no way to sign in as an admin: set auth.admin.password, or a roleMappingExpr on a sign-in")
	}
	return nil
}

func (s *SignIn) validate(where string) error {
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
	if s.ClientID == "" {
		return fmt.Errorf("configfile: %s.clientId is required", where)
	}
	if s.clientSecret.Value() == "" {
		return fmt.Errorf("configfile: %s.clientSecret resolved to an empty value", where)
	}
	return nil
}
