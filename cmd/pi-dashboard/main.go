// Command pi-dashboard is a standalone, read-only realtime viewer of pi-mcp
// workflows. It reads the registry + run files the pi-mcp server persists and
// serves a web UI over HTTP + SSE, bound to the host's Tailscale IPv4.
package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"pi-mcp/internal/config"
	"pi-mcp/internal/dashboard"
)

const defaultPort = "7777"

func main() {
	addrFlag := flag.String("addr", "", "explicit bind address host:port (default: detected Tailscale IP + :7777)")
	stateDir := flag.String("state-dir", config.StateDir(), "pi-mcp state dir (holds pi-mcp/registry.db)")
	flag.Parse()

	logger := log.New(os.Stderr, "pi-dashboard ", log.LstdFlags)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	addr, err := resolveAddrWait(ctx, *addrFlag, logger)
	if err != nil {
		logger.Fatalf("resolve bind address: %v", err)
	}

	registryPath := registryPathFor(*stateDir)

	hub := dashboard.NewHub()
	poller := dashboard.NewPoller(registryPath, *stateDir, hub)
	go poller.Run(ctx)

	srv := dashboard.NewServer(poller, hub)
	httpSrv := &http.Server{
		Addr:              addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(shutCtx)
	}()

	logger.Printf("serving on http://%s (state-dir=%s)", addr, *stateDir)
	if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		logger.Fatalf("serve: %v", err)
	}
}

// resolveAddrWait is the production path: explicit addr binds immediately; an
// empty addr WAITS for the tailnet IP (never falls back to LAN).
func resolveAddrWait(ctx context.Context, explicit string, logger *log.Logger) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	logger.Printf("waiting for tailnet IP…")
	ip, err := dashboard.WaitForTailscaleIP(ctx)
	if err != nil {
		return "", err
	}
	logger.Printf("bound tailnet IP %s", ip)
	return ip + ":" + defaultPort, nil
}

// registryPathFor resolves the registry DB path for a (possibly overridden)
// state dir. Always the canonical .db, never the legacy .json — a non-default
// --state-dir previously mis-built "registry.json" and split-brained the view.
func registryPathFor(stateDir string) string {
	return config.RegistryPathFor(stateDir)
}
