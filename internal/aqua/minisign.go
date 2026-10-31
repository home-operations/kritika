package aqua

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"io"
	"strings"

	"golang.org/x/crypto/blake2b"
)

// verifyMinisign makes sure that sig, a minisign signature file, is valid
// over the message that r reads, against publicKey, a minisign public key's
// base64 line. It covers the signature over the message, as itself or
// prehashed with BLAKE2b-512, and the global signature over that signature
// and the trusted comment.
func verifyMinisign(publicKey string, sig []byte, r io.Reader) error {
	pk, err := base64.StdEncoding.DecodeString(strings.TrimSpace(publicKey))
	if err != nil || len(pk) != 2+8+ed25519.PublicKeySize || string(pk[:2]) != "Ed" {
		return errors.New("minisign: the public key is not a minisign key")
	}
	keyID, key := pk[2:10], ed25519.PublicKey(pk[10:])
	lines := strings.Split(strings.ReplaceAll(string(sig), "\r\n", "\n"), "\n")
	if len(lines) < 4 || !strings.HasPrefix(lines[0], "untrusted comment:") || !strings.HasPrefix(lines[2], "trusted comment: ") {
		return errors.New("minisign: the signature file is not one")
	}
	raw, err := base64.StdEncoding.DecodeString(lines[1])
	if err != nil || len(raw) != 2+8+ed25519.SignatureSize {
		return errors.New("minisign: the signature is not one")
	}
	algorithm, signature := string(raw[:2]), raw[10:]
	if !bytes.Equal(raw[2:10], keyID) {
		return errors.New("minisign: signed with another key")
	}
	var message []byte
	switch algorithm {
	case "Ed":
		if message, err = io.ReadAll(io.LimitReader(r, maxDownloadBytes)); err != nil {
			return err
		}
	case "ED":
		h, _ := blake2b.New512(nil)
		if _, err := io.Copy(h, r); err != nil {
			return err
		}
		message = h.Sum(nil)
	default:
		return errors.New("minisign: the signature's algorithm is not one")
	}
	if !ed25519.Verify(key, message, signature) {
		return errors.New("minisign: the signature does not match")
	}
	global, err := base64.StdEncoding.DecodeString(lines[3])
	if err != nil || !ed25519.Verify(key, append(bytes.Clone(signature), strings.TrimPrefix(lines[2], "trusted comment: ")...), global) {
		return errors.New("minisign: the trusted comment's signature does not match")
	}
	return nil
}
