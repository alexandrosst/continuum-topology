import { ChevronsUpDown, Fingerprint, KeyRound, LogOut, Mail, Monitor, Moon, Plus, ScrollText, ShieldCheck, Sun, Ticket, Users, X } from 'lucide-react'
import QRCode from 'qrcode'
import { useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import { Link, NavLink, useLocation } from 'react-router-dom'
import { Button, CopyButton, ErrorBanner, Field, Input, Modal, PasswordInput, Select } from '@/components/ui/primitives'
import { PasswordRequirements, passwordRules } from '@/components/auth/AuthGate'
import { atLeast, ROLE_LABEL, type Passkey } from '@/lib/api'
import { bareIpHost, passkeysSupported } from '@/lib/webauthn'
import { getThemePreference, setThemePreference, type ThemePreference } from '@/lib/theme'
import { useServer } from '@/store/server'
import { useWorkspace, type SyncStatus } from '@/store/workspace'

const SYNC_LABEL: Record<SyncStatus, { text: string; tone: string }> = {
  off: { text: '', tone: '' },
  loading: { text: 'Loading workspace…', tone: 'text-nb-500' },
  saved: { text: 'All changes saved', tone: 'text-emerald-300' },
  dirty: { text: 'Unsaved changes…', tone: 'text-amber-300' },
  saving: { text: 'Saving…', tone: 'text-nb-400' },
  conflict: { text: 'Changed elsewhere', tone: 'text-red-300' },
  error: { text: 'Could not save. Retrying…', tone: 'text-red-300' },
  readonly: { text: 'Read-only access', tone: 'text-nb-400' },
  choose: { text: 'Waiting for your choice', tone: 'text-amber-300' },
}

/** A scannable QR code for an otpauth:// (or any) URI, generated entirely client-side - no image request
 * leaves the browser, which matters here since the secret is embedded in the URL. `undefined` while it is
 * still being drawn or if `value` is empty; a caller shows the text fallback (already needed for apps
 * without a camera) either way. */
function useQrDataUrl(value: string): string | undefined {
  const [url, setUrl] = useState<string>()
  useEffect(() => {
    if (!value) return // initial state is already undefined; nothing to derive yet
    let live = true
    // Always render as solid black-on-white, in its own fixed-white box below (not tinted to the app's own
    // theme): some phone camera scanners are unreliable on inverted or low-contrast QR codes, and a code that
    // has to stay scannable is not the place to experiment with theme-matching colors.
    QRCode.toDataURL(value, { margin: 1, width: 176, color: { dark: '#000000ff', light: '#ffffffff' } })
      .then((u) => { if (live) setUrl(u) })
      .catch(() => { if (live) setUrl(undefined) })
    return () => {
      live = false
    }
  }, [value])
  return url
}

function ChangePasswordModal({ onClose }: { onClose: () => void }) {
  const { changePassword, error, user } = useServer()
  const [current, setCurrent] = useState('')
  const [next, setNext] = useState('')
  const [again, setAgain] = useState('')
  const [busy, setBusy] = useState(false)
  const [done, setDone] = useState(false)
  const meetsPolicy = passwordRules(next, user?.username ?? '').every((r) => r.ok)
  const bad = !meetsPolicy || next !== again || !current

  const submit = async () => {
    setBusy(true)
    const ok = await changePassword(current, next)
    setBusy(false)
    if (ok) setDone(true)
  }

  return (
    <Modal
      open
      onClose={onClose}
      title="Change password"
      width="max-w-md"
      footer={
        done ? (
          <Button variant="primary" onClick={onClose}>Close</Button>
        ) : (
          <>
            <Button onClick={onClose}>Cancel</Button>
            <Button variant="primary" onClick={submit} disabled={busy || bad}>
              {busy ? 'Saving…' : 'Change password'}
            </Button>
          </>
        )
      }
    >
      {done ? (
        <p className="text-sm text-emerald-300">Password changed. Your other browsers were signed out.</p>
      ) : (
        <form className="space-y-4" onSubmit={(e) => { e.preventDefault(); if (!bad) void submit() }}>
          <Field label="Current password"><PasswordInput value={current} onChange={(e) => setCurrent(e.target.value)} autoComplete="current-password" autoFocus /></Field>
          <Field label="New password"><PasswordInput value={next} onChange={(e) => setNext(e.target.value)} autoComplete="new-password" /></Field>
          <PasswordRequirements password={next} username={user?.username ?? ''} />
          <Field label="New password again"><PasswordInput value={again} onChange={(e) => setAgain(e.target.value)} autoComplete="new-password" /></Field>
          {error && <ErrorBanner>{error}</ErrorBanner>}
        </form>
      )}
    </Modal>
  )
}

/** Turning two-factor authentication on: generate a secret, confirm one code from it, show the recovery codes once. */
function TwoFactorSetupModal({ onClose }: { onClose: () => void }) {
  const { setupTwoFactor, enableTwoFactor, error } = useServer()
  const [step, setStep] = useState<'loading' | 'confirm' | 'recovery'>('loading')
  const [secret, setSecret] = useState('')
  const [otpauthUrl, setOtpauthUrl] = useState('')
  const [code, setCode] = useState('')
  const [busy, setBusy] = useState(false)
  const [codes, setCodes] = useState<string[]>([])
  const qr = useQrDataUrl(otpauthUrl)

  useEffect(() => {
    void setupTwoFactor().then((r) => {
      if (r) {
        setSecret(r.secret)
        setOtpauthUrl(r.otpauthUrl)
        setStep('confirm')
      }
    })
  }, [setupTwoFactor])

  const submit = async () => {
    setBusy(true)
    const recovery = await enableTwoFactor(code)
    setBusy(false)
    if (recovery) {
      setCodes(recovery)
      setStep('recovery')
    } else {
      setCode('')
    }
  }

  return (
    <Modal
      open
      onClose={onClose}
      title="Two-factor authentication"
      width="max-w-md"
      footer={
        step === 'recovery' ? (
          <Button variant="primary" onClick={onClose}>Done</Button>
        ) : (
          <>
            <Button onClick={onClose}>Cancel</Button>
            {step === 'confirm' && (
              <Button variant="primary" onClick={submit} disabled={busy || code.trim().length !== 6}>
                {busy ? 'Checking…' : 'Turn on'}
              </Button>
            )}
          </>
        )
      }
    >
      {step === 'loading' && <p className="text-sm text-nb-500">Generating a secret…</p>}
      {step === 'confirm' && (
        <form className="space-y-4" onSubmit={(e) => { e.preventDefault(); if (code.trim().length === 6) void submit() }}>
          <p className="text-sm text-nb-400">Scan this with an authenticator app (Google Authenticator, 1Password, Authy, …), or add the key by hand, then enter the 6-digit code it shows.</p>
          {qr && (
            <div className="flex justify-center">
              <img src={qr} alt="Scan with your authenticator app" width={176} height={176} className="rounded-md border border-nb-800 bg-white p-2" data-testid="totp-qr" />
            </div>
          )}
          <Field label="Secret key">
            <div className="flex items-center gap-2">
              <code className="min-w-0 flex-1 truncate rounded-md border border-nb-800 bg-nb-925 px-3 py-2 text-sm text-nb-200" data-testid="totp-secret">{secret}</code>
              <CopyButton text={secret} />
            </div>
          </Field>
          <details className="text-xs text-nb-500">
            <summary className="cursor-pointer select-none">Or use a setup link</summary>
            <div className="mt-2 flex items-center gap-2">
              <code className="min-w-0 flex-1 truncate rounded-md border border-nb-800 bg-nb-925 px-3 py-2 text-[11px] text-nb-400">{otpauthUrl}</code>
              <CopyButton text={otpauthUrl} />
            </div>
          </details>
          <Field label="Code from the app">
            <Input value={code} onChange={(e) => setCode(e.target.value)} autoFocus inputMode="numeric" autoComplete="one-time-code" placeholder="123456" data-testid="totp-code" />
          </Field>
          {error && <ErrorBanner>{error}</ErrorBanner>}
        </form>
      )}
      {step === 'recovery' && (
        <div className="space-y-4">
          <p className="text-sm text-emerald-300">Two-factor authentication is on.</p>
          <p className="text-sm text-nb-400">
            Save these recovery codes somewhere safe. Each works once, in place of a code from your app, if you ever lose access to it. They will not be shown again.
          </p>
          <div className="grid grid-cols-2 gap-1.5 rounded-md border border-nb-800 bg-nb-925 p-3 font-mono text-sm text-nb-200" data-testid="recovery-codes">
            {codes.map((c) => <div key={c}>{c}</div>)}
          </div>
          <CopyButton text={codes.join('\n')} label="Copy all" />
        </div>
      )}
    </Modal>
  )
}

/** Turning two-factor authentication off: needs the current password, the same as changing it. */
function TwoFactorDisableModal({ onClose }: { onClose: () => void }) {
  const { disableTwoFactor, error } = useServer()
  const [password, setPassword] = useState('')
  const [busy, setBusy] = useState(false)

  const submit = async () => {
    setBusy(true)
    const ok = await disableTwoFactor(password)
    setBusy(false)
    if (ok) onClose()
    else setPassword('')
  }

  return (
    <Modal
      open
      onClose={onClose}
      title="Turn off two-factor authentication"
      description="Your account will only need a password to sign in from then on."
      width="max-w-md"
      footer={<><Button onClick={onClose}>Cancel</Button><Button variant="primary" onClick={submit} disabled={busy || !password}>{busy ? 'Turning off…' : 'Turn off'}</Button></>}
    >
      <form className="space-y-4" onSubmit={(e) => { e.preventDefault(); if (password) void submit() }}>
        <Field label="Current password"><PasswordInput value={password} onChange={(e) => setPassword(e.target.value)} autoComplete="current-password" autoFocus /></Field>
        {error && <ErrorBanner>{error}</ErrorBanner>}
      </form>
    </Modal>
  )
}

/**
 * Turning on email as a second factor. Starts on 'confirm' when an address is already on file (whether or
 * not it is verified yet - the step itself tells those two apart), otherwise 'address', so re-opening this
 * after a partial attempt never asks for an address already set. "Confirm" and "turn on" are chained under
 * one button: from here, the only reason to verify an address is to use it for codes, so there is no
 * separate step where it sits verified but still off.
 */
function EmailSetupModal({ onClose }: { onClose: () => void }) {
  const { user, requestEmailVerification, confirmEmail, enableEmailOTP, error } = useServer()
  const [step, setStep] = useState<'address' | 'confirm' | 'done'>(user?.email ? 'confirm' : 'address')
  const [email, setEmail] = useState(user?.email ?? '')
  const [confirmedAddress, setConfirmedAddress] = useState(user?.email ?? '')
  const [alreadyVerified] = useState(!!user?.emailVerified)
  const [code, setCode] = useState('')
  const [busy, setBusy] = useState(false)
  const [resent, setResent] = useState(false)

  const submitAddress = async () => {
    setBusy(true)
    const ok = await requestEmailVerification(email)
    setBusy(false)
    if (ok) {
      setConfirmedAddress(email)
      setStep('confirm')
    }
  }

  const resend = async () => {
    setBusy(true)
    const ok = await requestEmailVerification(confirmedAddress)
    setBusy(false)
    if (ok) setResent(true)
  }

  const submitCode = async () => {
    setBusy(true)
    // Skip straight to turning codes on when the address was already verified from an earlier attempt -
    // there is no fresh code to confirm in that case.
    const ok = alreadyVerified || (await confirmEmail(code))
    if (ok) {
      const enabled = await enableEmailOTP()
      setBusy(false)
      if (enabled) setStep('done')
      else setCode('')
    } else {
      setBusy(false)
      setCode('')
    }
  }

  return (
    <Modal
      open
      onClose={onClose}
      title="Turn on email codes"
      width="max-w-md"
      footer={
        step === 'done' ? (
          <Button variant="primary" onClick={onClose}>Done</Button>
        ) : (
          <>
            <Button onClick={onClose}>Cancel</Button>
            {step === 'address' && (
              <Button variant="primary" onClick={submitAddress} disabled={busy || !email.trim()}>
                {busy ? 'Sending…' : 'Send a code'}
              </Button>
            )}
            {step === 'confirm' && (
              <Button variant="primary" onClick={submitCode} disabled={busy || (!alreadyVerified && code.trim().length !== 6)}>
                {busy ? 'Checking…' : alreadyVerified ? 'Turn on' : 'Confirm & turn on'}
              </Button>
            )}
          </>
        )
      }
    >
      {step === 'address' && (
        <form className="space-y-4" onSubmit={(e) => { e.preventDefault(); if (email.trim()) void submitAddress() }}>
          <p className="text-sm text-nb-400">We will email a code to this address whenever it is used to finish signing in.</p>
          <Field label="Email address">
            <Input type="email" value={email} onChange={(e) => setEmail(e.target.value)} autoFocus autoComplete="email" placeholder="you@example.com" data-testid="email-address" />
          </Field>
          {error && <ErrorBanner>{error}</ErrorBanner>}
        </form>
      )}
      {step === 'confirm' && (
        <form className="space-y-4" onSubmit={(e) => { e.preventDefault(); if (alreadyVerified || code.trim().length === 6) void submitCode() }}>
          {alreadyVerified ? (
            <p className="text-sm text-nb-400"><strong className="text-nb-200">{confirmedAddress}</strong> is already verified. Turn codes on to use it as a second factor.</p>
          ) : (
            <>
              <p className="text-sm text-nb-400">Enter the code we emailed to <strong className="text-nb-200">{confirmedAddress}</strong>.</p>
              <Field label="Code">
                <Input value={code} onChange={(e) => setCode(e.target.value)} autoFocus inputMode="numeric" autoComplete="one-time-code" placeholder="123456" data-testid="email-code" />
              </Field>
              <button type="button" onClick={() => void resend()} disabled={busy} className="text-xs text-accent hover:text-accent/80 disabled:opacity-50">
                {resent ? 'Code sent — send another' : 'Resend code'}
              </button>
            </>
          )}
          <div>
            <button type="button" onClick={() => setStep('address')} className="text-xs text-nb-500 hover:text-nb-300">
              Use a different address
            </button>
          </div>
          {error && <ErrorBanner>{error}</ErrorBanner>}
        </form>
      )}
      {step === 'done' && <p className="text-sm text-emerald-300">Email codes are on. Signing in will now offer a code sent to {confirmedAddress}.</p>}
    </Modal>
  )
}

/** Turning email codes off: needs the current password, the same as TOTP. Leaves the address itself verified. */
function EmailDisableModal({ onClose }: { onClose: () => void }) {
  const { disableEmailOTP, error } = useServer()
  const [password, setPassword] = useState('')
  const [busy, setBusy] = useState(false)

  const submit = async () => {
    setBusy(true)
    const ok = await disableEmailOTP(password)
    setBusy(false)
    if (ok) onClose()
    else setPassword('')
  }

  return (
    <Modal
      open
      onClose={onClose}
      title="Turn off email codes"
      description="Your verified address stays on file, but signing in will not offer a mailed code again until you turn it back on."
      width="max-w-md"
      footer={<><Button onClick={onClose}>Cancel</Button><Button variant="primary" onClick={submit} disabled={busy || !password}>{busy ? 'Turning off…' : 'Turn off'}</Button></>}
    >
      <form className="space-y-4" onSubmit={(e) => { e.preventDefault(); if (password) void submit() }}>
        <Field label="Current password"><PasswordInput value={password} onChange={(e) => setPassword(e.target.value)} autoComplete="current-password" autoFocus /></Field>
        {error && <ErrorBanner>{error}</ErrorBanner>}
      </form>
    </Modal>
  )
}

/** One registered passkey, either shown plainly with rename/remove buttons, or swapped for a small inline
 *  form while one of those is in progress - a passkey has no single "are you sure" dialog the way disabling
 *  two-factor entirely does, since removing one of several is much lower-stakes than turning the whole thing
 *  off, but it still needs the password check RemovePasskey requires. */
function PasskeyRow({ passkey, busy, setBusy }: { passkey: Passkey; busy: boolean; setBusy: (b: boolean) => void }) {
  const { renamePasskey, removePasskey, error } = useServer()
  const [mode, setMode] = useState<'idle' | 'rename' | 'remove'>('idle')
  const [name, setName] = useState(passkey.name)
  const [password, setPassword] = useState('')

  const saveRename = async () => {
    setBusy(true)
    const ok = await renamePasskey(passkey.id, name)
    setBusy(false)
    if (ok) setMode('idle')
  }

  const confirmRemove = async () => {
    setBusy(true)
    const ok = await removePasskey(passkey.id, password)
    setBusy(false)
    setPassword('')
    if (ok) setMode('idle')
  }

  if (mode === 'rename') {
    return (
      <li className="rounded-md border border-nb-800 bg-nb-925 p-3">
        <form className="flex items-center gap-2" onSubmit={(e) => { e.preventDefault(); if (name.trim()) void saveRename() }}>
          <Input value={name} onChange={(e) => setName(e.target.value)} autoFocus className="h-8 flex-1 text-sm" />
          <Button type="submit" size="sm" variant="primary" disabled={busy || !name.trim()}>Save</Button>
          <Button type="button" size="sm" onClick={() => { setMode('idle'); setName(passkey.name) }}>Cancel</Button>
        </form>
        {error && <ErrorBanner className="mt-2">{error}</ErrorBanner>}
      </li>
    )
  }
  if (mode === 'remove') {
    return (
      <li className="rounded-md border border-nb-800 bg-nb-925 p-3">
        <form className="space-y-2" onSubmit={(e) => { e.preventDefault(); if (password) void confirmRemove() }}>
          <p className="text-xs text-nb-400">Enter your password to remove <strong className="text-nb-200">{passkey.name}</strong>.</p>
          <div className="flex items-center gap-2">
            <PasswordInput value={password} onChange={(e) => setPassword(e.target.value)} autoFocus autoComplete="current-password" className="h-8 flex-1 text-sm" />
            <Button type="submit" size="sm" variant="danger" disabled={busy || !password}>Remove</Button>
            <Button type="button" size="sm" onClick={() => { setMode('idle'); setPassword('') }}>Cancel</Button>
          </div>
        </form>
        {error && <ErrorBanner className="mt-2">{error}</ErrorBanner>}
      </li>
    )
  }
  return (
    <li className="flex items-center justify-between gap-2 rounded-md border border-nb-800 bg-nb-925 p-3">
      <div className="min-w-0">
        <div className="truncate text-sm text-nb-200">{passkey.name}</div>
        <div className="text-[11px] text-nb-500">
          Added {new Date(passkey.createdAt).toLocaleDateString()}
          {passkey.lastUsedAt ? `, last used ${new Date(passkey.lastUsedAt).toLocaleDateString()}` : ', never used to sign in'}
        </div>
      </div>
      <div className="flex shrink-0 gap-1">
        <Button size="sm" onClick={() => setMode('rename')}>Rename</Button>
        <Button size="sm" variant="danger" onClick={() => setMode('remove')}>Remove</Button>
      </div>
    </li>
  )
}

/**
 * Passkeys, unlike TOTP and email, have no single on/off switch to toggle - an account can hold several at
 * once (a laptop, a phone, a hardware key), so this is a small management list rather than a two-step setup
 * flow. Adding one runs the browser's own create() ceremony (see lib/webauthn) between asking for a name and
 * actually registering it.
 */
function PasskeysModal({ onClose }: { onClose: () => void }) {
  const user = useServer((s) => s.user)
  const registerPasskey = useServer((s) => s.registerPasskey)
  const error = useServer((s) => s.error)
  const [adding, setAdding] = useState(false)
  const [name, setName] = useState('')
  const [busy, setBusy] = useState(false)
  const supported = passkeysSupported()
  const bareIp = bareIpHost()
  const passkeys = user?.passkeys ?? []

  const add = async () => {
    setBusy(true)
    const ok = await registerPasskey(name)
    setBusy(false)
    if (ok) {
      setAdding(false)
      setName('')
    }
  }

  return (
    <Modal
      open
      onClose={onClose}
      title="Passkeys"
      description="A passkey or security key can finish signing in as your second factor, with no code to type."
      width="max-w-md"
      footer={<Button variant="primary" onClick={onClose}>Done</Button>}
    >
      <div className="space-y-4">
        {!supported && <ErrorBanner>Passkeys are not supported in this browser.</ErrorBanner>}
        {supported && bareIp && (
          <ErrorBanner>
            This server is reached at a bare IP address, and passkeys require a real domain name (browsers will not register one otherwise). Give it a hostname - even a private DNS
            entry or an /etc/hosts line works - or put it behind the SSO reverse-proxy setup, then come back here.
          </ErrorBanner>
        )}
        {passkeys.length === 0 && !adding && <p className="text-sm text-nb-500">No passkeys on this account yet.</p>}
        {passkeys.length > 0 && (
          <ul className="space-y-2" data-testid="passkey-list">
            {passkeys.map((p) => (
              <PasskeyRow key={p.id} passkey={p} busy={busy} setBusy={setBusy} />
            ))}
          </ul>
        )}
        {supported && !bareIp &&
          (adding ? (
            <form className="space-y-3 border-t border-nb-850 pt-4" onSubmit={(e) => { e.preventDefault(); void add() }}>
              <Field label="Name it" hint="So you can tell it apart later.">
                <Input value={name} onChange={(e) => setName(e.target.value)} autoFocus placeholder="MacBook Touch ID" data-testid="passkey-name" />
              </Field>
              <div className="flex gap-2">
                <Button type="submit" variant="primary" disabled={busy}>{busy ? 'Waiting for your passkey…' : 'Continue'}</Button>
                <Button type="button" onClick={() => setAdding(false)} disabled={busy}>Cancel</Button>
              </div>
              {error && <ErrorBanner>{error}</ErrorBanner>}
            </form>
          ) : (
            <button type="button" onClick={() => setAdding(true)} className="text-sm text-accent hover:text-accent/80" data-testid="add-passkey">
              + Add a passkey
            </button>
          ))}
      </div>
    </Modal>
  )
}

/**
 * The single entry point for "how does this account sign in with a second factor": one screen listing all
 * three methods with their current state, instead of three separately-worded menu items that each dropped
 * straight into their own setup flow (the confusing part - see TwoFactorSetupModal - was landing on a raw
 * secret key with no sense of the other choices). Picking a method closes this and opens its existing,
 * unchanged setup modal; nothing about how a method is turned on or off changes, only how a person gets there.
 */
/** One method's row in the hub above: its icon, name, current state, and the single action available on
 *  it (or none, when it is unavailable here). Module-scope, not nested in TwoFactorHubModal, so React
 *  never mistakes it for a fresh component type on every render. */
function TwoFactorMethodRow({
  icon, title, status, on, action, note,
}: { icon: ReactNode; title: string; status: string; on?: boolean; action: { label: string; onClick: () => void } | null; note?: ReactNode }) {
  return (
    <div className="flex items-start justify-between gap-3 rounded-md border border-nb-800 bg-nb-925 p-3">
      <div className="flex min-w-0 gap-2.5">
        <span className="mt-0.5 shrink-0 text-nb-500" aria-hidden>{icon}</span>
        <div className="min-w-0">
          <div className="text-sm font-medium text-nb-200">{title}</div>
          <div className={`text-xs ${on ? 'text-emerald-300' : 'text-nb-500'}`}>{status}</div>
          {note && <div className="mt-1 text-xs text-nb-500">{note}</div>}
        </div>
      </div>
      {action && <Button size="sm" className="shrink-0" onClick={action.onClick}>{action.label}</Button>}
    </div>
  )
}

function TwoFactorHubModal({ onClose, open2FA, openEmail, openPasskeys }: { onClose: () => void; open2FA: () => void; openEmail: () => void; openPasskeys: () => void }) {
  const user = useServer((s) => s.user)
  const supported = passkeysSupported()
  const bareIp = bareIpHost()
  if (!user) return null
  const passkeyCount = user.passkeys.length

  return (
    <Modal
      open
      onClose={onClose}
      title="Two-factor authentication"
      description="A second factor for signing in. Set up one method, or several - any one of them is enough."
      width="max-w-md"
      footer={<Button variant="primary" onClick={onClose}>Done</Button>}
    >
      <div className="space-y-2.5">
        <TwoFactorMethodRow
          icon={<ShieldCheck size={16} />}
          title="Authenticator app"
          status={user.twoFactorEnabled ? 'On' : 'Off - a code from an app like 1Password or Google Authenticator'}
          on={user.twoFactorEnabled}
          action={{ label: user.twoFactorEnabled ? 'Turn off' : 'Turn on', onClick: () => { onClose(); open2FA() } }}
        />
        <TwoFactorMethodRow
          icon={<Mail size={16} />}
          title="Email code"
          status={!user.mailConfigured ? 'Not available' : user.emailOtpEnabled ? 'On' : 'Off - a code sent to your address'}
          on={user.mailConfigured && user.emailOtpEnabled}
          action={user.mailConfigured ? { label: user.emailOtpEnabled ? 'Turn off' : 'Turn on', onClick: () => { onClose(); openEmail() } } : null}
          note={!user.mailConfigured && (
            <>This server has no outgoing mail set up yet. <Link to="/settings" className="text-accent hover:underline" onClick={onClose}>Set it up in Settings</Link>.</>
          )}
        />
        <TwoFactorMethodRow
          icon={<Fingerprint size={16} />}
          title="Passkey"
          status={!supported ? 'Not supported in this browser' : bareIp ? 'Not available' : passkeyCount > 0 ? `${passkeyCount} added - no code to type` : 'None added - no code to type'}
          on={passkeyCount > 0}
          action={supported && !bareIp ? { label: passkeyCount > 0 ? 'Manage' : 'Add', onClick: () => { onClose(); openPasskeys() } } : null}
          note={supported && bareIp && 'Needs a real domain name - this server is currently reached at a bare IP address.'}
        />
      </div>
    </Modal>
  )
}

function NewOrgModal({ onClose }: { onClose: () => void }) {
  const createOrg = useServer((s) => s.createOrg)
  const error = useServer((s) => s.error)
  const [name, setName] = useState('')
  const [busy, setBusy] = useState(false)
  const submit = async () => {
    setBusy(true)
    const ok = await createOrg(name)
    setBusy(false)
    if (ok) onClose()
  }
  return (
    <Modal
      open
      onClose={onClose}
      title="New organisation"
      description="A separate, private topology with its own clusters, history and members. You will be its owner."
      width="max-w-md"
      footer={<><Button onClick={onClose}>Cancel</Button><Button variant="primary" onClick={submit} disabled={busy || name.trim().length < 2}>Create</Button></>}
    >
      <form className="space-y-4" onSubmit={(e) => { e.preventDefault(); if (name.trim().length >= 2) void submit() }}>
        <Field label="Name"><Input value={name} onChange={(e) => setName(e.target.value)} autoFocus data-testid="new-org-name" /></Field>
        {error && <ErrorBanner>{error}</ErrorBanner>}
      </form>
    </Modal>
  )
}

function JoinModal({ onClose }: { onClose: () => void }) {
  const join = useServer((s) => s.joinWithInvite)
  const [code, setCode] = useState('')
  const [msg, setMsg] = useState('')
  const [busy, setBusy] = useState(false)
  const submit = async () => {
    setBusy(true)
    const m = await join(code)
    setBusy(false)
    if (m) setMsg(m)
    else onClose()
  }
  return (
    <Modal
      open
      onClose={onClose}
      title="Join with an invitation"
      description="Paste the code an administrator of the organisation gave you."
      width="max-w-md"
      footer={<><Button onClick={onClose}>Cancel</Button><Button variant="primary" onClick={submit} disabled={busy || !code.trim()}>Join</Button></>}
    >
      <form className="space-y-4" onSubmit={(e) => { e.preventDefault(); if (code.trim()) void submit() }}>
        <Field label="Invitation code"><Input value={code} onChange={(e) => setCode(e.target.value)} placeholder="cni_…" spellCheck={false} autoFocus data-testid="join-code" /></Field>
        {msg && <ErrorBanner>{msg}</ErrorBanner>}
      </form>
    </Modal>
  )
}

/** Bottom of the sidebar: who is signed in and where, whether their work is saved, and the account actions. */
// twoFactorNudgeKey namespaces the "don't ask again" flag per account, in this browser only: dismissing it
// signed in as one person on one machine should not silence it for anyone else, and a shared machine with
// several accounts should not have one account's dismissal hide the nudge for the next person who signs in.
const twoFactorNudgeKey = (userId: string) => `continuum:2fa-nudge-dismissed:${userId}`

export default function AccountMenu() {
  const user = useServer((s) => s.user)
  const status = useServer((s) => s.status)
  const orgs = useServer((s) => s.orgs)
  const orgId = useServer((s) => s.orgId)
  const role = useServer((s) => s.role)
  const registration = useServer((s) => s.registration)
  const signOut = useServer((s) => s.signOut)
  const selectOrg = useServer((s) => s.selectOrg)
  const sync = useWorkspace((s) => s.status)
  const [pw, setPw] = useState(false)
  const [hub, setHub] = useState(false)
  const [twoFA, setTwoFA] = useState(false)
  const [emailOTP, setEmailOTP] = useState(false)
  const [passkeys, setPasskeys] = useState(false)
  const [newOrg, setNewOrg] = useState(false)
  const [join, setJoin] = useState(false)
  const [open, setOpen] = useState(false)
  const [theme, setTheme] = useState<ThemePreference>(getThemePreference)
  // Nudges to set up a second sign-in step: shown from first login onward (there is nothing to have
  // dismissed yet the first time) until either a method is turned on or this account dismisses it on this
  // browser - a light, ongoing reminder rather than a one-shot that is easy to miss and never see again.
  // Read directly during render (a plain, synchronous localStorage lookup, not a subscription to anything
  // external) rather than mirrored into state through an effect; `dismissedNow` layers today's own dismiss
  // click on top without waiting for a re-render to see it written back.
  const userId = user?.id
  const storedDismissed = useMemo(() => {
    if (!userId) return false
    try { return localStorage.getItem(twoFactorNudgeKey(userId)) === '1' } catch { return false }
  }, [userId])
  const [dismissedNow, setDismissedNow] = useState(false)
  const nudgeDismissed = storedDismissed || dismissedNow
  const { pathname } = useLocation()
  // close the menu when the page changes (adjusting state while rendering, not in an effect)
  const [seenPath, setSeenPath] = useState(pathname)
  if (seenPath !== pathname) { setSeenPath(pathname); setOpen(false) }
  const button = useRef<HTMLButtonElement>(null)
  const menu = useRef<HTMLDivElement>(null)
  useEffect(() => {
    if (!open) return
    const items = () => [...(menu.current?.querySelectorAll<HTMLElement>('[role=menuitem]') ?? [])]
    // A menu takes focus when it opens, moves with the arrow keys, and hands focus back to its button when it closes.
    items()[0]?.focus()
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        setOpen(false)
        button.current?.focus()
      } else if (e.key === 'ArrowDown' || e.key === 'ArrowUp' || e.key === 'Home' || e.key === 'End') {
        const list = items()
        if (!list.length) return
        e.preventDefault()
        const at = list.indexOf(document.activeElement as HTMLElement)
        const next = e.key === 'Home' ? 0 : e.key === 'End' ? list.length - 1 : (at + (e.key === 'ArrowDown' ? 1 : -1) + list.length) % list.length
        list[next].focus()
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [open])
  if (status !== 'connected' || !user) return null
  const label = SYNC_LABEL[sync]
  const item = 'flex w-full items-center gap-2.5 rounded-md px-2.5 py-2 text-left text-sm text-nb-300 hover:bg-nb-940 hover:text-white'
  const twoFactorMethodsOn = [user.twoFactorEnabled, user.emailOtpEnabled, user.passkeys.length > 0].filter(Boolean).length
  const showTwoFactorNudge = twoFactorMethodsOn === 0 && !nudgeDismissed
  const dismissTwoFactorNudge = () => {
    setDismissedNow(true)
    try { localStorage.setItem(twoFactorNudgeKey(user.id), '1') } catch { /* a private window or blocked storage just means it asks again next time */ }
  }
  const pick = (fn: () => void) => () => {
    setOpen(false)
    fn()
  }

  return (
    <div className="relative mb-2" data-testid="account">
      {showTwoFactorNudge && (
        <div className="mb-2 flex items-start gap-2 rounded-lg border border-amber-400/30 bg-amber-400/10 p-2.5 text-xs text-amber-100" data-testid="two-factor-nudge">
          <ShieldCheck size={14} className="mt-0.5 shrink-0 text-amber-300" aria-hidden />
          <p className="flex-1">
            Add a second sign-in step so a leaked password alone can't get in.{' '}
            <button type="button" className="font-medium underline hover:text-white" onClick={() => setHub(true)} data-testid="two-factor-nudge-setup">Set up now</button>
          </p>
          <button type="button" aria-label="Dismiss" className="shrink-0 text-amber-200/70 hover:text-white" onClick={dismissTwoFactorNudge} data-testid="two-factor-nudge-dismiss">
            <X size={14} />
          </button>
        </div>
      )}
      {open && (
        <>
          <div className="fixed inset-0 z-10" onClick={() => setOpen(false)} />
          <div ref={menu} role="menu" aria-label="Account" className="absolute inset-x-0 bottom-full z-20 mb-2 rounded-lg border border-nb-850 bg-nb-920 p-1 shadow-xl" data-testid="account-menu">
            <div className="px-2.5 pb-1 pt-1 text-[11px] font-medium uppercase tracking-wide text-nb-600" aria-hidden>Organisation</div>
            {orgId && (
              <NavLink to="/team" className={item} role="menuitem" data-testid="nav-team">
                <Users size={15} className="text-nb-500" /> Members &amp; access
              </NavLink>
            )}
            {orgId && atLeast(role, 'admin') && (
              <NavLink to="/activity" className={item} role="menuitem" data-testid="nav-activity">
                <ScrollText size={15} className="text-nb-500" /> Who did what
              </NavLink>
            )}
            {registration === 'open' && (
              <button onClick={pick(() => setNewOrg(true))} className={item} role="menuitem"><Plus size={15} className="text-nb-500" /> New organisation</button>
            )}
            <button onClick={pick(() => setJoin(true))} className={item} role="menuitem" data-testid="join-open"><Ticket size={15} className="text-nb-500" /> Join with a code</button>
            <div className="my-1 border-t border-nb-850" />
            <div className="px-2.5 pb-1 pt-1 text-[11px] font-medium uppercase tracking-wide text-nb-600" aria-hidden>Account &amp; security</div>
            <button onClick={pick(() => setPw(true))} className={item} role="menuitem"><KeyRound size={15} className="text-nb-500" /> Change password</button>
            <button onClick={pick(() => setHub(true))} className={item} role="menuitem" data-testid="two-factor-open">
              <ShieldCheck size={15} className="text-nb-500" />
              Two-factor authentication
              {twoFactorMethodsOn > 0 && <span className="ml-auto text-xs text-nb-500">{twoFactorMethodsOn} on</span>}
            </button>
            <div className="my-1 border-t border-nb-850" />
            <div className="px-2.5 pb-1 pt-1 text-[11px] font-medium uppercase tracking-wide text-nb-600" aria-hidden>Appearance</div>
            <div className="flex gap-1 px-2.5 pb-1.5" role="radiogroup" aria-label="Theme">
              {(
                [
                  { value: 'system' as const, label: 'System', icon: Monitor },
                  { value: 'light' as const, label: 'Light', icon: Sun },
                  { value: 'dark' as const, label: 'Dark', icon: Moon },
                ]
              ).map(({ value, label, icon: Icon }) => (
                <button
                  key={value}
                  type="button"
                  role="radio"
                  aria-checked={theme === value}
                  onClick={() => { setThemePreference(value); setTheme(value) }}
                  className={`flex flex-1 flex-col items-center gap-1 rounded-md border py-1.5 text-xs ${theme === value ? 'border-accent/60 bg-accent-soft text-white' : 'border-nb-850 text-nb-500 hover:bg-nb-940 hover:text-nb-300'}`}
                  data-testid={`theme-${value}`}
                >
                  <Icon size={14} />
                  {label}
                </button>
              ))}
            </div>
            <div className="my-1 border-t border-nb-850" />
            <button onClick={pick(() => void signOut())} className={item} role="menuitem" data-testid="sign-out"><LogOut size={15} className="text-nb-500" /> Sign out</button>
          </div>
        </>
      )}
      <div className="rounded-lg border border-nb-850 bg-nb-925 p-2">
        <button
          ref={button}
          onClick={() => setOpen((o) => !o)}
          aria-haspopup="menu"
          aria-expanded={open}
          aria-label="Account menu"
          className="flex w-full items-center gap-2.5 rounded-md p-1 text-left hover:bg-nb-930"
          data-testid="account-button"
        >
          <span className="grid size-8 shrink-0 place-items-center rounded-full bg-accent-soft text-sm font-semibold uppercase text-accent" aria-hidden>{user.username.slice(0, 1)}</span>
          <span className="min-w-0 flex-1">
            <span className="block truncate text-sm font-medium text-white" data-testid="account-name">{user.username}</span>
            <span className="block text-[11px] text-nb-500" data-testid="account-role">{role ? ROLE_LABEL[role] : 'No organisation'}</span>
          </span>
          <ChevronsUpDown size={14} className="shrink-0 text-nb-500" aria-hidden />
        </button>
        {orgs.length > 0 && (
          <div className="mt-2">
            <Select value={orgId ?? ''} onChange={(e) => void selectOrg(e.target.value)} aria-label="Organisation" className="h-8 text-xs" data-testid="org-switch">
              {orgs.map((o) => (
                <option key={o.id} value={o.id}>{o.name}</option>
              ))}
            </Select>
          </div>
        )}
        {label.text && <div className={`mt-2 px-1 text-[11px] ${label.tone}`} role="status" data-testid="sync-status">{label.text}</div>}
      </div>
      {pw && <ChangePasswordModal onClose={() => setPw(false)} />}
      {hub && (
        <TwoFactorHubModal
          onClose={() => setHub(false)}
          open2FA={() => setTwoFA(true)}
          openEmail={() => setEmailOTP(true)}
          openPasskeys={() => setPasskeys(true)}
        />
      )}
      {twoFA && (user.twoFactorEnabled ? <TwoFactorDisableModal onClose={() => setTwoFA(false)} /> : <TwoFactorSetupModal onClose={() => setTwoFA(false)} />)}
      {emailOTP && (user.emailOtpEnabled ? <EmailDisableModal onClose={() => setEmailOTP(false)} /> : <EmailSetupModal onClose={() => setEmailOTP(false)} />)}
      {passkeys && <PasskeysModal onClose={() => setPasskeys(false)} />}
      {newOrg && <NewOrgModal onClose={() => setNewOrg(false)} />}
      {join && <JoinModal onClose={() => setJoin(false)} />}
    </div>
  )
}
