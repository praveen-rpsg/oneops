package safehttp

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The SSRF guard must refuse every non-public address class and allow public
// ones. This is the regression for the class proven live (ADR-SECURITY-001):
// loopback and link-local (the cloud-metadata IP) and private ranges were all
// reachable through a default client.
func TestIsPublicIP_BlocksNonPublic(t *testing.T) {
	blocked := []string{
		"127.0.0.1", "127.0.0.53", "::1", // loopback
		"169.254.169.254", "fe80::1", // link-local (cloud metadata)
		"10.0.0.5", "172.16.0.1", "172.31.255.255", "192.168.1.1", // RFC1918
		"fd00::1",       // RFC4193 ULA
		"0.0.0.0", "::", // unspecified
		"224.0.0.1", "ff02::1", // multicast
		"100.64.0.1", "100.127.255.255", // CGNAT
		"::ffff:127.0.0.1", "::ffff:10.0.0.1", // IPv4-mapped loopback/private
	}
	for _, s := range blocked {
		ip := net.ParseIP(s)
		if ip == nil {
			t.Fatalf("bad test IP %q", s)
		}
		if IsPublicIP(ip) {
			t.Errorf("IsPublicIP(%s) = true; a non-public address must be blocked (SSRF)", s)
		}
	}

	allowed := []string{"8.8.8.8", "1.1.1.1", "93.184.216.34", "2606:2800:220:1:248:1893:25c8:1946"}
	for _, s := range allowed {
		ip := net.ParseIP(s)
		if !IsPublicIP(ip) {
			t.Errorf("IsPublicIP(%s) = false; a public address must be allowed", s)
		}
	}
	if IsPublicIP(nil) {
		t.Error("IsPublicIP(nil) must be false")
	}
}

func TestValidateWebhookURL(t *testing.T) {
	bad := []string{
		"file:///etc/passwd", "gopher://x/", "ftp://h/", "", "http://", "https://",
		"redis://127.0.0.1:6379", "//no-scheme",
	}
	for _, u := range bad {
		if err := ValidateWebhookURL(u); err == nil {
			t.Errorf("ValidateWebhookURL(%q) = nil; want rejection", u)
		}
	}
	good := []string{"http://example.com/hook", "https://api.example.com:8443/x", "http://public.host/p"}
	for _, u := range good {
		if err := ValidateWebhookURL(u); err != nil {
			t.Errorf("ValidateWebhookURL(%q) = %v; want accept", u, err)
		}
	}
}

// The guard must refuse a real dial to a loopback server (not just classify IPs),
// and must honor the explicit opt-in. This is the end-to-end transport test.
func TestClient_RefusesLoopbackDial(t *testing.T) {
	srv := httptestNewServer()
	defer srv.Close()

	// Default (secure): the dial to the loopback test server is refused.
	blocked := Client(5*time.Second, false)
	if _, err := blocked.Get(srv.URL); err == nil {
		t.Fatalf("safehttp.Client(allowPrivate=false) reached loopback %s — SSRF guard failed", srv.URL)
	} else if !strings.Contains(err.Error(), "SSRF guard") {
		t.Fatalf("expected an SSRF-guard error, got: %v", err)
	}

	// Explicit opt-in: private targets are permitted.
	allowed := Client(5*time.Second, true)
	resp, err := allowed.Get(srv.URL)
	if err != nil {
		t.Fatalf("safehttp.Client(allowPrivate=true) should reach loopback: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("opt-in dial status = %d, want 200", resp.StatusCode)
	}
}

func httptestNewServer() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) }))
}
