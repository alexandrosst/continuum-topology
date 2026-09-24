# Continuum security review — September 2026

This is the deep review requested as Task #187, run after Task #188 (two-factor authentication: TOTP,
email-OTP, and passkeys/WebAuthn) and Task #189 (application regroup) were both implemented, so it covers
those new features alongside the rest of the codebase. It was carried out as eight parallel, adversarial
passes over distinct areas — authentication/session/2FA, authorization and multi-tenant isolation, agent
enrollment and the PKI/mTLS trust chain, outbound network calls (SSRF surface), storage-layer injection and
audit-trail integrity, cryptography and secret generation, the two Helm charts, and the frontend — each
reading the real code and the existing test suite in full rather than sampling, and each asked to report only
findings it could back with a concrete attack scenario, not stylistic nitpicks.

The overall picture: this is an unusually well-hardened codebase for its size. Several areas that are common
sources of real vulnerabilities elsewhere — SQL/Cypher injection, cross-tenant IDOR, session fixation, CSRF,
SSRF via the decider/geoip integrations, timing-based account enumeration, agent identity spoofing — turned
up nothing exploitable, largely because the existing test suite (`security_test.go`, `dos_test.go`,
`tenancy_test.go`, `decider_test.go`, and more) already pins down the specific attack scenarios a reviewer
would otherwise have to discover by hand. One finding below is worth fixing promptly (the SMTP STARTTLS
downgrade); the rest are smaller hardening improvements or already-accepted, well-documented trade-offs
worth having written down in one place.

## Findings, ranked by severity

### High — SMTP STARTTLS can be silently downgraded to plain text

`backend/internal/server/mail.go` sends mail through `net/smtp.SendMail`, which only upgrades to TLS when the
server's EHLO response advertises the `STARTTLS` extension — it has no "fail if TLS isn't available" mode. A
network-position attacker between this server and the configured `--smtp-host` can strip the `STARTTLS` line
from the plaintext EHLO reply, and `SendMail` will proceed unencrypted without complaint. `smtp.PlainAuth`'s
built-in guard refuses to send credentials over a non-TLS, non-loopback connection, so a real remote relay is
protected on the credential side — but the message body (a login or email-verification OTP code) still goes
out as unauthenticated `DATA` over that same plaintext connection either way, and if the relay is configured
as `localhost` (a common local-relay setup), even the SMTP credentials lose that protection, since Go's guard
exempts loopback addresses.

Fix: after `Hello`, check `ok, _ := c.Extension("STARTTLS")` and fail the send rather than falling through to
plaintext when it's false, or replace `smtp.SendMail` with a small wrapper around `smtp.Dial`/`smtp.Client`
that calls `StartTLS` explicitly and errors out if it's unavailable, before `Auth`/`Mail`/`Rcpt`/`Data`. Port
465 (implicit TLS) is a second option that removes the plaintext negotiation window entirely.

### Medium — `finishPasskeyLogin` has no request-body size limit

Every other pre-session auth endpoint (`login`, `login2FA`, `requestLoginEmailCode`, `beginPasskeyLogin`,
`register`, `previewInvite`) explicitly sets `r.Body = http.MaxBytesReader(w, r.Body, 4<<10)` before decoding,
and every session-gated route gets the same treatment from `guard()`. `finishPasskeyLogin`
(`backend/internal/server/admin.go`) is the one exception: it decodes a body containing a
`Response json.RawMessage` field with no size bound at all, reachable by anyone with no session and no rate
limiting gate ahead of the body read. An unauthenticated `POST` with a multi-hundred-megabyte `response` field
is buffered in full by the JSON decoder before WebAuthn validation ever runs, which is a straightforward
memory/CPU exhaustion path. Fix is one line: add the same `http.MaxBytesReader` call its sibling
`beginPasskeyLogin` already has.

### Low-Medium — TOTP codes have no anti-replay window

`VerifyTOTP` (`backend/internal/server/totp.go`) accepts any code within counter±1 (a 90-second window) but
never records which counter value an account last consumed. Because `Login` mints a fresh `pendingLogin`
token on every attempt, an attacker who already has the password and who observes or intercepts one valid
6-digit code — shoulder-surfing, a compromised notification channel, a MITM'd request — can start a second,
independent login and replay that same code successfully within the window. This doesn't defeat 2FA on its
own (the password is still required), but it does defeat the "single use" property a second factor is
supposed to have. Fix: persist the last-accepted counter per account (the `users` row `SetTOTP` already
touches is a natural home) and reject any code whose counter isn't strictly greater, updating it on success.

### Low — PROXY-protocol source-address trust has no allow-list

