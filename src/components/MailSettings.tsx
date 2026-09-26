import clsx from 'clsx'
import { CircleAlert, Save } from 'lucide-react'
import { useEffect, useState } from 'react'
import { Button, Field, Input, SavedNote } from '@/components/ui/primitives'
import { api, type Conn, type MailConfig } from '@/lib/api'

/**
 * Settings → Email: the server's outgoing-mail (SMTP) configuration, used only for a sign-in code and
 * address verification. This is server-wide, not one organisation's, so it is fetched and saved through
 * its own endpoint (api.mailConfig/saveMailConfig) rather than through AppSettings, and this section only
 * renders at all when the signed-in user is an owner of the default organisation (User.canManageMail) -
 * being an admin or owner of some other organisation, which anyone can create for themselves, has nothing
 * to do with it (see requireDefaultOwner on the server).
 */
export default function MailSettings({ conn }: { conn: Conn }) {
  const [cfg, setCfg] = useState<MailConfig | null>(null)
  const [host, setHost] = useState('')
  const [port, setPort] = useState('')
  const [username, setUsername] = useState('')
  const [from, setFrom] = useState('')
  const [password, setPassword] = useState('')
  const [editingPassword, setEditingPassword] = useState(false)
  const [clearPassword, setClearPassword] = useState(false)
  const [saved, setSaved] = useState(false)
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(true)

  useEffect(() => {
    let live = true
    api
      .mailConfig(conn)
      .then((c) => {
        if (!live) return
        setCfg(c)
        setHost(c.host)
        setPort(c.port)
        setUsername(c.username)
        setFrom(c.from)
        setPassword('')
        setEditingPassword(false)
        setClearPassword(false)
        setError('')
      })
      .catch((e) => live && setError(e instanceof Error ? e.message : 'Could not load the mail configuration.'))
      .finally(() => live && setLoading(false))
    return () => {
      live = false
    }
  }, [conn])

  if (loading || !cfg) {
    return (
      <section id="email" className="mb-8 scroll-mt-6">
        <h2 className="mb-1 text-sm font-medium text-nb-300">Email (SMTP)</h2>
        <p className="text-sm text-nb-500">{error || 'Loading…'}</p>
      </section>
    )
  }

  const badFrom = from !== '' && !/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(from)
  const badPort = port !== '' && (!/^\d+$/.test(port) || Number(port) < 1 || Number(port) > 65535)
  const dirty = host !== cfg.host || port !== cfg.port || username !== cfg.username || from !== cfg.from || password !== '' || clearPassword

  const save = async () => {
    try {
      const n = await api.saveMailConfig(conn, {
        host: host.trim(),
        port: port.trim(),
        username: username.trim(),
        from: from.trim(),
        ...(password ? { password } : {}),
        ...(clearPassword ? { clearPassword: true } : {}),
      })
      setCfg(n)
      setPassword('')
      setEditingPassword(false)
      setClearPassword(false)
      setSaved(true)
      setError('')
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Could not save the mail configuration.')
    }
  }

  return (
    <section id="email" className="mb-8 scroll-mt-6">
      <h2 className="mb-1 text-sm font-medium text-nb-300">Email (SMTP)</h2>
      <p className="mb-3 max-w-2xl text-sm text-nb-500">
        Used for a sign-in code and address verification - nothing else is ever sent. Only owners of the default organisation can see or change this; leave the host empty to switch email off entirely.
      </p>
      <div className="rounded-xl border border-nb-850 bg-nb-925 p-5">
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label="Host" hint="Leave empty to switch email off.">
            <Input value={host} onChange={(e) => { setSaved(false); setHost(e.target.value) }} placeholder="smtp.example.com" data-testid="mail-host" />
          </Field>
          <Field label="Port" hint={badPort ? 'A number between 1 and 65535.' : 'Usually 587 (STARTTLS) or 465.'}>
            <Input value={port} onChange={(e) => { setSaved(false); setPort(e.target.value) }} placeholder="587" className={clsx('w-28', badPort && 'border-bad/60')} data-testid="mail-port" />
          </Field>
          <Field label="Username" hint="Leave empty for a relay that needs no authentication.">
            <Input value={username} onChange={(e) => { setSaved(false); setUsername(e.target.value) }} data-testid="mail-username" />
          </Field>
          <Field label="From address" hint={badFrom ? 'Looks incomplete.' : 'What recipients see the code arrive from.'}>
            <Input value={from} onChange={(e) => { setSaved(false); setFrom(e.target.value) }} placeholder="continuum@example.com" className={clsx(badFrom && 'border-bad/60')} data-testid="mail-from" />
          </Field>
        </div>

        <div className="mt-4">
          <span className="mb-1.5 block text-sm font-medium text-nb-300">Password</span>
          {editingPassword ? (
            <div className="flex flex-wrap items-center gap-2">
              <Input
                type="password"
                value={password}
                onChange={(e) => { setSaved(false); setPassword(e.target.value) }}
                placeholder="New password"
                className="max-w-xs"
                autoComplete="new-password"
                data-testid="mail-password"
              />
              <Button size="sm" type="button" onClick={() => { setPassword(''); setEditingPassword(false) }}>Cancel</Button>
            </div>
          ) : clearPassword ? (
            <p className="flex flex-wrap items-center gap-2 text-sm text-warn/90">
              Removing the password on save.
              <button type="button" className="text-xs text-accent hover:underline" onClick={() => setClearPassword(false)}>Undo</button>
            </p>
          ) : (
            <p className="flex flex-wrap items-center gap-3 text-sm text-nb-400">
              {cfg.passwordSet ? 'A password is set.' : 'No password is set.'}
              <button type="button" className="text-xs text-accent hover:underline" onClick={() => { setSaved(false); setEditingPassword(true) }} data-testid="mail-password-edit">
                {cfg.passwordSet ? 'Replace' : 'Set one'}
              </button>
              {cfg.passwordSet && (
                <button type="button" className="text-xs text-accent hover:underline" onClick={() => { setSaved(false); setClearPassword(true) }} data-testid="mail-password-remove">
                  Remove
                </button>
              )}
            </p>
          )}
        </div>

        {error && (
          <p className="mt-3 text-sm text-bad" role="alert">
            <CircleAlert size={13} className="mr-1 inline" aria-hidden />
            {error}
          </p>
        )}
        <div className="mt-4 flex items-center gap-3">
          <Button variant="primary" disabled={!dirty || badFrom || badPort} onClick={save} data-testid="save-mail">
            <Save size={15} /> Save
          </Button>
          {saved && <SavedNote>Saved.</SavedNote>}
        </div>
      </div>
    </section>
  )
}
