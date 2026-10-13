package configfile

import (
	"fmt"
	"maps"
	"slices"
)

// connectionsOf lists the file's apps in the order of their names, each
// carrying its key as its name.
func connectionsOf(apps map[string]Connection) []Connection {
	conns := make([]Connection, 0, len(apps))
	for _, name := range slices.Sorted(maps.Keys(apps)) {
		in := apps[name]
		in.Name, in.Forge = name, ForgeGitHub
		conns = append(conns, in)
	}
	return conns
}

// validateConnections checks one layer's connections: names that are hook
// paths; accounts listed; credentials that resolved; and every account
// served by one connection alone.
func validateConnections(conns []Connection) error {
	served := map[string]string{}
	for i := range conns {
		in := &conns[i]
		where := "apps." + in.Name
		if !nameRe.MatchString(in.Name) {
			return fmt.Errorf("configfile: %s: an app's name must be lowercase alphanumerics and hyphens, 1 to 63 characters", where)
		}
		if err := validateAccountNames(in.Accounts, where); err != nil {
			return err
		}
		if err := in.validate(where); err != nil {
			return err
		}
		for ai, a := range in.Accounts {
			key := AccountKey(in.Forge, a)
			if other, dup := served[key]; dup {
				return fmt.Errorf("configfile: %s.accounts[%d] %q is served by app %q already; an account is served by one app",
					where, ai, a, other)
			}
			served[key] = in.Name
		}
	}
	return nil
}
