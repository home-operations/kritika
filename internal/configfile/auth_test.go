package configfile

import (
	"slices"
	"strings"
	"testing"
	"time"
)

const authMinimal = `auth:
  sessionTTL: 8h
  admin:
    password: { env: TEST_ADMIN_PASSWORD }
  oidc:
    name: Zitadel
    issuer: https://sso.example.com
    clientId: kritika
    clientSecret: { env: TEST_WEBHOOK_SECRET }
    scopes: [openid, profile, email]
    rolesClaim: "urn:zitadel:iam:org:project:1:roles"
    roleMappingExpr: '"kritika-admin" in roles ? "admin" : ("kritika-user" in roles ? "member" : "")'
    defaultRole: none
  github:
    clientId: Iv1.x
    clientSecret: { env: TEST_WEBHOOK_SECRET }
    roleMappingExpr: 'login == "user-1" ? "admin" : ""'
`

func authEnv(t *testing.T) {
	t.Helper()
	t.Setenv("TEST_PRIVATE_KEY", "key")
	t.Setenv("TEST_WEBHOOK_SECRET", "whsec")
	t.Setenv("TEST_ADMIN_PASSWORD", "hunter2")
}

func TestAuth(t *testing.T) {
	authEnv(t)
	f, err := loadBytes(t, []byte(authMinimal+minimal))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	a := f.Auth
	if user, pw, ok := a.AdminUser(); !ok || user != DefaultAdminUser || pw.Value() != "hunter2" {
		t.Fatalf("AdminUser = %q, %v", user, ok)
	}
	oidc, ok := a.SignInByType(SignInOIDC)
	if !ok || oidc.Label() != "Zitadel" || oidc.ClientSecretValue().Value() != "whsec" || oidc.Mapping() == nil || oidc.MembersByDefault() {
		t.Fatalf("oidc = %+v, %v", oidc, ok)
	}
	gh, ok := a.SignInByType(SignInGitHub)
	if !ok || gh.Label() != "GitHub" || gh.Type() != SignInGitHub || gh.Mapping() == nil {
		t.Fatalf("github = %+v, %v", gh, ok)
	}
	if got := a.SignIns(); len(got) != 2 || got[0] != oidc || got[1] != gh {
		t.Fatalf("SignIns = %v", got)
	}
	if a.SessionTTLOrDefault() != 8*time.Hour || (&Auth{}).SessionTTLOrDefault() != DefaultSessionTTL || DefaultSessionTTL != 12*time.Hour {
		t.Fatal("session ttl")
	}
	if !a.Configured() || (&Auth{}).Configured() {
		t.Fatal("Configured")
	}
}

func TestAuthClientID(t *testing.T) {
	authEnv(t)
	t.Setenv("TEST_SIGN_IN_CLIENT_ID", "from-env\r\n")
	t.Setenv("TEST_EMPTY", "")
	for _, provider := range []struct {
		typ    SignInType
		inline string
	}{{SignInOIDC, "kritika"}, {SignInGitHub, "Iv1.x"}} {
		t.Run(string(provider.typ), func(t *testing.T) {
			for _, tt := range []struct {
				name, value, want, wantErr string
			}{
				{"inline", "inline-id", "inline-id", ""},
				{"environment", "{ env: TEST_SIGN_IN_CLIENT_ID }", "from-env", ""},
				{"unset environment", "{ env: TEST_NOPE }", "", "auth." + string(provider.typ) + ".clientId: environment variable TEST_NOPE is not set"},
				{"empty environment", "{ env: TEST_EMPTY }", "", "auth." + string(provider.typ) + ".clientId is required"},
				{"empty reference", "{}", "", "auth." + string(provider.typ) + ".clientId is required"},
				{"unknown field", "{ env: TEST_SIGN_IN_CLIENT_ID, vault: x }", "", "field vault not found"},
			} {
				t.Run(tt.name, func(t *testing.T) {
					doc := strings.Replace(authMinimal, "clientId: "+provider.inline, "clientId: "+tt.value, 1) + minimal
					f, err := load(t, doc)
					if tt.wantErr != "" {
						if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
							t.Fatalf("error %v does not mention %q", err, tt.wantErr)
						}
						return
					}
					if err != nil {
						t.Fatal(err)
					}
					s, ok := f.Auth.SignInByType(provider.typ)
					if !ok || s.ClientIDValue() != tt.want {
						t.Fatalf("sign-in = %+v, want client ID %q", s, tt.want)
					}
					if got, want := slices.Contains(f.SecretEnv(), "TEST_SIGN_IN_CLIENT_ID"), tt.name == "environment"; got != want {
						t.Fatalf("SecretEnv = %v, want client ID environment recorded: %v", f.SecretEnv(), want)
					}
				})
			}
		})
	}
}

func TestAuthRejects(t *testing.T) {
	authEnv(t)
	t.Setenv("TEST_EMPTY", "")
	rep := func(o, n string) string { return strings.Replace(authMinimal, o, n, 1) + minimal }
	tests := []struct {
		name string
		yaml string
		want string
	}{
		{"oidc http issuer", rep("issuer: https://", "issuer: http://"), "auth.oidc.issuer"},
		{"oidc without issuer", rep("    issuer: https://sso.example.com\n", ""), "auth.oidc.issuer"},
		{"issuer on github", rep("    clientId: Iv1.x\n", "    clientId: Iv1.x\n    issuer: https://x.example.com\n"), "auth.github.issuer is for oidc"},
		{"scopes on github", rep("    clientId: Iv1.x\n", "    clientId: Iv1.x\n    scopes: [read:user]\n"), "auth.github.scopes is for oidc"},
		{"github enterprise host", rep("    clientId: Iv1.x\n", "    clientId: Iv1.x\n    host: github.example.com\n"), "field host not found"},
		{"no client id", rep("clientId: kritika", "clientId: \"\""), "auth.oidc.clientId is required"},
		{"unset secret", rep("{ env: TEST_WEBHOOK_SECRET }", "{ env: TEST_NOPE }"), "auth.oidc.clientSecret"},
		{"empty secret", rep("{ env: TEST_WEBHOOK_SECRET }", "{ env: TEST_EMPTY }"), "auth.oidc.clientSecret resolved to an empty value"},
		{"sealed secret", rep("{ env: TEST_WEBHOOK_SECRET }", "{ sealed: abc }"), "field sealed not found"},
		{"empty password", rep("TEST_ADMIN_PASSWORD", "TEST_EMPTY"), "auth.admin.password resolved to an empty value"},
		{"blank admin user", rep("  admin:\n", "  admin:\n    user: ' '\n"), "auth.admin.user must not be blank"},
		{"unknown default role", rep("defaultRole: none", "defaultRole: admin"), "auth.oidc.defaultRole must be none or member"},
		{"bad mapping", rep(`roleMappingExpr: 'login == "user-1" ? "admin" : ""'`, `roleMappingExpr: 'claims.x'`), "auth.github.roleMappingExpr"},
		{"ttl too short", rep("sessionTTL: 8h", "sessionTTL: 1m"), "auth.sessionTTL"},
		{"ttl too long", rep("sessionTTL: 8h", "sessionTTL: 800h"), "auth.sessionTTL"},
		{"the old web block", "web:\n  sessionTTL: 8h\n" + minimal, "field web not found"},
		{"no way to an admin", `auth:
  github:
    clientId: Iv1.x
    clientSecret: { env: TEST_WEBHOOK_SECRET }
` + minimal, "no way to sign in as an admin"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := loadBytes(t, []byte(tt.yaml))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error %v does not mention %q", err, tt.want)
			}
		})
	}
}
