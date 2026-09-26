import clsx from 'clsx'
import { Check, Cpu, KeyRound, Minus, X } from 'lucide-react'
import { useState, type ReactNode } from 'react'
import { Copyright } from '@/components/ui/brand'
import { Button, ErrorBanner, Field, Input, PasswordInput } from '@/components/ui/primitives'
import { ROLE_LABEL } from '@/lib/api'
import { useServer } from '@/store/server'

function Shell({ title, description, children }: { title: string; description: string; children: ReactNode }) {
  return (
    <div className="grid min-h-full place-items-center bg-nb-900 px-4 py-10">
      <div className="w-full max-w-sm">
        <div className="mb-6 flex items-center gap-2.5">
          <div className="grid size-9 place-items-center rounded-lg bg-accent-soft text-accent">
            <Cpu size={20} />
          </div>
          <div className="leading-tight">
            <div className="text-sm font-semibold text-white">Continuum</div>
            <div className="text-[11px] text-nb-500">Topology Studio</div>
          </div>
        </div>
        <div className="rounded-xl border border-nb-850 bg-nb-920 p-6 shadow-2xl">
          <h1 className="text-lg font-medium text-white">{title}</h1>
          <p className="mt-1 text-sm text-nb-500">{description}</p>
          <div className="mt-5">{children}</div>
        </div>
        <Copyright className="mt-6 text-center" />
      </div>
    </div>
  )
}

function ErrorLine({ text }: { text?: string }) {
  return text ? <ErrorBanner>{text}</ErrorBanner> : null
}

/** The rules the server enforces (see backend/internal/server/password.go's CheckPasswordPolicy):
 *  length first, then a digit and a symbol on top - not NIST's composition-free recommendation, but a
 *  deliberate exception to it (see that function's own doc comment for why). */
export function passwordRules(password: string, username: string) {
  const repeatedChar = password.length > 0 && [...password].every((c) => c === password[0])
  const hasDigit = /\d/u.test(password)
  const hasSymbol = [...password].some((c) => !/[\p{L}\d]/u.test(c))
  return [
    { key: 'length', label: '12–128 characters', ok: password.length >= 12 && password.length <= 128 },
    { key: 'username', label: 'Not the same as your username', ok: password.length > 0 && password.toLowerCase() !== username.trim().toLowerCase() },
    { key: 'repeat', label: 'Not a single character repeated', ok: password.length > 0 && !repeatedChar },
    { key: 'digit', label: 'At least one number', ok: hasDigit },
    { key: 'symbol', label: 'At least one symbol (e.g. ! @ # $ %)', ok: hasSymbol },
  ]
}

/**
 * Live checklist of the password policy, one line per rule: gray and unmarked before the person has
 * typed anything, then green or red as it starts matching or missing each one. Shared between sign-up
 * and the forced change-password screen, the only two places someone picks a new password.
 */
export function PasswordRequirements({ password, username }: { password: string; username: string }) {
  const empty = password === ''
  return (
    <ul className="space-y-1">
      {passwordRules(password, username).map((r) => (
        <li key={r.key} className={clsx('flex items-center gap-1.5 text-xs', empty ? 'text-nb-500' : r.ok ? 'text-emerald-400' : 'text-red-400')}>
          {empty ? <Minus size={12} aria-hidden /> : r.ok ? <Check size={12} className="fade-in" aria-hidden /> : <X size={12} className="fade-in" aria-hidden />}
          {r.label}
          {!empty && <span className="sr-only">{r.ok ? ' satisfied' : ' not satisfied'}</span>}
        </li>
      ))}
    </ul>
  )
}

/** The invitation someone arrived with, shown above the sign-in and sign-up forms. */
function InviteNote() {
  const invite = useServer((s) => s.invite)
  if (!invite?.preview) return null
  return (
    <p className="mb-4 rounded-md border border-accent/30 bg-accent-soft px-3 py-2 text-sm text-white" data-testid="invite-note">
      You have been invited to <strong>{invite.preview.organisation}</strong> as {ROLE_LABEL[invite.preview.role].toLowerCase()}. Sign in, or create an account, to join.
    </p>
  )
}

