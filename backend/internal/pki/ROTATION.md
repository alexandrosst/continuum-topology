# CA key: backup, restore and rotation

The Continuum CA (`<data-dir>/pki/ca.crt` and `ca.key`) signs every agent certificate and the
agent-facing server certificate. Whoever holds `ca.key` can issue certificates the server trusts, so
treat it like any other signing key.

## How agents pin the CA today

An install command carries `--ca-pin`. The agent trusts **only** a CA whose pin matches, before it
sends a token or a certificate signing request.

* Legacy pin: the hex SHA-256 of the whole CA certificate (`sha256:<hex>` or bare hex). It is the
  form older installs carry; the agent still accepts it.
* Public-key pin: `sha256/<base64 or hex>` of the SHA-256 of the CA certificate's
  SubjectPublicKeyInfo (the same value `curl --pinnedpubkey` uses). It is the form the UI and install commands print. It stays the same when the CA
  certificate is re-issued for the **same key** (new validity, same key). The server logs it at
  startup as `ca_spki_pin`.

The agent's TLS verifier (`pki.ClientTLS`) and its enrollment, rejoin, and renewal checks
(`internal/agent/enroll.go`, `internal/agent/run.go`) all compare the returned CA with
`pki.PinMatches`, which accepts either form - a `sha256/...` pin works everywhere a legacy pin does. The renewal sidecar of a regional operator verifies the server's CA against the same pin before it sends a request.

The CA certificate is valid for ten years. After enrollment an agent stores the CA certificate it
received (it was checked against the pin), and re-checks the pin on every rejoin and renewal.

## Back up

Back up **both** files together, and the passphrase separately from them:

1. Stop the server, or copy while it is idle (the files change only when the CA is first created or
   the key is first encrypted).
2. Copy `pki/ca.crt` and `pki/ca.key` to encrypted storage. Do not put the key and its passphrase in
   the same place.
3. Record the pin (`ca_pin`, and `ca_spki_pin`) somewhere independent of the server. An install
   command is meaningless if you can no longer tell what it should pin.

If the key is encrypted (`--ca-key-passphrase-file`), the backup is encrypted too: argon2id
(64 MiB, 3 passes) with AES-256-GCM. Losing the passphrase is losing the CA.

The iperf3 credentials the server issues for cluster-link throughput tests (`link_iperf_credentials` in `continuum.db`) are sealed under a key derived from `ca.key`. A database restored with a different CA key cannot open them; the server then treats them as missing and makes new ones, which reach the clusters in their next configuration. Nothing is done on the clusters.

`continuum.db` (agents, tokens, audit trail) is separate. The CA is enough to keep existing agents
working; the database is needed to keep their approvals and history.

## Restore

1. Put `ca.crt` and `ca.key` back in `<data-dir>/pki/` with mode `0600` for the key.
2. Start the server with the same passphrase file if the key is encrypted. The server refuses to
   start with an encrypted key and no passphrase, and never creates a second CA over half of one.
3. Check the logged pin (`ca_spki_pin`, or `ca_pin` for agents installed with a legacy pin) equals the one
   your agents were installed with. If it does, agents reconnect without any action.

Restoring only the database onto a new CA creates a different pin: every agent will refuse the server.

## Encrypting an existing key

Add `--ca-key-passphrase-file /path/to/file` (or `CONTINUUM_CA_KEY_PASSPHRASE_FILE`) and restart. The
plaintext key is replaced by an encrypted one after it has been read back successfully. The pin does
not change. The file holds the passphrase (at least 12 characters); a single trailing newline is
ignored. Keep it `0400`/`0600` and outside the data directory (a mounted Kubernetes Secret works).
Removing the flag later does not decrypt the key; the server will refuse to start until the passphrase
is supplied again.

**The `continuum-server` Helm chart does this for you by default** (`pki.encryptAtRest`, on unless set
to `false`): it passes `--ca-key-auto-passphrase`, so the server mints its own passphrase with
`crypto/rand` on first start and persists it at `<data-dir>/pki/ca.passphrase`, reusing it on every
restart. See deploy/README.md, "Encrypting the CA key at rest", for what that trades away, how to back
it up, bring your own passphrase instead, or turn encryption off.

## Rotation

The pin identifies the CA, so rotating the CA means every agent must learn a new pin. There is no
in-place rotation that keeps old agents connected today. Choose by what you must rotate:

