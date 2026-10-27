// Command excalidraw-server is the board storage API for self-hosted
// excalidraw-app.
//
//	excalidraw-server serve
//	excalidraw-server user create --email a@b.c --name "Name"
//	excalidraw-server user passwd --email a@b.c
//	excalidraw-server user list
//	excalidraw-server healthcheck
//
// Passwords are read from the terminal (or a single line of stdin when piped),
// never from arguments, so they don't end up in shell history.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"golang.org/x/term"

	"github.com/rramadhan11/excalidraw/excalidraw-server/internal/auth"
	"github.com/rramadhan11/excalidraw/excalidraw-server/internal/db"
	"github.com/rramadhan11/excalidraw/excalidraw-server/internal/server"
)

const usage = `usage:
  excalidraw-server serve
  excalidraw-server user create --email EMAIL --name NAME
  excalidraw-server user passwd --email EMAIL
  excalidraw-server user list
  excalidraw-server healthcheck

environment:
  DATABASE_URL         postgres connection string (required)
  LISTEN_ADDR          default ":8080"
  COOKIE_SECURE        default "true"; set "false" only for local http dev
  TRUST_PROXY_HEADERS  default "false"; "true" behind nginx to read X-Real-IP
  LOG_LEVEL            debug|info|warn|error, default "info"
`

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, usage)
		return errors.New("missing command")
	}
	switch args[0] {
	case "serve":
		return serve()
	case "user":
		return userCommand(args[1:])
	case "healthcheck":
		return healthcheck()
	default:
		fmt.Fprint(os.Stderr, usage)
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func envBool(name string, def bool) (bool, error) {
	raw := os.Getenv(name)
	if raw == "" {
		return def, nil
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("%s: %w", name, err)
	}
	return v, nil
}

func envOr(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

func databaseURL() (string, error) {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		return "", errors.New("DATABASE_URL is required")
	}
	return url, nil
}

func newLogger() (*slog.Logger, error) {
	var level slog.Level
	if err := level.UnmarshalText([]byte(envOr("LOG_LEVEL", "info"))); err != nil {
		return nil, fmt.Errorf("LOG_LEVEL: %w", err)
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level})), nil
}

func serve() error {
	logger, err := newLogger()
	if err != nil {
		return err
	}
	dbURL, err := databaseURL()
	if err != nil {
		return err
	}
	cookieSecure, err := envBool("COOKIE_SECURE", true)
	if err != nil {
		return err
	}
	trustProxy, err := envBool("TRUST_PROXY_HEADERS", false)
	if err != nil {
		return err
	}
	if !cookieSecure {
		logger.Warn("COOKIE_SECURE=false: session cookies are sent over plain http, use only for local development")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	applied, err := db.Migrate(ctx, dbURL)
	if err != nil {
		return err
	}
	for _, m := range applied {
		logger.Info("migration applied", slog.String("source", m.Source.Path))
	}

	pool, err := db.Open(ctx, dbURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	go cleanupSessions(ctx, auth.NewSessions(pool), logger)

	srv := &http.Server{
		Addr:              envOr("LISTEN_ADDR", ":8080"),
		Handler:           server.NewHandler(pool, server.Config{CookieSecure: cookieSecure, TrustProxy: trustProxy}, logger),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       2 * time.Minute,
		WriteTimeout:      2 * time.Minute,
		IdleTimeout:       2 * time.Minute,
	}

	errCh := make(chan error, 1)
	go func() {
		logger.Info("listening", slog.String("addr", srv.Addr))
		errCh <- srv.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}
	logger.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}

func cleanupSessions(ctx context.Context, sessions *auth.Sessions, logger *slog.Logger) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		if n, err := sessions.DeleteExpired(ctx); err != nil && ctx.Err() == nil {
			logger.Error("delete expired sessions", slog.Any("err", err))
		} else if n > 0 {
			logger.Info("deleted expired sessions", slog.Int64("count", n))
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func healthcheck() error {
	addr := envOr("LISTEN_ADDR", ":8080")
	if strings.HasPrefix(addr, ":") {
		addr = "127.0.0.1" + addr
	}
	client := http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("http://" + addr + "/healthz")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unhealthy: %s", resp.Status)
	}
	return nil
}

func userCommand(args []string) error {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, usage)
		return errors.New("missing user subcommand")
	}
	fs := flag.NewFlagSet("user "+args[0], flag.ContinueOnError)
	email := fs.String("email", "", "user email")
	name := fs.String("name", "", "display name")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}

	dbURL, err := databaseURL()
	if err != nil {
		return err
	}
	ctx := context.Background()
	if _, err := db.Migrate(ctx, dbURL); err != nil {
		return err
	}
	pool, err := db.Open(ctx, dbURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	users := auth.NewUsers(pool)

	switch args[0] {
	case "create":
		if *email == "" || *name == "" {
			return errors.New("--email and --name are required")
		}
		password, err := readPassword()
		if err != nil {
			return err
		}
		user, err := users.Create(ctx, *email, *name, password)
		if err != nil {
			return err
		}
		fmt.Printf("created user %s (%s)\n", user.Email, user.ID)
		return nil

	case "passwd":
		if *email == "" {
			return errors.New("--email is required")
		}
		password, err := readPassword()
		if err != nil {
			return err
		}
		if err := users.SetPassword(ctx, *email, password); err != nil {
			return err
		}
		fmt.Println("password updated; existing sessions were signed out")
		return nil

	case "list":
		list, err := users.List(ctx)
		if err != nil {
			return err
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "EMAIL\tNAME\tCREATED")
		for _, u := range list {
			fmt.Fprintf(tw, "%s\t%s\t%s\n", u.Email, u.Name, u.CreatedAt.Format(time.DateOnly))
		}
		return tw.Flush()

	default:
		fmt.Fprint(os.Stderr, usage)
		return fmt.Errorf("unknown user subcommand %q", args[0])
	}
}

// readPassword prompts twice on a terminal, or reads one line from piped stdin.
func readPassword() (string, error) {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && line == "" {
			return "", fmt.Errorf("read password from stdin: %w", err)
		}
		password := strings.TrimRight(line, "\r\n")
		return password, auth.ValidatePassword(password)
	}

	fmt.Fprint(os.Stderr, "Password: ")
	first, err := term.ReadPassword(fd)
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", err
	}
	if err := auth.ValidatePassword(string(first)); err != nil {
		return "", err
	}
	fmt.Fprint(os.Stderr, "Repeat password: ")
	second, err := term.ReadPassword(fd)
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", err
	}
	if string(first) != string(second) {
		return "", errors.New("passwords do not match")
	}
	return string(first), nil
}