function SignInScreen({ onRegister }: { onRegister: () => void }) {
  const { url, error, signIn, disconnect, registration, invite } = useServer()
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [busy, setBusy] = useState(false)
  const canRegister = registration === 'open' || (registration === 'invite' && !!invite?.preview)

  const submit = async () => {
    setBusy(true)
    const ok = await signIn(username, password)
    setBusy(false)
    if (!ok) setPassword('')
  }

  return (
    <Shell title="Sign in" description={`Server: ${url || window.location.origin}`}>
      <InviteNote />
      <form
        className="space-y-4"
        onSubmit={(e) => {
          e.preventDefault()
          void submit()
        }}
      >
        <Field label="Username">
          <Input value={username} onChange={(e) => setUsername(e.target.value)} autoComplete="username" autoFocus spellCheck={false} autoCapitalize="none" />
        </Field>
        <Field label="Password">
          <PasswordInput value={password} onChange={(e) => setPassword(e.target.value)} autoComplete="current-password" />
        </Field>
        <ErrorLine text={error} />
        <Button type="submit" variant="primary" className="w-full" disabled={busy || !username.trim() || !password}>
          {busy ? 'Signing in…' : 'Sign in'}
        </Button>
      </form>
      {canRegister && (
        <button onClick={onRegister} className="mt-4 w-full rounded-md border border-nb-800 py-2 text-center text-sm text-nb-300 hover:bg-nb-940" data-testid="to-register">
          Create an account
        </button>
      )}
      {registration === 'invite' && !canRegister && <p className="mt-4 text-center text-xs text-nb-500">New accounts on this server are by invitation. Open the invitation link you were given.</p>}
      <button onClick={disconnect} className="mt-4 w-full text-center text-xs text-nb-500 hover:text-nb-300">
        Use this browser without a server
      </button>
    </Shell>
  )
}

/** Shown after a correct password when the account also has two-factor authentication on. Which methods apply
 *  (a passkey, an authenticator app / recovery code, an emailed code, any combination) comes from
 *  `pendingMethods`, set by `signIn` from the 401's error body - `login2FA` accepts a code from an
 *  authenticator app or by email back in the same field, while a passkey runs its own browser ceremony via
 *  `signInWithPasskey` and opens the session directly. */
function TwoFactorScreen() {
  const { error, verifyTwoFactor, cancelTwoFactor, requestLoginEmailCode, signInWithPasskey, pendingMethods } = useServer()
  const [code, setCode] = useState('')
  const [busy, setBusy] = useState(false)
  const [emailBusy, setEmailBusy] = useState(false)
  const [emailSent, setEmailSent] = useState(false)
  const [passkeyBusy, setPasskeyBusy] = useState(false)
  const hasTotp = pendingMethods.includes('totp')
  const hasEmail = pendingMethods.includes('email')
  const hasPasskey = pendingMethods.includes('webauthn')
  const hasCode = hasTotp || hasEmail

  const submit = async () => {
    setBusy(true)
    const ok = await verifyTwoFactor(code)
    setBusy(false)
    if (!ok) setCode('')
  }

  const sendEmailCode = async () => {
    setEmailBusy(true)
    const ok = await requestLoginEmailCode()
    setEmailBusy(false)
    if (ok) setEmailSent(true)
  }

  const signInWithPasskeyClick = async () => {
    setPasskeyBusy(true)
    await signInWithPasskey()
    setPasskeyBusy(false)
  }

  const description = hasPasskey
    ? hasCode
      ? 'Use your passkey, enter a two-factor code, or have one emailed to you.'
      : 'Use your passkey to finish signing in.'
    : hasTotp && hasEmail
      ? 'Open your authenticator app, use a recovery code, or have a code emailed to you.'
      : hasEmail
        ? 'We can email you a code to finish signing in.'
        : 'Open your authenticator app, or use one of your recovery codes if you no longer have it.'

  return (
    <Shell title="Enter your code" description={description}>
      {hasPasskey && (
        <Button
          variant={hasCode ? 'secondary' : 'primary'}
          className="w-full"
          onClick={() => void signInWithPasskeyClick()}
          disabled={passkeyBusy}
          data-testid="webauthn-2fa"
        >
          <KeyRound size={15} aria-hidden />
          {passkeyBusy ? 'Waiting for your passkey…' : 'Use your passkey'}
        </Button>
      )}
      {hasCode && (
        <>
          {hasPasskey && (
            <div className="my-4 flex items-center gap-2 text-[11px] text-nb-600" aria-hidden="true">
              <div className="h-px flex-1 bg-nb-850" />
              or
              <div className="h-px flex-1 bg-nb-850" />
            </div>
          )}
          <form
            className="space-y-4"
            onSubmit={(e) => {
              e.preventDefault()
              void submit()
            }}
          >
            <Field label="Code">
              <Input
                value={code}
                onChange={(e) => setCode(e.target.value)}
                autoFocus={!hasPasskey}
                autoComplete="one-time-code"
                inputMode="numeric"
                placeholder="123456"
                data-testid="totp-code"
              />
            </Field>
            <Button type="submit" variant="primary" className="w-full" disabled={busy || !code.trim()}>
              {busy ? 'Checking…' : 'Continue'}
            </Button>
          </form>
          {hasEmail && (
            <button
              onClick={() => void sendEmailCode()}
              disabled={emailBusy}
              className="mt-3 w-full text-center text-xs text-accent hover:text-accent/80 disabled:opacity-50"
              data-testid="email-2fa-code"
            >
              {emailBusy ? 'Sending…' : emailSent ? 'Code sent — send another' : 'Email me a code'}
            </button>
          )}
        </>
      )}
      {error && (
        <div className="mt-4">
          <ErrorLine text={error} />
        </div>
      )}
      <button onClick={cancelTwoFactor} className="mt-4 w-full text-center text-xs text-nb-500 hover:text-nb-300">
        Back to sign in
      </button>
    </Shell>
  )
}

