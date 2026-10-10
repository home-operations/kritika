package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/home-operations/kritika/internal/chatgpt"
	"github.com/home-operations/kritika/internal/config"
	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/store"
)

func loginChatGPT(ctx context.Context, cfg *config.Config, args []string, logger *slog.Logger) error {
	f, err := loadConfig(cfg.ConfigFile)
	if err != nil {
		return err
	}
	p, err := chatGPTProvider(f, args)
	if err != nil {
		return err
	}
	st, err := store.Open(ctx, store.Options{AppURL: cfg.DatabaseURL, Logger: logger})
	if err != nil {
		return err
	}
	defer st.Close()
	ready, err := st.SchemaReady(ctx)
	if err != nil {
		return err
	}
	if !ready {
		return fmt.Errorf("chatgpt: the schema is not ready; wait for kritika serve to apply migrations")
	}
	key := p.ChatGPTSessionKey()
	session, err := st.PrepareChatGPTSession(ctx, key)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	c, err := (chatgpt.Login{Out: os.Stdout}).Run(ctx, session.Credentials)
	if err != nil {
		return err
	}
	if err := st.ConnectChatGPTSession(ctx, key, c, session.Credentials.ClientID); err != nil {
		return err
	}
	_, err = fmt.Fprintf(os.Stdout, "\nConnected provider %s as %s. Credentials are saved in Postgres. "+
		"You can stop the port-forward.\n", key, c.Email)
	return err
}

func chatGPTProvider(f *configfile.File, args []string) (configfile.Provider, error) {
	var a *configfile.Account
	if len(args) == 2 {
		forge, name, ok := strings.Cut(args[0], "/")
		if !ok {
			return configfile.Provider{}, fmt.Errorf("chatgpt: account %q must be <forge/account>", args[0])
		}
		a, ok = f.Account(configfile.Forge(forge), name)
		if !ok {
			return configfile.Provider{}, fmt.Errorf("chatgpt: account %q is not configured", args[0])
		}
	}
	name := args[len(args)-1]
	p, ok := f.Provider(a, name)
	if !ok || p.Type != configfile.ProviderChatGPT {
		return configfile.Provider{}, fmt.Errorf("chatgpt: provider %q must be configured with type chatgpt", name)
	}
	return p, nil
}
