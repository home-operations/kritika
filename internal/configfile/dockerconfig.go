package configfile

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"sync"

	"oras.land/oras-go/v2/registry/remote/credentials"

	"github.com/home-operations/kritika/internal/egresspolicy"
)

// dockerConfig is the Docker config file that a credential is read from:
// the username and password of its host's entry under auths. The kubelet
// updates a Secret mounted as a volume in place, so the file is read again
// when it changes, and the credential rotates without a restart. Until a
// changed file reads cleanly, the credential read last stays.
type dockerConfig struct {
	path, host string

	mu               sync.Mutex
	info             os.FileInfo
	username, secret string
	// held are the credentials that the file has held, the current one
	// among them, for the mask: a rotation revokes none of them.
	held []egresspolicy.Credential
	// failed is the last read's error, so that a broken file is logged
	// once and not on every request.
	failed string
}

// load reads the host's entry as the file holds it now. The file is
// statted first: a change between the two reads it again next time.
func (d *dockerConfig) load() error {
	info, err := os.Stat(d.path)
	if err != nil {
		return err
	}
	store, err := credentials.NewFileStore(d.path)
	if err != nil {
		return err
	}
	cred, err := store.Get(context.Background(), d.host)
	switch {
	case err != nil:
		// The store's error can quote the entry's decoded auth, the secret.
		return fmt.Errorf("the auths entry for %s is not a username and password", d.host)
	case cred.RefreshToken != "" || cred.AccessToken != "":
		return fmt.Errorf("the auths entry for %s holds a token, and kritika reads a username and password alone", d.host)
	case cred.Username == "" || cred.Password == "":
		return fmt.Errorf("auths has no username and password for %s", d.host)
	}
	d.info, d.username, d.secret = info, cred.Username, cred.Password
	if !slices.ContainsFunc(d.held, func(c egresspolicy.Credential) bool {
		return c.Username == cred.Username && c.Secret == cred.Password
	}) {
		d.held = append(d.held, egresspolicy.Credential{Username: cred.Username, Secret: cred.Password})
	}
	return nil
}

// heldCredentials are the credentials that the file has held.
func (d *dockerConfig) heldCredentials() []egresspolicy.Credential {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Clone(d.held)
}

// current is the username and password as the file holds them, or as it
// held them when it last read cleanly.
func (d *dockerConfig) current() (username, secret string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if info, err := os.Stat(d.path); err == nil && os.SameFile(d.info, info) &&
		info.ModTime().Equal(d.info.ModTime()) && info.Size() == d.info.Size() {
		return d.username, d.secret
	}
	if err := d.load(); err != nil {
		if err.Error() != d.failed {
			d.failed = err.Error()
			slog.Warn("configfile: a Docker config did not read, so the credential read from it last stays",
				"host", d.host, "path", d.path, "error", err)
		}
		return d.username, d.secret
	}
	d.failed = ""
	return d.username, d.secret
}