When `--agent-behind-proxy` is set, `backend/internal/server/grpc.go`/`proxyproto.go` trust whatever address a
PROXY v1/v2 header declares for rate-limiting and the audit trail, with no check on which TCP peers may send
that header. This is documented as a network-perimeter assumption rather than a code guarantee, and it cannot
be used to spoof agent identity or bypass mTLS — only the *address* used for logging and rate limits is
affected. If the raw `--agent-listen` port is ever reachable directly (a misconfigured security group, a
compromised pod sharing the node's network), a client could forge a `PROXY TCP4 <victim-ip> ...` header to
evade rate limits or frame another address in the audit log. Worth an optional trusted-CIDR check on the
immediate TCP peer as defense in depth, and worth calling out in the deployment docs next to the equivalent
advice already given for `--decider-allow-cidrs`.

### Low — CA key decryption accepts an overly generous KDF-parameter ceiling

`backend/internal/pki/keystore.go` accepts a header-declared Argon2id `memory` parameter up to 1 GiB (and
`time`/`threads` up to 10/16) from the encrypted CA key file being decrypted, before the passphrase is even
checked. Exploiting this needs an attacker who can already substitute the on-disk key file — a serious
compromise in its own right — so this is a defense-in-depth gap, not a primary vulnerability: it just lets
that attacker additionally force the server to allocate up to 1 GiB and burn CPU on every decrypt (e.g. every
restart). Lowering `maxKDFMemoryKiB`/`maxKDFTime` to something like 256 MiB / 5 would still comfortably exceed
what this project's own key-writing path ever produces (64 MiB / t=3 / p=4) while closing the self-inflicted
DoS knob.

### Low — the optional flow-collector DaemonSet is a materially larger blast radius (by design, off by default)

`backend/internal/chart/continuum-agent/templates/flow.yaml` runs the opt-in flow collector as root with
`hostNetwork`, `hostPID` (when byte-level flow capture is on), and `seccompProfile: Unconfined`, plus
`CAP_BPF`/`CAP_PERFMON`/`CAP_SYS_RESOURCE` — a real departure from the otherwise strict
non-root/read-only/all-capabilities-dropped posture the rest of both charts hold to. It's disabled by default
and clearly commented as a necessary trade-off for eBPF-based flow capture, not an oversight, but it's worth
having written down explicitly for anyone evaluating "what can the agent do at its most permissive": a
compromise of this one container, when enabled, could see other pods' processes and sockets on the same node.
Relatedly, its receiver port defaults to open (`0.0.0.0/0` inside the cluster) whenever flow is turned on and
`flowObserver.networkPolicy` isn't separately set — reports are HMAC-signed so forged data is rejected, but
the port itself accepts probes/floods from any pod on the cluster network. Defaulting
`flowObserver.networkPolicy: true` alongside `flowObserver.enabled: true` would close that without changing
the feature's behavior.

### Informational — a handful of already-accepted, already-documented trade-offs

A few things came up that don't need action, only awareness, because the code already says as much: the
audit hash chain (`backend/internal/store/auditchain.go`) proves internal self-consistency and catches
accidental corruption or a live tampering attempt through the API, but by its own design comment cannot
detect a rewrite performed by someone with raw file access to the SQLite database itself — that would need an
external anchor (a signed export, a value written somewhere the server can't reach) which `verify-audit`'s
own guidance gestures at but nothing currently automates. Neither Helm chart sets a CPU limit (only requests
and memory limits), a deliberate choice to avoid CFS throttling that trades away protection against
noisy-neighbor CPU contention in a shared cluster — a namespace-level `LimitRange`/`ResourceQuota` is the more
common way to backstop this than a hardcoded per-pod limit. The chart's `extraArgs` escape hatch lets an
operator override flags past the chart's own install-time safety checks (e.g. around plaintext admin
exposure), but `cmd/server/main.go`'s own runtime refusal to serve plaintext on a non-loopback address still
catches it independently, so this only matters if someone is relying on the chart-level check alone. Finally,
the frontend sets no Content-Security-Policy anywhere, which is a normal thing to leave to a reverse proxy —
but since nothing in this repository sets one either, it's worth deciding deliberately (a `default-src 'self'`
policy with `connect-src` opened for whatever server URL the UI is configured against) rather than by default.
Two smaller items in the same bucket: `RequestLoginEmailCode`/`RequestEmailVerification` echo the raw
`net/smtp` error back to the caller (internal mail-relay details disclosed to someone who's already gotten
past a correct password, but not exploitable on its own — log it server-side and return a generic message
instead), and `TestNobodyReachesAnotherOrganisation`'s route sweep doesn't explicitly exercise
`agents/{id}/tier` and `agents/{id}/consent` even though the underlying handlers are correctly protected by
the same `agentInOrg` check as their siblings — a test-coverage gap worth closing for regression safety, not
a real hole.

