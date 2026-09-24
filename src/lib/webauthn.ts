// Thin wrapper around the browser's native WebAuthn API for passkeys/security keys. The server (see
// api.ts's beginPasskeyRegistration/beginPasskeyLogin) hands back the options object exactly as
// go-webauthn built it - challenge, credential IDs and the like as base64url strings, not ArrayBuffers,
// which is the interoperable "JSON" shape the WebAuthn Level 3 spec itself defines for exchanging these
// options over the wire. PublicKeyCredential.parseCreationOptionsFromJSON/parseRequestOptionsFromJSON and
// credential.toJSON() do the base64url<->ArrayBuffer conversion on both ends, so nothing here touches that
// encoding by hand.

/** True when this browser can actually do a passkey ceremony: both the JSON conversion helpers (Level 3,
 *  widely available since 2023) and WebAuthn itself. Checked once, before offering any passkey UI at all,
 *  so an older browser gets a plain "not supported here" instead of a confusing runtime failure. */
export function passkeysSupported(): boolean {
  return typeof PublicKeyCredential !== 'undefined' && typeof PublicKeyCredential.parseCreationOptionsFromJSON === 'function' && typeof PublicKeyCredential.parseRequestOptionsFromJSON === 'function'
}

/** Human-readable reason a ceremony didn't produce a credential: cancelled, a timeout, or anything else,
 *  told apart because "the person just said no" deserves a calmer message than a real failure. */
export function passkeyErrorMessage(err: unknown): string {
  if (err instanceof DOMException && (err.name === 'NotAllowedError' || err.name === 'AbortError')) {
    return 'Cancelled.'
  }
  return err instanceof Error ? err.message : 'That passkey could not be used.'
}

/** Runs navigator.credentials.create() against the server's registration options and returns the result
 *  ready to send back to finishPasskeyRegistration. */
export async function createPasskey(options: unknown): Promise<unknown> {
  const opts = options as { publicKey: PublicKeyCredentialCreationOptionsJSON }
  const publicKey = PublicKeyCredential.parseCreationOptionsFromJSON(opts.publicKey)
  const cred = (await navigator.credentials.create({ publicKey })) as PublicKeyCredential | null
  if (!cred) throw new Error('That passkey could not be created.')
  return (cred as unknown as { toJSON(): unknown }).toJSON()
}

/** Runs navigator.credentials.get() against the server's login options and returns the result ready to
 *  send back to finishPasskeyLogin. */
export async function getPasskey(options: unknown): Promise<unknown> {
  const opts = options as { publicKey: PublicKeyCredentialRequestOptionsJSON }
  const publicKey = PublicKeyCredential.parseRequestOptionsFromJSON(opts.publicKey)
  const cred = (await navigator.credentials.get({ publicKey })) as PublicKeyCredential | null
  if (!cred) throw new Error('That passkey could not be used.')
  return (cred as unknown as { toJSON(): unknown }).toJSON()
}
