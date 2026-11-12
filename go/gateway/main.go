// fleet-gateway: the only process that uses API key values at run time (docs/designs/fleet-connections.md §3).
// Ported from crates/gateway: loopback socket, snapshot adoption every second with a retained last-valid copy,
// disabled-key persistence, provider error mapping, a spool file and a lost counter. The `guard` command is the
// Cloudflare Access origin verifier (docs/designs/fleet-stack.md L4).
package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// version is set at build time with -ldflags "-X main.version=<commit>".
var version = "dev"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "fleet-gateway: %v\n", err)
		os.Exit(2)
	}
}

func run(args []string) error {
	switch {
	case len(args) == 1 && args[0] == "selftest":
		if _, err := parseSnapshot([]byte(`{"schema":2,"generated_ms":1,"providers":{},"connections":{},"tokens":[]}`)); err != nil {
			return err
		}
		if day(0) != "1970-01-01" || day(1_762_732_800_000) != "2025-11-10" {
			return errors.New("selftest: day is wrong")
		}
		fmt.Printf("fleet-gateway %s: selftest ok\n", version)
		return nil
	case len(args) == 2 && args[0] == "serve":
		config, err := loadConfig(args[1])
		if err != nil {
			return err
		}
		listener, err := listen(config.Listen)
		if err != nil {
			return err
		}
		return serve(config, listener, terminated())
	case len(args) >= 1 && args[0] == "guard":
		address := "127.0.0.1:9471"
		if len(args) == 2 {
			address = args[1]
		}
		secret := os.Getenv("FLEET_BROWSER_SECRET")
		if secret == "" {
			return errors.New("guard: FLEET_BROWSER_SECRET is required")
		}
		listener, err := listen(address) // fleet-access-guard.socket holds the port across restarts
		if err != nil {
			return err
		}
		server := &http.Server{Handler: newGuard(secret, accessIssuer+"/cdn-cgi/access/certs"), ReadHeaderTimeout: 10 * time.Second}
		drained := make(chan struct{})
		go func() {
			defer close(drained)
			<-terminated()
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second) // in-flight checks finish
			defer cancel()
			server.Shutdown(ctx)
		}()
		if err := server.Serve(listener); !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		<-drained
		return nil
	}
	return errors.New("usage: fleet-gateway selftest | serve <config.json> | guard [127.0.0.1:9471]")
}

func terminated() <-chan struct{} {
	done := make(chan struct{})
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTERM, syscall.SIGINT)
	go func() { <-signals; close(done) }()
	return done
}

// listen takes a systemd-activated socket (held open while a restart drains) or binds `listen` for tests and manual runs.
func listen(address string) (net.Listener, error) {
	var listener net.Listener
	var err error
	if os.Getenv("LISTEN_PID") == fmt.Sprint(os.Getpid()) && os.Getenv("LISTEN_FDS") == "1" {
		listener, err = net.FileListener(os.NewFile(3, "systemd-socket"))
	} else if address == "" {
		return nil, errors.New("no activated socket and no listen address")
	} else {
		listener, err = net.Listen("tcp", address)
	}
	if err != nil {
		return nil, err
	}
	if tcp, ok := listener.Addr().(*net.TCPAddr); !ok || !tcp.IP.IsLoopback() {
		listener.Close()
		return nil, fmt.Errorf("the gateway binds only to loopback, not %s", listener.Addr())
	}
	return listener, nil
}