function RegisterScreen({ onBack }: { onBack: () => void }) {
  const { url, error, register, invite } = useServer()
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [org, setOrg] = useState('')
  const [busy, setBusy] = useState(false)
  const joining = !!invite?.preview

  const submit = async () => {
    setBusy(true)
    const ok = await register(username, password, org, joining ? invite?.token : undefined)
    setBusy(false)
    if (!ok) setPassword('')
  }

  return (
    <Shell
      title="Create an account"
      description={joining ? `You will join ${invite?.preview?.organisation}.` : 'You get an organisation of your own: a private topology that nobody else can see unless you invite them.'}
    >
      <InviteNote />
      <form
        className="space-y-4"
        onSubmit={(e) => {
          e.preventDefault()
          void submit()
        }}
      >
        <Field label="Username" hint="3-64 characters: letters, digits and . _ @ -">
          <Input value={username} onChange={(e) => setUsername(e.target.value)} autoComplete="username" autoFocus spellCheck={false} autoCapitalize="none" data-testid="reg-username" />
        </Field>
        <Field label="Password" hint="A few random words work well.">
          <PasswordInput value={password} onChange={(e) => setPassword(e.target.value)} autoComplete="new-password" data-testid="reg-password" />
        </Field>
        <PasswordRequirements password={password} username={username} />
        {!joining && (
          <Field label="Name of your organisation" hint="Optional. You can rename it later.">
            <Input value={org} onChange={(e) => setOrg(e.target.value)} placeholder={username ? `${username}'s organisation` : ''} data-testid="reg-org" />
          </Field>
        )}
        <ErrorLine text={error} />
        <Button type="submit" variant="primary" className="w-full" disabled={busy || username.trim().length < 3 || password.length < 12} data-testid="reg-submit">
          {busy ? 'Creating…' : 'Create account'}
        </Button>
      </form>
      <button onClick={onBack} className="mt-4 w-full text-center text-xs text-nb-500 hover:text-nb-300">
        I already have an account
      </button>
      <p className="mt-2 text-center text-[11px] text-nb-600">{url || window.location.origin}</p>
    </Shell>
  )
}

