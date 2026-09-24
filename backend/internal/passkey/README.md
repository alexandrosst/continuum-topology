# internal/passkey

This package is the one place in the module that imports `github.com/go-webauthn/webauthn`. It implements
`server.WebAuthnProvider` (declared in `backend/internal/server/webauthn.go`) so `cmd/server` can wire up real
passkey/security-key support.

## Why this is its own package

The cloud sandbox this feature was otherwise built and tested in has no network path to `proxy.golang.org`,
so it cannot fetch `go-webauthn`. Go compiles a package as a whole - one file with an unfetchable import
fails the entire package, not just that file - so the only way to keep the rest of the module buildable and
testable there was to put the dependency behind an interface (`WebAuthnProvider`) and confine the real
implementation to this package on its own. Every other package builds and its tests pass without
`go-webauthn` ever being fetched; `internal/server`'s passkey tests use a hand-written fake provider instead
(`internal/server/webauthn_test.go`).

`cmd/server/main.go` imports this package to construct the real provider, so once that wiring is in place,
`cmd/server` (and this package) are the only two things in the module that need `go-webauthn` available to
build.

## Building and testing this package

This needs to happen on a machine with a normal Go toolchain and network access to the module proxy (i.e.
your laptop, not the cloud sandbox):

```sh
cd backend
go mod tidy      # only if go.sum needs updating for this package's own imports
go build ./...
go vet ./...
go test ./internal/passkey/... -v
```

`go.mod`/`go.sum` already have `github.com/go-webauthn/webauthn` from an earlier `go get`, so `go mod tidy`
may not need to do anything - just confirm `go build ./...` succeeds for the whole module now that
`cmd/server/main.go` also imports this package.

## What to double-check while building this the first time

I wrote this against the documented API of `go-webauthn/webauthn` v0.18.2 (the version already recorded in
`go.mod`) without being able to compile it myself, so there are a few specific things worth checking once it
builds:

- **`webauthn.SessionData`'s JSON shape.** `passkey.go` marshals the `*webauthn.SessionData` that
  `BeginRegistration`/`BeginLogin` return straight to JSON and unmarshals it back in `FinishRegistration`/
  `FinishLogin`. This should just work (the fields are simple: byte slices and strings), but if `go vet` or a
  test complains about it, the fix is almost certainly just letting `encoding/json`'s default field-name
  matching do its job - nothing here depends on a particular tag spelling.
- **`protocol.AuthenticatorTransport` is a plain `type AuthenticatorTransport string`** in this version, so
  `wuser.WebAuthnCredentials()` casts your stored transport strings (`"usb"`, `"nfc"`, `"ble"`, `"internal"`,
  `"hybrid"`) directly rather than mapping them through named constants. If a future version changes this to
  a non-string type, the cast in `passkey.go` is the only place that needs to change.
- **`webauthn.New(config)` validates but does no I/O**, so building a fresh `*webauthn.WebAuthn` per call in
  `webAuthnFor` (rather than once at startup) is meant to be cheap - it exists only so the RP ID/origin can be
  derived from each request's own `Host` header (see `Admin.relyingParty` and `RelyingParty`'s doc comment).
  If a future version of the library makes `New` do real work (loading something, say), this per-call
  construction should be revisited.

`internal/passkey/passkey_test.go` covers this package's own plumbing - the JSON round-trip, the
`RelyingParty`→`Config` mapping, and the credential/transport conversions - deliberately not the WebAuthn
protocol itself (challenge generation, attestation/assertion verification), which is `go-webauthn`'s own
responsibility and already has its own test suite upstream. If those tests pass, this package is almost
certainly wired up correctly; if they don't, the failure should point straight at whichever assumption above
was wrong.

## After this builds and passes

Once `go build ./...` and `go test ./...` succeed for the whole module with this package in place, the
`backend/vendor/` directory left over from an earlier abandoned `go mod vendor` attempt is no longer needed
and can be deleted (`rm -rf backend/vendor`) - this module is not vendored, and that tree was never committed.
