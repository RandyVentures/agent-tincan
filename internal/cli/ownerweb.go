package cli

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/mvanhorn/agent-tincan/internal/ownerweb"
	"github.com/mvanhorn/agent-tincan/internal/relay"
)

func ownerWebCmd() *cobra.Command {
	root := &cobra.Command{Use: "owner-web", Short: "Configure the authenticated owner's text workspace"}
	var stdin bool
	hash := &cobra.Command{Use: "hash-password", Short: "Hash an owner passphrase without putting it in command arguments", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		var password []byte
		var err error
		if stdin {
			password, err = io.ReadAll(io.LimitReader(cmd.InOrStdin(), 74))
			password = []byte(strings.TrimSuffix(strings.TrimSuffix(string(password), "\n"), "\r"))
		} else {
			fd := int(os.Stdin.Fd())
			if !term.IsTerminal(fd) {
				return errors.New("use an interactive terminal, or explicitly pass --password-stdin")
			}
			fmt.Fprint(cmd.ErrOrStderr(), "Owner passphrase (16–72 bytes, hidden): ")
			password, err = term.ReadPassword(fd)
			fmt.Fprintln(cmd.ErrOrStderr())
		}
		defer clear(password)
		if err != nil {
			return err
		}
		hashed, err := ownerweb.HashPassword(password)
		if err != nil {
			return err
		}
		cmd.Println(hashed)
		return nil
	}}
	hash.Flags().BoolVar(&stdin, "password-stdin", false, "read a passphrase from stdin instead of an interactive hidden prompt")
	root.AddCommand(hash)
	var cfg ownerweb.Config
	var path string
	setup := &cobra.Command{Use: "setup", Short: "Create a private owner web config using a hidden passphrase prompt", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if path == "" || cfg.PublicURL == "" {
			return errors.New("--config and --public-url are required")
		}
		if _, err := os.Lstat(path); err == nil {
			return errors.New("config path already exists; refusing to overwrite it")
		} else if !os.IsNotExist(err) {
			return err
		}
		fd := int(os.Stdin.Fd())
		if !term.IsTerminal(fd) {
			return errors.New("run owner-web setup in an interactive terminal; do not pass a password in chat or command arguments")
		}
		fmt.Fprint(cmd.ErrOrStderr(), "New owner passphrase (16–72 bytes, hidden): ")
		password, err := term.ReadPassword(fd)
		fmt.Fprintln(cmd.ErrOrStderr())
		if err != nil {
			return err
		}
		defer clear(password)
		fmt.Fprint(cmd.ErrOrStderr(), "Repeat passphrase (hidden): ")
		repeat, err := term.ReadPassword(fd)
		fmt.Fprintln(cmd.ErrOrStderr())
		if err != nil {
			return err
		}
		defer clear(repeat)
		if subtle.ConstantTimeCompare(password, repeat) != 1 {
			return errors.New("passphrases did not match; no config written")
		}
		cfg.PasswordHash, err = ownerweb.HashPassword(password)
		if err != nil {
			return err
		}
		if err := ownerweb.SaveNew(path, cfg); err != nil {
			return err
		}
		cmd.Printf("Owner web config saved privately to %s. No server was started.\n", path)
		return nil
	}}
	setup.Flags().StringVar(&path, "config", "", "new private config file to create")
	setup.Flags().StringVar(&cfg.PublicURL, "public-url", "", "HTTPS address, optionally including a path ending in /, for the owner app")
	setup.Flags().StringVar(&cfg.Listen, "listen", "127.0.0.1:8788", "loopback listener behind your HTTPS reverse proxy")
	setup.Flags().StringVar(&cfg.Agent, "agent", "owner-web", "unused internal ask-only owner identity")
	setup.Flags().StringSliceVar(&cfg.Targets, "target", []string{"muse"}, "teammates this workspace may ask")
	root.AddCommand(setup)
	return root
}

func prepareOwnerWeb(ctx context.Context, srv *relay.Server, db *sql.DB, file string) (*http.Server, net.Listener, error) {
	if file == "" {
		return nil, nil, nil
	}
	cfg, err := ownerweb.LoadConfig(file)
	if err != nil {
		return nil, nil, err
	}
	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return nil, nil, err
	}
	fail := func(err error) (*http.Server, net.Listener, error) { ln.Close(); return nil, nil, err }
	ledger, err := ownerweb.NewLedger(db)
	if err != nil {
		return fail(err)
	}
	backend, err := srv.OwnerWebBackend(ctx, cfg.Agent, cfg.Targets)
	if err != nil {
		return fail(err)
	}
	app, err := ownerweb.New(cfg, backend, ledger)
	if err != nil {
		return fail(err)
	}
	return &http.Server{Handler: app, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 20 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}, ln, nil
}
