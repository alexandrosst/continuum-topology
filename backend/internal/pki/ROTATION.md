# CA key: backup, restore and rotation

The Continuum CA (`<data-dir>/pki/ca.crt` and `ca.key`) signs every agent certificate and the
agent-facing server certificate. Whoever holds `ca.key` can issue certificates the server trusts, so
treat it like any other signing key.

## How agents pin the CA today

An install command carries `--ca-pin`. The agent trusts **only** a CA whose pin matches, before it
sends a token or a certificate signing request.

* Legacy pin: the hex SHA-256 of the whole CA certificate (`sha256:<hex>` or bare hex). It is the
  value shown in the UI and in install commands, and it is unchanged.
* Public-key pin: `sha256/<base64 or hex>` of the SHA-256 of the CA certificate's
  SubjectPublicKeyInfo (the same value `curl --pinnedpubkey` uses). It stays the same when the CA
  certificate is re-issued for the **same key** (new validity, same key). The server logs it at
  startup as `ca_spki_pin`.

The agent's TLS verifier (`pki.ClientTLS`) accepts either form. The agent's enrollment, rejoin and
renewal checks compare the returned CA with `pki.PinOf(...) != pki.NormalizePin(...)`, which only
understands the legacy form; until those call sites use `pki.PinMatches`, an agent configured with a
`sha256/...` pin will connect but fail at enrollment. Use the legacy pin for agents until then.

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

`continuum.db` (agents, tokens, audit trail) is separate. The CA is enough to keep existing agents
working; the database is needed to keep their approvals and history.

## Restore

1. Put `ca.crt` and `ca.key` back in `<data-dir>/pki/` with mode `0600` for the key.
2. Start the server with the same passphrase file if the key is encrypted. The server refuses to
   start with an encrypted key and no passphrase, and never creates a second CA over half of one.
3. Check the logged `ca_pin` equals the one your agents were installed with. If it does, agents
   reconnect without any action.

Restoring only the database onto a new CA creates a different pin: every agent will refuse the server.

## Encrypting an existing key

Add `--ca-key-passphrase-file /path/to/file` (or `CONTINUUM_CA_KEY_PASSPHRASE_FILE`) and restart. The
plaintext key is replaced by an encrypted one after it has been read back successfully. The pin does
not change. The file holds the passphrase (at least 12 characters); a single trailing newline is
ignored. Keep it `0400`/`0600` and outside the data directory (a mounted Kubernetes Secret works).
Removing the flag later does not decrypt the key; the server will refuse to start until the passphrase
is supplied again.

**The `continuum-server` Helm chart does this for you by default** (`pki.encryptAtRest`, on unless set
to `false`): it generates the passphrase itself into its own Secret, kept across upgrades and
uninstalls, and passes the flag automatically. See deploy/README.md, "Encrypting the CA key at rest",
for how to back that Secret up, bring your own instead, or turn it off.

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
installed with a `sha256/...` pin are unaffected once the agent-side checks use `PinMatches`.

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
