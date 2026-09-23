import { ChevronRight, Copy, Link2, Trash2, UserPlus } from 'lucide-react'
import { useCallback, useEffect, useState } from 'react'
import { Button, ErrorBanner, Field, Input, Modal, PageHeader, Pill, Select, Table, Td, Th } from '@/components/ui/primitives'
import { api, ApiError, grantable, ROLE_HELP, ROLE_LABEL, type Invite, type Member, type Role } from '@/lib/api'
import { useServer } from '@/store/server'

const when = (iso?: string) => (iso ? new Date(iso).toLocaleString([], { dateStyle: 'medium', timeStyle: 'short' }) : 'never')
const problem = (e: unknown, fallback: string) => (e instanceof ApiError ? e.message : fallback)

function ErrorLine({ text }: { text: string }) {
  return text ? <ErrorBanner className="mb-4">{text}</ErrorBanner> : null
}

/** The invitation, shown once: the server keeps only a hash, so this is the only chance to copy it. */
function InviteCreated({ token, invite, url, org, onClose }: { token: string; invite: Invite; url: string; org: string; onClose: () => void }) {
  const [copied, setCopied] = useState('')
  const link = `${url || window.location.origin}/?invite=${token}`
  const copy = async (what: string, text: string) => {
    try {
      await navigator.clipboard.writeText(text)
      setCopied(what)
    } catch {
      /* clipboard unavailable: the text is selectable */
    }
  }
  return (
    <Modal open onClose={onClose} title="Invitation created" width="max-w-lg" footer={<Button variant="primary" onClick={onClose}>Done</Button>}>
      <p className="text-sm text-nb-400">
        Send this to {invite.label ? <strong className="text-white">{invite.label}</strong> : 'the person you are inviting'}. It lets one person join <strong className="text-white">{org}</strong> as {ROLE_LABEL[invite.role].toLowerCase()}, once, until {when(invite.expiresAt)}.
        It is shown only now; if it is lost, make another.
      </p>
      <div className="mt-4 space-y-3">
        <div>
          <div className="mb-1 text-xs text-nb-500">Link</div>
          <div className="flex items-center gap-2 rounded-md border border-nb-800 bg-nb-925 px-3 py-2">
            <code className="flex-1 select-all break-all font-mono text-xs text-white" data-testid="invite-link">{link}</code>
            <Button size="sm" onClick={() => void copy('link', link)}><Link2 size={13} /> {copied === 'link' ? 'Copied' : 'Copy'}</Button>
          </div>
        </div>
        <div>
          <div className="mb-1 text-xs text-nb-500">Or just the code, for someone who already has an account</div>
          <div className="flex items-center gap-2 rounded-md border border-nb-800 bg-nb-925 px-3 py-2">
            <code className="flex-1 select-all break-all font-mono text-xs text-white" data-testid="invite-code">{token}</code>
            <Button size="sm" onClick={() => void copy('code', token)}><Copy size={13} /> {copied === 'code' ? 'Copied' : 'Copy'}</Button>
          </div>
        </div>
      </div>
    </Modal>
  )
}

function Danger() {
  const conn = useServer((s) => s.conn)
  const info = useServer((s) => s.info)
  const reloadOrgs = useServer((s) => s.reloadOrgs)
  const [name, setName] = useState('')
  const [confirm, setConfirm] = useState('')
  const [open, setOpen] = useState(false)
  const [error, setError] = useState('')
  const [saved, setSaved] = useState(false)
  useEffect(() => setName(info?.orgName ?? ''), [info?.orgName])

  const rename = async () => {
    const c = conn()
    if (!c) return
    try {
      setError('')
      await api.renameOrg(c, name.trim())
      setSaved(true)
      await reloadOrgs(c.org)
      const fresh = await api.info(c)
      useServer.setState({ info: fresh })
    } catch (e) {
      setError(problem(e, 'Could not rename.'))
    }
  }
  const remove = async () => {
    const c = conn()
    if (!c) return
    try {
      setError('')
      await api.deleteOrg(c, confirm)
      setOpen(false)
      await reloadOrgs()
    } catch (e) {
      setError(problem(e, 'Could not delete.'))
    }
  }

  return (
    <section className="mt-10" aria-labelledby="org-h">
      <h2 id="org-h" className="mb-3 text-sm font-medium text-white">Organisation</h2>
      <ErrorLine text={error} />
      <form className="flex max-w-lg items-end gap-2" onSubmit={(e) => { e.preventDefault(); void rename() }}>
        <Field label="Name" className="flex-1"><Input value={name} onChange={(e) => { setName(e.target.value); setSaved(false) }} data-testid="org-name" /></Field>
        <Button type="submit" disabled={name.trim().length < 2 || name.trim() === info?.orgName}>{saved ? 'Saved' : 'Rename'}</Button>
      </form>
      <div className="mt-6 max-w-lg rounded-lg border border-red-500/25 bg-red-500/5 p-4">
        <div className="text-sm font-medium text-red-200">Delete this organisation</div>
        <p className="mt-1 text-xs text-nb-400">Erases its agents, topology, history and memberships for good. The people keep their accounts and their other organisations. The audit trail of who deleted it is kept.</p>
        <Button className="mt-3" variant="danger" onClick={() => { setConfirm(''); setOpen(true) }} data-testid="org-delete"><Trash2 size={14} /> Delete organisation…</Button>
      </div>
      <Modal
        open={open}
        onClose={() => setOpen(false)}
        title="Delete this organisation?"
        description="This cannot be undone."
        width="max-w-md"
        footer={<><Button onClick={() => setOpen(false)}>Cancel</Button><Button variant="danger" onClick={remove} disabled={confirm.trim() !== info?.orgName} data-testid="org-delete-confirm">Delete for good</Button></>}
      >
        <Field label={`Type “${info?.orgName ?? ''}” to confirm`}><Input value={confirm} onChange={(e) => setConfirm(e.target.value)} autoFocus data-testid="org-delete-name" /></Field>
      </Modal>
    </section>
  )
}

