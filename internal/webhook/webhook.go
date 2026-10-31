// Package webhook does signature verification on inbound forge webhook
// requests and parses them. Verification uses crypto/hmac rather than a forge
// SDK: it is the canonical, auditable primitive, and one forge-neutral place
// to add another forge's scheme.
package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/home-operations/kritika/internal/configfile"
)

// Verification outcomes. Callers map any non-nil error to HTTP 401.
var (
	ErrNoSecret          = errors.New("webhook: no secret configured")
	ErrMissingSignature  = errors.New("webhook: missing signature header")
	ErrSignatureMismatch = errors.New("webhook: signature mismatch")
)

// Verify reports whether an inbound webhook request is authentic for forge,
// given the connection's secret, the request headers, and the raw body. It
// returns nil when authentic. Otherwise it returns one of the sentinel errors
// above.
func Verify(forge configfile.Forge, secret string, header http.Header, body []byte) error {
	if secret == "" {
		return ErrNoSecret
	}
	switch forge {
	case configfile.ForgeGitHub:
		// X-Hub-Signature-256: "sha256=" + hex(HMAC-SHA256(body, secret)).
		return verifyHMAC(header.Get("X-Hub-Signature-256"), secret, body)
	case configfile.ForgeForgejo:
		// X-Forgejo-Signature: hex(HMAC-SHA256(body, secret)), no prefix.
		// Forgejo sends X-Hub-Signature-256 too, but its own header is
		// the one that says a Forgejo sent the delivery.
		sig := header.Get("X-Forgejo-Signature")
		if sig != "" {
			sig = "sha256=" + sig
		}
		return verifyHMAC(sig, secret, body)
	case configfile.ForgeGitea:
		// X-Gitea-Signature, as Forgejo's own header: Gitea sends no
		// X-Forgejo-* header, and Forgejo sends X-Gitea-* too.
		sig := header.Get("X-Gitea-Signature")
		if sig != "" {
			sig = "sha256=" + sig
		}
		return verifyHMAC(sig, secret, body)
	case configfile.ForgeGitLab:
		return verifyGitLab(secret, header, body, time.Now())
	default:
		return fmt.Errorf("webhook: unsupported forge %q", forge)
	}
}

// gitlabTolerance is how far a GitLab delivery's timestamp can be from now:
// the signature covers it, so an older one is a replay.
const gitlabTolerance = 5 * time.Minute

// verifyGitLab does signature verification on a delivery signed with a GitLab
// webhook's signing token: webhook-signature is one or more space-separated
// "v1," + base64 HMAC-SHA256 values over
// "{webhook-id}.{webhook-timestamp}.{body}", keyed by the base64 that the
// token carries after its "whsec_" prefix. A webhook with only a secret
// token, which GitLab sends in plain text, is unsigned.
func verifyGitLab(secret string, header http.Header, body []byte, now time.Time) error {
	provided := header.Get("webhook-signature")
	if provided == "" {
		return ErrMissingSignature
	}
	key, err := GitLabSigningKey(secret)
	if err != nil {
		return err
	}
	id, stamp := header.Get("webhook-id"), header.Get("webhook-timestamp")
	sec, err := strconv.ParseInt(stamp, 10, 64)
	if id == "" || err != nil {
		return ErrSignatureMismatch
	}
	if d := now.Sub(time.Unix(sec, 0)); d > gitlabTolerance || d < -gitlabTolerance {
		return ErrSignatureMismatch
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(id + "." + stamp + "."))
	mac.Write(body)
	want := mac.Sum(nil)
	for sig := range strings.FieldsSeq(provided) {
		encoded, ok := strings.CutPrefix(sig, "v1,")
		if !ok {
			continue
		}
		if got, err := base64.StdEncoding.DecodeString(encoded); err == nil && hmac.Equal(got, want) {
			return nil
		}
	}
	return ErrSignatureMismatch
}

// GitLabSigningKey is the HMAC key a GitLab webhook signing token holds:
// the base64 after its "whsec_" prefix, decoded.
func GitLabSigningKey(token string) ([]byte, error) {
	encoded, ok := strings.CutPrefix(token, "whsec_")
	key, err := base64.StdEncoding.DecodeString(encoded)
	if !ok || err != nil || len(key) == 0 {
		return nil, errors.New(`webhook: a GitLab signing token is "whsec_" followed by base64`)
	}
	return key, nil
}

// Delivered reports whether the request carries the headers that forge puts
// on every delivery, signed or not. So a request from anyone else can be told
// from one that the forge sent.
func Delivered(forge configfile.Forge, header http.Header) bool {
	switch forge {
	case configfile.ForgeGitHub:
		return header.Get("X-GitHub-Delivery") != "" && header.Get("X-GitHub-Event") != ""
	case configfile.ForgeForgejo:
		return header.Get("X-Forgejo-Delivery") != "" && header.Get("X-Forgejo-Event") != ""
	case configfile.ForgeGitea:
		return header.Get("X-Gitea-Delivery") != "" && header.Get("X-Gitea-Event") != ""
	case configfile.ForgeGitLab:
		return header.Get("webhook-id") != "" && header.Get("X-Gitlab-Event") != ""
	default:
		return false
	}
}

// verifyHMAC does signature verification on a "sha256=" + hex HMAC-SHA256
// signature header. The comparison is constant-time (hmac.Equal).
func verifyHMAC(provided, secret string, body []byte) error {
	if provided == "" {
		return ErrMissingSignature
	}
	digest, ok := strings.CutPrefix(provided, "sha256=")
	if !ok {
		return ErrSignatureMismatch
	}
	got, err := hex.DecodeString(digest)
	if err != nil {
		return ErrSignatureMismatch
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	if !hmac.Equal(got, mac.Sum(nil)) {
		return ErrSignatureMismatch
	}
	return nil
}