**Passphrase only.** Stop the server, move `ca.key` aside, start once with the *old* passphrase file to
confirm it opens, then re-encrypt: decrypting is not offered as a command, so the supported route is to
restore a plaintext backup of the key, start with the new passphrase file, and the key is encrypted
under it. Pin unchanged; no agent is affected.

**Certificate renewal, same key (before the ten years end).** Issue a new self-signed certificate over
the same key with the same subject and replace `ca.crt`. The legacy pin changes; a public-key pin does
not. Agents installed with a legacy pin must be re-installed (or their stored pin updated); agents
installed with a `sha256/...` pin are unaffected - this is exactly the case a public-key pin exists for.

**New key (suspected compromise).**

1. Assume everything the old key could sign is untrusted. Take a backup of the data directory.
2. Stop the server, remove `pki/ca.crt` and `pki/ca.key`, start it: a new CA and pin are created (and
   the key is born encrypted when a passphrase is configured). Agent-facing server certificates are
   issued from it automatically.
3. Every agent now fails the pin check. Generate new install commands (they carry the new pin), revoke
   the old agents in the UI, and re-install; each agent is approved again as a new agent.
4. Destroy the old key and all its backups.

Plan for step 3 to take as long as it takes to reach every cluster; there is no overlap period in which
both CAs are trusted.

## Operator CAs and operator certificates

A regional operator has its own CA, separate from the Continuum CA above. Its receiver and client
certificates last 30 days and are renewed by their holders (an install from before renewal keeps its
365-day certificates until its first renewal). Revoking an operator or a sender stops renewal; it does
not revoke a certificate, which stops working when it ends, within 30 days (up to 365 for a legacy
certificate that was never renewed). An immediate cut-off means rotating the operator CA: put the new
`ca.crt` on the receiver and restart it. There is no overlap period.

## What a certificate says about who holds it

Every certificate this server issues carries one URI name in its subject alternative names, in the SPIFFE form, next to the common
name and organisation that were always there:

| Issued to | URI name | Used for |
|---|---|---|
| an agent | `spiffe://ikhnos/org/<org>/agent/<agent id>` | the agent's connection, rejoin |
| an operator's receiver | `spiffe://ikhnos/org/<org>/operator/<operator id>/receiver` | server use |
| an operator's sender | `spiffe://ikhnos/org/<org>/operator/<operator id>/sender` | client use |

The name must agree with the common name, the organisation and the permitted use of the same certificate; one that does not is refused as
a whole and never read as an older certificate. What the name buys is that a certificate of one kind cannot be used as another: an agent's
certificate is refused where an operator's is expected, and the reverse. Three places enforce it: the agent connection, rejoin, and the renewal of operator certificates.

Certificates issued before this change have no such name. They keep working while `pki.RequireIdentityURI` is off, which is the setting
of the release that introduces the name. Agent certificates last 24 hours and operator TLS certificates 30 days, so after one release
cycle every certificate in use has the name; the release after that turns the requirement on (a certificate without a name is then
refused, and the holder is told to enrol or install again). Operator certificates from before renewal existed last 365 days; check the
operators page for any that are still on the old lifetime before turning it on.

## Rotation at a glance

| What changed | Pin changes | Agents need | Operators need | Overlap |
|---|---|---|---|---|
| passphrase of `ca.key` | no | nothing | nothing | not needed |
| CA certificate re-issued over the same key | legacy pin yes, `sha256/` pin no | re-install only if on a legacy pin | nothing | not needed |
| new CA key (compromise) | yes | re-install and approve again | nothing, unless the operator's own CA is also suspect | none |
| operator CA key | no (agents are not involved) | nothing | new `ca.crt` on the receiver, restart | none |

Two things are worth knowing before you need this:

* **The cost of a new CA key is a cluster-by-cluster re-install, not a server operation.** Agents hold nothing that lets them move to a
  new CA by themselves, because trusting a CA they have not been told to trust is exactly what the pin prevents. A controller that
  applies desired state over the existing channel could carry a new pin to an agent that still trusts the old CA, which is the main
  reason an overlap period is worth designing when the lifecycle controller is built; today there is none.
* **A 24-hour agent certificate bounds what a stolen agent key is worth** without any rotation: it stops being accepted a day after the
  agent last renewed, and revoking the agent stops the renewal sooner. Rotating the CA is for the case where the *CA* key is lost, not an
  agent's.

An exercise worth doing once, on a copy of the data directory: stop the server, remove `pki/ca.crt` and `pki/ca.key`, start it, and
check that an enrolled agent reports the pin mismatch in its log and does not send its token. That is the behaviour step 3 above
relies on.
