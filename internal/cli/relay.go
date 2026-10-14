package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/Boulea7/shellpier/internal/relay"
)

func runRelay(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	f := flags("relay", stderr)
	listen := f.String("listen", "127.0.0.1:8080", "HTTP listener behind an HTTPS reverse proxy")
	keyFile := f.String("token-file", "", "relay enrollment key file")
	initKey := f.String("init-key", "", "create a new private relay key file and exit")
	maxSessions := f.Int("max-sessions", 64, "maximum simultaneous sessions")
	maxTTL := f.Duration("max-ttl", 24*time.Hour, "maximum nonpersistent session lifetime")
	persistent := f.Bool("allow-persistent", false, "explicitly permit sessions with TTL 0")
	cert := f.String("tls-cert", "", "PEM certificate for native HTTPS")
	key := f.String("tls-key", "", "PEM private key for native HTTPS")
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() != 0 {
		return errors.New("relay accepts flags only")
	}
	if *initKey != "" {
		value, err := secret()
		if err != nil {
			return err
		}
		if err := writePrivate(*initKey, []byte(value+"\n")); err != nil {
			return err
		}
		fmt.Fprintln(stdout, "Created relay key file:", *initKey)
		return nil
	}
	if (*cert == "") != (*key == "") || *maxTTL < time.Second {
		return errors.New("provide both TLS certificate and key; max-ttl must be at least 1s")
	}
	apiKey, err := tokenKey(*keyFile)
	if err != nil {
		return err
	}
	server, err := relay.New(apiKey, *maxSessions)
	if err != nil {
		return err
	}
	server.AllowPersistent, server.MaxTTL = *persistent, *maxTTL
	defer server.Close()
	listener, err := net.Listen("tcp", *listen)
	if err != nil {
		return err
	}
	httpServer := &http.Server{Handler: server, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 8192}
	done := make(chan error, 1)
	go func() {
		if *cert != "" {
			done <- httpServer.ServeTLS(listener, *cert, *key)
		} else {
			done <- httpServer.Serve(listener)
		}
	}()
	fmt.Fprintln(stdout, "Relay listening:", listener.Addr())
	if *cert == "" {
		fmt.Fprintln(stdout, "Use an HTTPS reverse proxy before exposing this HTTP listener.")
	}
	select {
	case err := <-done:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		server.Close()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(shutdown); err != nil {
			httpServer.Close()
			return err
		}
		return nil
	}
}