/** Who is in this organisation, what each may do, and how to bring someone in. */
export default function TeamPage() {
  const conn = useServer((s) => s.conn)
  const role = useServer((s) => s.role)
  const info = useServer((s) => s.info)
  const url = useServer((s) => s.url)
  const reloadOrgs = useServer((s) => s.reloadOrgs)
  const [members, setMembers] = useState<Member[]>([])
  const [invites, setInvites] = useState<Invite[]>([])
  const [error, setError] = useState('')
  const [inviting, setInviting] = useState(false)
  const [invRole, setInvRole] = useState<Role>('viewer')
  const [label, setLabel] = useState('')
  const [created, setCreated] = useState<{ token: string; invite: Invite } | null>(null)
  const mine = grantable(role)
  const canInvite = mine.length > 0

  const load = useCallback(async () => {
    const c = conn()
    if (!c) return
    try {
      setMembers(await api.members(c))
      setInvites(canInvite ? await api.invites(c) : [])
      setError('')
    } catch (e) {
      setError(problem(e, 'Could not load the members.'))
    }
  }, [conn, canInvite])
  useEffect(() => {
    void load()
  }, [load, info?.orgId])

  const act = async (f: () => Promise<void>, fallback: string) => {
    try {
      setError('')
      await f()
      await load()
    } catch (e) {
      setError(problem(e, fallback))
    }
  }

  const invite = () =>
    act(async () => {
      const c = conn()
      if (!c) return
      const r = await api.createInvite(c, invRole, label.trim())
      setInviting(false)
      setLabel('')
      setCreated(r)
    }, 'Could not send the invite.')

  if (!role) return null
  const open = invites.filter((i) => !i.used && !i.expired)
  const past = invites.filter((i) => i.used || i.expired)

  return (
    <>
      <PageHeader
        title="Members & access"
        description={`People in ${info?.orgName ?? 'this organisation'}. Everything here (clusters, agents, history, settings) belongs to this organisation alone; nobody outside it can see it.`}
        actions={canInvite ? <Button variant="primary" onClick={() => { setInvRole(mine.includes('viewer') ? 'viewer' : mine[0]); setInviting(true) }} data-testid="invite-open"><UserPlus size={16} /> Invite someone</Button> : undefined}
      />
      <ErrorLine text={error} />

      <Table>
        <thead>
          <tr><Th>Person</Th><Th>Role</Th><Th>Joined</Th><Th>Last sign-in</Th><Th /></tr>
        </thead>
        <tbody>
          {members.map((m) => {
            const manageable = !m.you && mine.includes(m.role)
            return (
              <tr key={m.id} className="group hover:bg-nb-930/60" data-testid={`member-${m.username}`}>
                <Td><span className="text-white">{m.username}</span>{m.you && <span className="ml-2 text-xs text-nb-500">(you)</span>}</Td>
                <Td>
                  {manageable ? (
                    <Select value={m.role} aria-label={`Role of ${m.username}`} onChange={(e) => void act(async () => { const c = conn(); if (c) await api.setMemberRole(c, m.id, e.target.value as Role) }, 'Could not change the role.')} className="h-8 w-36">
                      {mine.map((r) => (
                        <option key={r} value={r}>{ROLE_LABEL[r]}</option>
                      ))}
                    </Select>
                  ) : (
                    <Pill>{ROLE_LABEL[m.role]}</Pill>
                  )}
                </Td>
                <Td className="text-nb-500">{when(m.joinedAt)}</Td>
                <Td className="text-nb-500">{when(m.lastLogin)}</Td>
                <Td className="text-right">
                  {manageable && (
                    <Button size="sm" variant="danger" onClick={() => void act(async () => { const c = conn(); if (c) await api.removeMember(c, m.id) }, 'Could not remove the member.')}>Remove</Button>
                  )}
                  {m.you && (
                    <Button size="sm" onClick={() => void act(async () => { const c = conn(); if (!c) return; await api.leaveOrg(c); await reloadOrgs() }, 'Could not leave the organisation.')} data-testid="leave-org">Leave</Button>
                  )}
                </Td>
              </tr>
            )
          })}
        </tbody>
      </Table>

      {canInvite && (
        <section className="mt-10" aria-labelledby="inv-h">
          <h2 id="inv-h" className="mb-3 text-sm font-medium text-white">Invitations</h2>
          {invites.length === 0 ? (
            <p className="text-sm text-nb-500">None yet. An invitation is one link that lets one person join with the role you choose. It works once and expires after seven days.</p>
          ) : (
            <Table>
              <thead>
                <tr><Th>For</Th><Th>Role</Th><Th>Made by</Th><Th>Status</Th><Th /></tr>
              </thead>
              <tbody>
                {[...open, ...past].map((i) => (
                  <tr key={i.id} className="group hover:bg-nb-930/60" data-testid={`invite-${i.label || i.id}`}>
                    <Td className="text-white">{i.label || <span className="text-nb-500">anyone with the link</span>}</Td>
                    <Td><Pill>{ROLE_LABEL[i.role]}</Pill></Td>
                    <Td className="text-nb-500">{i.createdBy}</Td>
                    <Td className="text-nb-500">{i.used ? `used by ${i.usedBy || 'someone'}` : i.expired ? 'expired' : `open until ${when(i.expiresAt)}`}</Td>
                    <Td className="text-right">
                      {!i.used && !i.expired && mine.includes(i.role) && (
                        <Button size="sm" variant="danger" onClick={() => void act(async () => { const c = conn(); if (c) await api.revokeInvite(c, i.id) }, 'Could not withdraw the invite.')}>Withdraw</Button>
                      )}
                    </Td>
                  </tr>
                ))}
              </tbody>
            </Table>
          )}
        </section>
      )}

      <details className="group mt-10">
        <summary className="flex cursor-pointer select-none items-center gap-1.5 text-sm font-medium text-white marker:content-none">
          <ChevronRight size={14} className="text-nb-500 transition-transform group-open:rotate-90" aria-hidden />
          What each role can do
        </summary>
        <dl className="mt-3 grid max-w-3xl gap-x-6 gap-y-2 pl-[1.375rem] text-sm sm:grid-cols-[8rem_1fr]">
          {(['viewer', 'editor', 'admin', 'owner'] as Role[]).map((r) => (
            <div key={r} className="contents">
              <dt className="text-white">{ROLE_LABEL[r]}</dt>
              <dd className="text-nb-400">{ROLE_HELP[r]}</dd>
            </div>
          ))}
        </dl>
      </details>

      {role === 'owner' && <Danger />}

      <Modal
        open={inviting}
        onClose={() => setInviting(false)}
        title="Invite someone"
        description="They join this organisation with the role you pick. There is no email: you send them the link yourself."
        width="max-w-md"
        footer={<><Button onClick={() => setInviting(false)}>Cancel</Button><Button variant="primary" onClick={invite} data-testid="invite-create">Create invitation</Button></>}
      >
        <form className="space-y-4" onSubmit={(e) => { e.preventDefault(); void invite() }}>
          <Field label="Role" hint={ROLE_HELP[invRole]}>
            <Select value={invRole} onChange={(e) => setInvRole(e.target.value as Role)} data-testid="invite-role">
              {mine.map((r) => (
                <option key={r} value={r}>{ROLE_LABEL[r]}</option>
              ))}
            </Select>
          </Field>
          <Field label="Who is it for?" hint="Optional. Only a note for you, so you can tell invitations apart."><Input value={label} onChange={(e) => setLabel(e.target.value)} maxLength={80} data-testid="invite-label" /></Field>
        </form>
      </Modal>
      {created && <InviteCreated token={created.token} invite={created.invite} url={url} org={info?.orgName ?? 'this organisation'} onClose={() => setCreated(null)} />}
    </>
  )
}