/** Signed in but in no organisation yet: make one, or join one with an invitation code. */
function NoOrganisationScreen() {
  const { user, registration, createOrg, joinWithInvite, signOut, error } = useServer()
  const [name, setName] = useState('')
  const [code, setCode] = useState('')
  const [msg, setMsg] = useState('')
  const [busy, setBusy] = useState(false)
  return (
    <Shell title="You are not in an organisation yet" description={`Signed in as ${user?.username}. An organisation is a private topology with its own clusters, history and members.`}>
      <div className="space-y-6">
        <form
          className="space-y-3"
          onSubmit={(e) => {
            e.preventDefault()
            if (code.trim()) {
              setBusy(true)
              void joinWithInvite(code).then((m) => {
                setMsg(m)
                setBusy(false)
              })
            }
          }}
        >
          <Field label="I have an invitation code" hint="Paste the code an administrator gave you.">
            <Input value={code} onChange={(e) => setCode(e.target.value)} spellCheck={false} placeholder="cni_…" data-testid="join-code" />
          </Field>
          {msg && <ErrorLine text={msg} />}
          <Button type="submit" variant="primary" className="w-full" disabled={busy || !code.trim()}>Join</Button>
        </form>
        {registration === 'open' && (
          <form
            className="space-y-3 border-t border-nb-850 pt-5"
            onSubmit={(e) => {
              e.preventDefault()
              if (name.trim().length >= 2) void createOrg(name)
            }}
          >
            <Field label="Or start a new one"><Input value={name} onChange={(e) => setName(e.target.value)} placeholder="Name of the organisation" data-testid="new-org-name" /></Field>
            <ErrorLine text={error} />
            <Button type="submit" className="w-full" disabled={name.trim().length < 2}>Create organisation</Button>
          </form>
        )}
      </div>
      <button onClick={() => void signOut()} className="mt-4 w-full text-center text-xs text-nb-500 hover:text-nb-300">
        Sign out
      </button>
    </Shell>
  )
}

/** Shown when the account's password was set by someone else (first sign-in, or after a reset). */
function ChangePasswordScreen() {
  const { error, changePassword, signOut, user } = useServer()
  const [current, setCurrent] = useState('')
  const [next, setNext] = useState('')
  const [again, setAgain] = useState('')
  const [busy, setBusy] = useState(false)
  const mismatch = again !== '' && next !== again
  const short = next !== '' && next.length < 12

  const submit = async () => {
    setBusy(true)
    const ok = await changePassword(current, next)
    setBusy(false)
    if (!ok) return
  }

  return (
    <Shell title="Choose a new password" description={`Signed in as ${user?.username}. The password you used was set for you, so pick one only you know before continuing.`}>
      <form
        className="space-y-4"
        onSubmit={(e) => {
          e.preventDefault()
          void submit()
        }}
      >
        <Field label="Current password">
          <PasswordInput value={current} onChange={(e) => setCurrent(e.target.value)} autoComplete="current-password" autoFocus />
        </Field>
        <Field label="New password" hint="A few random words work well.">
          <PasswordInput value={next} onChange={(e) => setNext(e.target.value)} autoComplete="new-password" />
        </Field>
        <PasswordRequirements password={next} username={user?.username ?? ''} />
        <Field label="New password again">
          <PasswordInput value={again} onChange={(e) => setAgain(e.target.value)} autoComplete="new-password" />
        </Field>
        {mismatch && <p className="text-xs text-amber-300">The two passwords differ.</p>}
        <ErrorLine text={error} />
        <Button type="submit" variant="primary" className="w-full" disabled={busy || !current || short || !next || !again || mismatch}>
          {busy ? 'Saving…' : 'Save new password'}
        </Button>
      </form>
      <button onClick={() => void signOut()} className="mt-4 w-full text-center text-xs text-nb-500 hover:text-nb-300">
        Sign out
      </button>
    </Shell>
  )
}

/**
 * Stands in front of the app when a Continuum server is in use: sign-in first, then (when required)
 * a forced password change. Without a server the app opens directly and works on the browser's own copy.
 */
export default function AuthGate({ children }: { children: ReactNode }) {
  const status = useServer((s) => s.status)
  const checked = useServer((s) => s.checked)
  const mustChange = useServer((s) => s.user?.mustChangePassword ?? false)
  const hasOrg = useServer((s) => !!s.orgId)
  const [registering, setRegistering] = useState(false)

  if (!checked) return <div className="min-h-full bg-nb-900" aria-busy="true" />
  if (status === 'signin') return registering ? <RegisterScreen onBack={() => setRegistering(false)} /> : <SignInScreen onRegister={() => setRegistering(true)} />
  if (status === 'twofactor') return <TwoFactorScreen />
  if (status === 'connected' && mustChange) return <ChangePasswordScreen />
  if (status === 'connected' && !hasOrg) return <NoOrganisationScreen />
  return <>{children}</>
}
