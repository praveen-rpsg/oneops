# ADR-SECURITY-001 — Outbound delivery refuses non-public addresses (SSRF guard)

| | |
|---|---|
| **Status** | Accepted |
| **Date** | 2026-07-25 |
| **Decider** | Acting CTO |
| **Related** | ADR-CONCURRENCY-002 (delivery), ADR-TENANCY-003 (delivery ownership) |

## Context

The platform makes server-side HTTP requests to URLs a tenant controls: webhook
delivery targets, and policy HTTP-action endpoints. Both were dialed through a
default `&http.Client{}`, whose transport resolves and connects to *any* host.
Nothing validated the URL beyond "not empty."

Attacked live. A tenant registered a webhook at
`http://127.0.0.1:9977/latest/meta-data/iam/security-credentials/role` — an
internal-only address a tenant must never be able to reach — and triggered a
governance event. The dispatcher POSTed to it: the loopback service received the
request, signed with the platform's HMAC. The platform was a confused deputy,
reaching an internal resource on the tenant's behalf. Two escalations followed
from the same primitive:

- **Credential theft.** In a cloud deployment the same webhook pointed at
  `http://169.254.169.254/…` reaches the instance metadata service and its IAM
  credentials — a POST body is discarded, but many metadata and internal
  endpoints act on a request or return data through side channels.
- **An internal scanner.** The delivery row records `last_status_code`, exposed
  to the tenant at `GET /v1/admin/webhooks/{id}/deliveries`. A `200` versus a
  connection-refused `0` is an oracle: a tenant can map internal hosts and ports
  and fingerprint services from outside the network.

SSRF is a class, not a URL: a blocklist of strings does not close it, because a
public hostname can resolve to a private address (DNS rebinding), and encodings
of `127.0.0.1` are endless. The guard has to act on the resolved IP, at the
moment of connection.

## Decision

**The platform refuses to open an outbound connection to a non-public IP address,
enforced at dial time on the resolved address.**

1. **A safe dialer (`internal/safehttp`).** `safehttp.Client` returns an
   `*http.Client` whose `DialContext` resolves the host and refuses the
   connection if *any* resolved address is non-public — loopback, link-local
   (including `169.254.169.254`), private RFC1918/RFC4193, unspecified,
   multicast, or CGNAT `100.64.0.0/10`. It then dials the exact address it
   validated, never a re-resolved one, so a name that resolves to a public IP for
   the check and a private IP for the dial (rebinding) cannot slip through. A
   refusal is a typed `*BlockedError`.

2. **Applied to every tenant-directed outbound client.** The webhook dispatcher
   and the policy HTTP-action registry are both constructed with
   `safehttp.Client`. These are the only paths that dial an operator-supplied
   URL.

3. **Secure by default, explicit opt-in.** The guard is on unless
   `ONEOPS_WEBHOOK_ALLOW_PRIVATE_TARGETS=true`. A deployment whose delivery
   targets are legitimately on a private network, with tenants trusted to address
   them, may opt in; the default refuses.

4. **Scheme validation at creation.** Webhook creation rejects a non-`http(s)`
   scheme (`file:`, `gopher:`, …) up front for fast feedback. The authoritative
   IP guard remains at dial time, because a hostname's resolution can change
   between creation and delivery.

## Consequences

**The SSRF class is closed for tenant-supplied URLs.** Verified live: the exact
webhook that reached the loopback service before the fix now reaches nothing —
the internal service received zero requests, and the delivery records
`status=failed, last_status_code=0`. Because every blocked target fails
identically (code `0`), the status-code oracle no longer distinguishes internal
hosts, so the scanner is defeated too.

**The guarantee is a network-egress guarantee, stated precisely.** The platform
will not connect to a non-public address for a tenant. It is not a claim that
every reachable public endpoint is safe, nor that a compromised DNS resolver is
defended beyond the resolve-then-dial-the-same-IP measure. Egress filtering at
the network layer remains a defence-in-depth complement, not a substitute.

**Enforcement.**

- `safehttp.TestIsPublicIP_BlocksNonPublic` covers every non-public class
  (loopback, link-local/metadata, RFC1918, ULA, unspecified, multicast, CGNAT,
  IPv4-mapped) and allows public addresses.
- `safehttp.TestClient_RefusesLoopbackDial` makes a *real* dial to a loopback
  server and asserts it is refused by default and permitted only under the
  explicit opt-in.
- `safehttp.TestValidateWebhookURL` rejects non-http(s) schemes and empty hosts.
- `arch.TestOutboundClients_AreSSRFGuarded` parses the composition root and fails
  the build if the dispatcher or policy registry is passed a bare `&http.Client{}`
  instead of a `safehttp.Client` — the exact regression that reopens the class.
  Verified to bite.

## Residual risks

- **Creation accepts a literal private IP; the dial refuses it.** For fast
  feedback the create-time check validates only the scheme, so a webhook to
  `http://10.0.0.5` is stored but never delivered (blocked at dial, recorded as a
  failure). Adding a create-time IP check when the guard is active is minor future
  UX work; it does not affect security.
- **Policy-action URLs are guarded at dial, not validated at creation.** The
  dial-time guard covers them; a create-time scheme check for policy actions is a
  small follow-up.
- **A malicious or split-horizon DNS resolver** that returns a public address to
  the guard and is trusted by the OS for the actual connection is mitigated by
  dialing the validated IP directly, but a fully hostile resolver is outside this
  control; network egress policy is the backstop.
- **Redirects.** The client follows redirects by default; each hop is re-dialed
  through the same guarded transport, so a 302 to a private address is refused at
  the next dial. This is covered by the dialer, not by URL inspection.

## The invariant

The platform never opens a connection to a non-public address on behalf of a
tenant. Safety is decided on the resolved IP at the moment of dialing — not on the
spelling of a URL — so neither an internal address, a metadata endpoint, nor a
rebinding hostname can turn delivery into a window onto the internal network.
