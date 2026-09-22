import { Cpu } from 'lucide-react'
import { useState, type ReactNode } from 'react'
import { Copyright } from '@/components/ui/brand'
import { Button, ErrorBanner, Field, Input } from '@/components/ui/primitives'
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
          <Input type="password" value={password} onChange={(e) => setPassword(e.target.value)} autoComplete="current-password" />
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

/** Shown after a correct password when the account also has two-factor authentication on. */
function TwoFactorScreen() {
  const { error, verifyTwoFactor, cancelTwoFactor } = useServer()
  const [code, setCode] = useState('')
  const [busy, setBusy] = useState(false)

  const submit = async () => {
    setBusy(true)
    const ok = await verifyTwoFactor(code)
    setBusy(false)
    if (!ok) setCode('')
  }

  return (
    <Shell title="Enter your code" description="Open your authenticator app, or use one of your recovery codes if you no longer have it.">
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
            autoFocus
            autoComplete="one-time-code"
            inputMode="numeric"
            placeholder="123456"
            data-testid="totp-code"
          />
        </Field>
        <ErrorLine text={error} />
        <Button type="submit" variant="primary" className="w-full" disabled={busy || !code.trim()}>
          {busy ? 'Checking…' : 'Continue'}
        </Button>
      </form>
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
  const short = password !== '' && password.length < 12
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
        <Field label="Password" hint="At least 12 characters. A few random words work well.">
          <Input type="password" value={password} onChange={(e) => setPassword(e.target.value)} autoComplete="new-password" data-testid="reg-password" />
        </Field>
        {!joining && (
          <Field label="Name of your organisation" hint="Optional. You can rename it later.">
            <Input value={org} onChange={(e) => setOrg(e.target.value)} placeholder={username ? `${username}'s organisation` : ''} data-testid="reg-org" />
          </Field>
        )}
        {short && <p className="text-xs text-amber-300">Too short: use at least 12 characters.</p>}
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
          <Input type="password" value={current} onChange={(e) => setCurrent(e.target.value)} autoComplete="current-password" autoFocus />
        </Field>
        <Field label="New password" hint="At least 12 characters. A few random words work well.">
          <Input type="password" value={next} onChange={(e) => setNext(e.target.value)} autoComplete="new-password" />
        </Field>
        <Field label="New password again">
          <Input type="password" value={again} onChange={(e) => setAgain(e.target.value)} autoComplete="new-password" />
        </Field>
        {short && <p className="text-xs text-amber-300">Too short: use at least 12 characters.</p>}
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
