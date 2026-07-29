// Package safehttp provides an HTTP client that refuses to connect to non-public
// network addresses, closing the SSRF class for operator-supplied URLs.
//
// The platform makes server-side HTTP requests to URLs a tenant controls: webhook
// delivery targets and policy HTTP-action endpoints. A default http.Client will
// dial ANY host — loopback (127.0.0.1, ::1), link-local (169.254.169.254, the
// cloud metadata endpoint), and private RFC1918/RFC4193 ranges — turning the
// platform into a confused deputy that reaches internal-only services on a
// tenant's behalf and, via the returned status code, into an internal port
// scanner. Proven live before this existed (ADR-SECURITY-001).
package safehttp

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"time"
)

// Client returns an *http.Client whose dialer refuses to connect to a non-public
// IP address. The check runs AFTER DNS resolution, on the exact addresses that
// will be dialed, so a public hostname that resolves to a private address — DNS
// rebinding — is blocked as well. When allowPrivate is true the guard is disabled
// (for deployments whose webhook targets are legitimately on a private network,
// an explicit, operator-chosen opt-in); the secure default is false.
func Client(timeout time.Duration, allowPrivate bool) *http.Client {
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			DialContext:           guardedDialContext(dialer, allowPrivate),
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          100,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
		},
	}
}

// guardedDialContext resolves the host and refuses the connection if any resolved
// address is non-public, then dials the exact validated address (never a
// re-resolved one) so the IP that was checked is the IP that is dialed.
func guardedDialContext(dialer *net.Dialer, allowPrivate bool) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		if allowPrivate {
			return dialer.DialContext(ctx, network, addr)
		}
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, err
		}
		if len(ips) == 0 {
			return nil, fmt.Errorf("safehttp: no addresses for %q", host)
		}
		// Every resolved address must be public; a single private one (rebinding
		// or a round-robin trap) fails the whole connection, fail-closed.
		for _, ip := range ips {
			if !IsPublicIP(ip.IP) {
				return nil, &BlockedError{Host: host, IP: ip.IP.String()}
			}
		}
		return dialer.DialContext(ctx, network, net.JoinHostPort(ips[0].IP.String(), port))
	}
}

// BlockedError is returned when a dial is refused for targeting a non-public
// address. It is a distinct type so callers can recognise an SSRF block.
type BlockedError struct {
	Host string
	IP   string
}

func (e *BlockedError) Error() string {
	return fmt.Sprintf("safehttp: refusing to connect to non-public address %s (host %q) — SSRF guard", e.IP, e.Host)
}

// IsPublicIP reports whether ip is a globally-routable unicast address that the
// platform may connect to. Everything else — loopback, link-local (incl. the
// 169.254.169.254 cloud-metadata address), private RFC1918/RFC4193, unspecified,
// multicast, and IPv4 CGNAT (100.64.0.0/10) — is refused.
func IsPublicIP(ip net.IP) bool {
	if ip == nil {
		return false
	}
	if ip.IsLoopback() || ip.IsUnspecified() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsPrivate() {
		return false
	}
	if v4 := ip.To4(); v4 != nil {
		// 100.64.0.0/10 — carrier-grade NAT, not covered by IsPrivate.
		if v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127 {
			return false
		}
	}
	return true
}

// ValidateWebhookURL rejects, at creation time, URLs the platform must never
// dial: a non-http(s) scheme (file, gopher, ...) or a missing host. IP-level
// blocking is enforced authoritatively at dial time (guardedDialContext), because
// a hostname's resolution can change between creation and delivery.
func ValidateWebhookURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("url scheme must be http or https, got %q", u.Scheme)
	}
	if u.Hostname() == "" {
		return fmt.Errorf("url must have a host")
	}
	return nil
}
