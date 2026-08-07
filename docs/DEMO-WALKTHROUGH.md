# OneOps — Demo Walkthrough

A ~10-minute guided tour of the OneOps console for a design partner. It assumes
the demo dataset from `make seed-demo` (see **Setup**), and follows the screens
in an order that tells one coherent story: **a small SaaS estate, watched and
governed end-to-end — operations *and* security — in one multi-tenant control
plane.**

> Honesty note for the presenter: this is a real, running platform, not a
> mockup. Everything below is live data served by the Go control plane over its
> own API. Where a capability is deliberately deferred, it says so — don't
> oversell past the line (see **What we deliberately haven't built yet**).

---

## Setup (about 60 seconds, do it before the partner joins)

Prerequisites: local Postgres running (`make up`; dev DB on host port 5435).

```bash
# 1. Run the control plane with auth disabled (single-operator demo mode)
ONEOPS_AUTH_ENABLED=false go run ./cmd/controlplane
# 2. In another shell: fresh schema + a coherent demo dataset
make db-reset
make seed-demo
# 3. Open the console
open http://localhost:8080
```

`make seed-demo` populates one tenant with: 6 assets + a dependency graph, 3
alert rules, 3 incidents (open / acknowledged / investigating), 3 users +
memberships, an on-call schedule + escalation policy, a maintenance window, 6
vulnerability findings, 3 IOCs, a detection rule, 6 security observations, 4
risks, 3 compliance controls with evidence, and 1 SAFE response rule. It prints
a summary and finishes with "Open the console at http://localhost:8080".

The console is theme-aware — toggle **Dark mode** (top-right) if you prefer it
for screen-sharing.

---

## The tour

### 1. The operational picture — "one live view of the estate" (~1 min)
**NOC / Overview.** Open on this. It's the live state of the NOC loop:
incidents by status and severity (you'll see **3 open — one each open /
acknowledged / investigating**, across critical / high / medium), alerts
firing, asset health, and who's on call. One screen, one glance.

> Point: this is a *projection over primitives*, not a stored "dashboard." Every
> number is derived live and tenant-isolated.

### 2. The estate and its blast radius — Topology (~1 min)
**Topology.** The seeded graph: `web-frontend → api-gateway → primary-db`, with
`api-gateway` also depending on `auth-service` and `cache-node`, and
`worker-queue → primary-db`. Click a node to see its incidents and health in the
side panel.

> Point: dependencies are first-class. This is what powers *dependency-aware
> suppression* and *root-cause grouping* — when `primary-db` is down, OneOps
> won't page you six times for the six things that depend on it.

### 3. The incident loop — Incidents → On-call → Escalation (~1.5 min)
**Incidents.** The three incidents; open one into the side panel to see its
timeline. Note the acknowledged one is **assigned** (Raj Shah), the investigating
one to Mei Lin — the loop is already in motion.
**On-call** shows the schedule and roster; **Escalation** the policy and tiers;
**Maintenance** the active window. This is a complete detect → page → escalate
loop, not just alerting.

### 4. The security story — the differentiator (~4 min)
Move to the **Security** section in the left nav. This is the same platform,
same incidents, same estate — now through a security lens.

- **Observations** — append-only security telemetry tied to assets (choose
  `auth-service` + `auth_failure` + Load). This is the raw signal.
- **Detection rules** — a threshold rule ("N auth-failures in a window →
  incident"). **Indicators** — the IOC watchlist (a C2 IP, an exfil domain).
  Together these turn signal into security incidents *in the same incident loop
  you just saw* — SOC and NOC are one system, not two bolt-ons.
- **Vulnerabilities — lead with this.** Switch to the **Prioritized** view. The
  ranking is **severity × asset criticality**, not raw CVSS: the `xz` backdoor
  on the *critical* `primary-db` tops the list, while a *High*-severity Redis
  vuln on the *low*-criticality `cache-node` correctly ranks **below** medium
  items. Then click a finding → **Remediate**: one click opens a tracked
  remediation incident, linked back to the finding.

  > Point: "ranked by *business* risk" is the story. A generic scanner gives you
  > 10,000 CVEs; OneOps tells you which five matter *to this estate* and lets you
  > act in one click.

- **Risk register** — switch to the **Register** view: risks scored by
  likelihood × impact (an "Unpatched internet-facing DB" at *Likely × Severe*
  lands Critical). **Compliance** — a SOC2 control opened to show its
  **append-only evidence trail** (URL + attestation records). This is
  audit-grade governance, not a spreadsheet.

- **Response rules (SOAR)** — a rule that fires a webhook/notification on a
  matching security incident. **Read the SAFE-boundary callout aloud**: OneOps
  runs *outbound-safe* automation only; destructive/autonomous response
  (isolate/block/disable) is deliberately *not* here — it's gated behind a
  machine-action attestation model. That honesty is a selling point to a
  security buyer, not a gap.

### 5. Access & governance — Administration (~1.5 min)
**Administration** section: **Identity & roles** (who you are, your effective
permissions, and the role→permission matrix — roles come from your IdP, OneOps
enforces them), **Members** and **Users** (grant/revoke, lifecycle), and
**Invitations** (invite by email → a one-time redeem link). Mention the public
`/redeem` page: an invitee accepts *without* a prior session.

> Point: this is real multi-tenancy — every screen you've seen is row-level
> isolated per tenant, enforced at the database, proven by tests.

---

## The three things to leave them with

1. **One control plane for ops *and* security.** Security detections and
   vulnerabilities flow into the *same* incident/on-call/escalation loop as
   operational alerts — no second tool, no swivel-chair.
2. **Ranked by business risk, not raw signal.** Vulnerabilities and risks are
   prioritized by *asset criticality × severity/likelihood* — the estate's
   context is built in.
3. **Honest about autonomy.** OneOps automates the safe things and draws a clear
   line at destructive machine action. For a buyer who's been burned by
   over-promising "AI SecOps," that line is trust.

---

## What we deliberately haven't built yet (say it if asked — don't dodge)

- **Behavioral/anomaly detection** (baseline/ML-driven) — deferred to the AI
  track; today's detection is threshold + IOC matching.
- **Continuous-audit automation** for compliance — controls + evidence are
  operator-maintained today; automated control checking is future work.
- **Destructive/autonomous SOAR response** — gated behind the machine-action
  attestation model by design.
- **IdP integration for invited users to actually log in** — redeem provisions
  the OneOps side; production login needs the tenant's identity provider to
  recognize the email. (Demo runs with auth disabled, so this is invisible in
  the demo.)

These are choices, not omissions — and each is where customer input should shape
the roadmap. Which is the point of the demo.
