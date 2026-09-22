import { ChevronsUpDown, KeyRound, LogOut, Plus, ScrollText, ShieldCheck, Ticket, Users } from 'lucide-react'
import { useEffect, useRef, useState } from 'react'
import { NavLink, useLocation } from 'react-router-dom'
import { Button, CopyButton, ErrorBanner, Field, Input, Modal, Select } from '@/components/ui/primitives'
import { atLeast, ROLE_LABEL } from '@/lib/api'
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

function ChangePasswordModal({ onClose }: { onClose: () => void }) {
  const { changePassword, error } = useServer()
  const [current, setCurrent] = useState('')
  const [next, setNext] = useState('')
  const [again, setAgain] = useState('')
  const [busy, setBusy] = useState(false)
  const [done, setDone] = useState(false)
  const bad = next.length < 12 || next !== again || !current

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
          <Field label="Current password"><Input type="password" value={current} onChange={(e) => setCurrent(e.target.value)} autoComplete="current-password" autoFocus /></Field>
          <Field label="New password" hint="At least 12 characters."><Input type="password" value={next} onChange={(e) => setNext(e.target.value)} autoComplete="new-password" /></Field>
          <Field label="New password again"><Input type="password" value={again} onChange={(e) => setAgain(e.target.value)} autoComplete="new-password" /></Field>
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
          <p className="text-sm text-nb-400">Add this key to an authenticator app (Google Authenticator, 1Password, Authy, …), then enter the 6-digit code it shows.</p>
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
        <Field label="Current password"><Input type="password" value={password} onChange={(e) => setPassword(e.target.value)} autoComplete="current-password" autoFocus /></Field>
        {error && <ErrorBanner>{error}</ErrorBanner>}
      </form>
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
  const [twoFA, setTwoFA] = useState(false)
  const [newOrg, setNewOrg] = useState(false)
  const [join, setJoin] = useState(false)
  const [open, setOpen] = useState(false)
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
  const pick = (fn: () => void) => () => {
    setOpen(false)
    fn()
  }

  return (
    <div className="relative mb-2" data-testid="account">
      {open && (
        <>
          <div className="fixed inset-0 z-10" onClick={() => setOpen(false)} />
          <div ref={menu} role="menu" aria-label="Account" className="absolute inset-x-0 bottom-full z-20 mb-2 rounded-lg border border-nb-850 bg-nb-920 p-1 shadow-xl" data-testid="account-menu">
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
            <button onClick={pick(() => setPw(true))} className={item} role="menuitem"><KeyRound size={15} className="text-nb-500" /> Change password</button>
            <button onClick={pick(() => setTwoFA(true))} className={item} role="menuitem" data-testid="two-factor-open">
              <ShieldCheck size={15} className="text-nb-500" /> {user.twoFactorEnabled ? 'Two-factor authentication (on)' : 'Turn on two-factor authentication'}
            </button>
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
      {twoFA && (user.twoFactorEnabled ? <TwoFactorDisableModal onClose={() => setTwoFA(false)} /> : <TwoFactorSetupModal onClose={() => setTwoFA(false)} />)}
      {newOrg && <NewOrgModal onClose={() => setNewOrg(false)} />}
      {join && <JoinModal onClose={() => setJoin(false)} />}
    </div>
  )
}