## Reviewed and confirmed sound

Rather than list every negative result, the areas below are called out because they're exactly the kind of
thing that commonly goes wrong in a system like this, and each was checked against a concrete attack, not
just read for style:

Session handling mints a genuinely fresh secret on every successful login, 2FA completion, or passkey login,
never reuses or upgrades a pending token, and the password-only/pending-2FA state exists purely as an
in-memory token in the JSON body — never a cookie — so it can't reach any session-gated endpoint. Cookie
flags, CSRF defenses (`SameSite=Strict` plus a mandatory custom header that forces a CORS preflight), and
timing-safe comparisons for passwords, TOTP codes, recovery codes, and email codes are all correct and
covered by dedicated tests, including a dummy-hash comparison so a nonexistent username takes the same time
as a wrong password.

The WebAuthn relying-party binding is safe despite deriving RP ID/origin from the request's own `Host`
header with no fixed allow-list: the browser itself refuses to complete a ceremony whose RP ID isn't a
matching suffix of the page's real origin, so a spoofed `Host` on the server side cannot make a victim's
browser produce a credential for a different origin than it's actually on.

Authorization is enforced server-side on every request (role resolved via a live per-request membership
lookup, never cached in the session), every ID-keyed resource is scoped to the caller's own organization at
the query level, role-change logic correctly blocks self-promotion and touching an existing owner/admin from
a lesser role, invites are single-use via an atomic conditional update, and the Neo4j-backed history path
enforces tenant isolation structurally (a statement that doesn't parameterize `$org` is refused before it can
run), not just by convention.

Agent revocation is a real, continuously-checked control: every RPC re-resolves the agent's live status from
the database, so a revoked or rejected agent is rejected on its very next call regardless of how long its
still-valid certificate has left. Enrollment tokens and approval codes are both high-entropy, hashed or
compared in constant time, and capped on attempts before the comparison runs. The decider integration's SSRF
policy excludes cloud metadata addresses unconditionally, before and independent of whatever CIDRs an
operator allow-lists, and re-checks the address actually dialed (not just the one requested) to close a
DNS-rebinding gap.

Every SQL query site uses bound parameters; the one place SQL is built with string concatenation
(`VACUUM INTO` in `backup.go`, which SQLite gives no parameter syntax for) only ever takes CLI-flag-derived
local paths, never network input. The Cypher equivalent is enforced by a static check that a query actually
uses `$org` as a real parameter before it's allowed to run at all. Cryptographic choices throughout — Argon2id
for both passwords and the CA key, AES-256-GCM with a fresh random nonce per encryption, `crypto/rand`
everywhere a secret or code is generated, correct TOTP secret length — check out against current guidance,
and the only two `math/rand` call sites in the whole module are in test fixtures, not production code.

Both Helm charts hold to a strict least-privilege model outside the one documented exception above: RBAC is
tightly tiered with no secrets/exec/create/delete access, pods run as non-root with a read-only root
filesystem and all capabilities dropped, every secret is mounted rather than placed in a literal env value,
service account tokens are unmounted everywhere nothing talks to the Kubernetes API, and the chart refuses to
install a plaintext-exposed admin port by default — matching the server binary's own independent runtime
refusal to do the same thing.

The frontend has no `dangerouslySetInnerHTML`, `innerHTML`, or equivalent anywhere in the source tree,
including in the topology visualization that renders agent-supplied cluster and workload names — those go
through ordinary JSX text interpolation, which React escapes unconditionally. Nothing sensitive (a password,
session token, or 2FA/recovery code) is ever placed in a URL query string, written to `localStorage`, or
logged to the console, and the new WebAuthn helper (`src/lib/webauthn.ts`) uses the spec-defined
`parseCreationOptionsFromJSON`/`parseRequestOptionsFromJSON`/`toJSON()` conversions rather than a hand-rolled
base64url/ArrayBuffer implementation, which is exactly where that kind of code most often introduces subtle
bugs.

## Suggested order to work through this

If only one thing gets fixed immediately, it should be the SMTP STARTTLS downgrade — it's the one finding
with a genuinely realistic path to credential or OTP-code disclosure. The passkey-login body-size limit is a
one-line fix worth doing in the same pass. The TOTP replay window, the PROXY-protocol allow-list, and the CA
key KDF ceiling are all worth doing but lower urgency, since each needs an attacker who already has something
significant (the password, network-path access to the raw agent port, or write access to the key file
itself). Everything in the informational section is a "write it down and decide on purpose" item rather than
a bug: the audit chain's DB-tamper caveat, the missing CPU limits, the `extraArgs` escape hatch, and the
absent CSP are all things worth a one-line decision in the docs rather than urgent code changes.
