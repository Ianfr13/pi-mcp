package dashboard

import (
	"context"
	"errors"
	"net"
	"os/exec"
	"strings"
	"time"
)

// cgnat is the Tailscale CGNAT range (100.64.0.0/10) all tailnet IPv4 addresses
// fall in. Validating against it guarantees we never bind a LAN/public address.
var cgnat = func() *net.IPNet {
	_, n, _ := net.ParseCIDR("100.64.0.0/10")
	return n
}()

// tailscaleCandidates are the absolute fallbacks tried when "tailscale" is not on
// PATH (systemd user units have a minimal PATH).
var tailscaleCandidates = []string{"tailscale", "/usr/bin/tailscale", "/usr/local/bin/tailscale", "/usr/sbin/tailscale"}

// DetectTailscaleIP runs `tailscale ip -4` (trying PATH then common absolute
// locations) and returns the validated CGNAT IPv4.
func DetectTailscaleIP() (string, error) {
	var lastErr error
	for _, bin := range tailscaleCandidates {
		out, err := exec.Command(bin, "ip", "-4").Output()
		if err != nil {
			lastErr = err
			continue
		}
		return parseTailscaleIP(out)
	}
	if lastErr == nil {
		lastErr = errors.New("tailscale CLI not found")
	}
	return "", lastErr
}

// parseTailscaleIP extracts the first CGNAT IPv4 from `tailscale ip -4` output.
func parseTailscaleIP(out []byte) (string, error) {
	for _, line := range strings.Split(string(out), "\n") {
		s := strings.TrimSpace(line)
		if s == "" {
			continue
		}
		ip := net.ParseIP(s)
		if ip == nil || ip.To4() == nil {
			continue
		}
		if cgnat.Contains(ip) {
			return s, nil
		}
		return "", errors.New("tailscale ip not in CGNAT range: " + s)
	}
	return "", errors.New("no tailscale IPv4 found")
}

// waitForTailscaleIP polls detect until it returns an IP, ctx is canceled, or
// (never) — it loops on backoff. It never falls back to a non-tailnet address.
func waitForTailscaleIP(ctx context.Context, detect func() (string, error), backoff time.Duration) (string, error) {
	for {
		if ip, err := detect(); err == nil {
			return ip, nil
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(backoff):
		}
	}
}

// WaitForTailscaleIP is the production wait loop (2s backoff, logs via the
// caller). It blocks until a tailnet IP appears or ctx is canceled.
func WaitForTailscaleIP(ctx context.Context) (string, error) {
	return waitForTailscaleIP(ctx, DetectTailscaleIP, 2*time.Second)
}
