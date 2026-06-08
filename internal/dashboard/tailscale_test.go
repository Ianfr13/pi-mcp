package dashboard

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestParseTailscaleIP_Valid(t *testing.T) {
	ip, err := parseTailscaleIP([]byte("100.101.102.103\nfd7a::1\n"))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if ip != "100.101.102.103" {
		t.Errorf("ip=%q", ip)
	}
}

func TestParseTailscaleIP_RejectsNonCGNAT(t *testing.T) {
	if _, err := parseTailscaleIP([]byte("192.168.1.5\n")); err == nil {
		t.Errorf("LAN IP must be rejected")
	}
	if _, err := parseTailscaleIP([]byte("\n")); err == nil {
		t.Errorf("empty must be rejected")
	}
}

func TestWaitForTailscaleIP_RetriesThenSucceeds(t *testing.T) {
	calls := 0
	detect := func() (string, error) {
		calls++
		if calls < 3 {
			return "", errors.New("not up")
		}
		return "100.64.0.9", nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ip, err := waitForTailscaleIP(ctx, detect, time.Millisecond)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if ip != "100.64.0.9" || calls != 3 {
		t.Errorf("ip=%q calls=%d", ip, calls)
	}
}

func TestWaitForTailscaleIP_ContextCancel(t *testing.T) {
	detect := func() (string, error) { return "", errors.New("never") }
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := waitForTailscaleIP(ctx, detect, time.Millisecond); err == nil {
		t.Errorf("canceled ctx should error")
	}
}
