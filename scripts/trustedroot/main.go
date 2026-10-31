// Command trustedroot writes Sigstore's public-good trusted root to the
// file it is given, as Sigstore's TUF repository publishes it. The trusted
// root is the keys of its certificate authorities, transparency logs, and
// timestamp authorities. The command fetches it and makes sure that it is
// valid against the root of trust that sigstore-go carries. The file is
// therefore only ever what that repository signed.
package main

import (
	"fmt"
	"os"

	"github.com/sigstore/sigstore-go/pkg/root"
	"github.com/sigstore/sigstore-go/pkg/tuf"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: trustedroot <file>")
		os.Exit(2)
	}
	if err := write(os.Args[1]); err != nil {
		fmt.Fprintln(os.Stderr, "trustedroot:", err)
		os.Exit(1)
	}
}

func write(path string) error {
	opts := tuf.DefaultOptions()
	opts.DisableLocalCache = true
	client, err := tuf.New(opts)
	if err != nil {
		return err
	}
	data, err := client.GetTarget("trusted_root.json")
	if err != nil {
		return err
	}
	if _, err := root.NewTrustedRootFromJSON(data); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}
