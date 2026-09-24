import { useMemo } from 'react'
import { create } from 'zustand'
import { api, ApiError, atLeast, probe, twoFactorPending, type Conn, type InvitePreview, type OrgRef, type Registration, type Role, type ServerInfo, type Session, type TwoFactorMethod, type User } from '@/lib/api'
import { mergeDiscovered, type ServerState } from '@/lib/discovered'
import { createPasskey, getPasskey, passkeyErrorMessage } from '@/lib/webauthn'
import { useHistoryView } from './history'
import { useObserved } from './observed'
import { useSettings } from './settings'
import { useRawTopology } from './topology'
import { forgetPending, useWorkspace } from './workspace'

/**
 * Connection to a Continuum server. Only the server's address and the last organisation used are remembered (in localStorage).
 * The sign-in itself is an HttpOnly cookie the browser holds; this code never sees the session
 * secret or the password after submitting it. Once signed in, the server's workspace replaces the
 * browser's copy and every poll merges what agents discovered into it, keeping human overrides.
 */
const URL_KEY = 'continuum-server/url'
const ORG_KEY = 'continuum-server/org'
/** Which organisation the topology copy held in this browser mirrors, so it is never shown to, or uploaded into, another. */
const OWNER_KEY = 'continuum-workspace-owner'

const read = (k: string) => {
  try {
    return localStorage.getItem(k) ?? ''
  } catch {
    return ''
  }
}
const write = (k: string, v: string) => {
  try {
    if (v) localStorage.setItem(k, v)
    else localStorage.removeItem(k)
  } catch {
    /* storage unavailable: the address is simply not remembered */
  }
}

export type ServerStatus =
  | 'disconnected' // no server in use: the app works on this browser's own copy
  | 'connecting'
  | 'signin' // a server answered and wants credentials
  | 'twofactor' // the password was right; a code from an authenticator app (or a recovery code) finishes it
  | 'connected' // signed in
  | 'error' // could not reach a server at that address

interface ServerStore {
  url: string
  status: ServerStatus
  error?: string
  user?: User
  /** Every organisation the person belongs to, with their role in each. */
  orgs: OrgRef[]
  /** The organisation in use, and what the person may do in it. */
  orgId?: string
  role?: Role
  /** Who may create an account on this server. */
  registration: Registration
  /** An invitation the person arrived with (a link), waiting to be used. */
  invite?: { token: string; preview?: InvitePreview }
  info?: ServerInfo
  state?: ServerState
  /** True once the page has asked whether a server is around, so the UI does not flash the wrong screen. */
  checked: boolean
  /** Set while status is 'twofactor': the token verifyTwoFactor must send back with the code. */
  pendingLogin?: string
  /** Set alongside pendingLogin: which methods this account can complete the sign-in with. */
  pendingMethods: TwoFactorMethod[]

  connect: (url: string) => Promise<boolean>
  signIn: (username: string, password: string) => Promise<boolean>
  /** Finishes a sign-in that stopped at status 'twofactor': code is a 6-digit authenticator code, a recovery code, or an emailed code. */
  verifyTwoFactor: (code: string) => Promise<boolean>
  /** Mails a fresh code for the pending sign-in (only meaningful when pendingMethods includes 'email'). */
  requestLoginEmailCode: () => Promise<boolean>
  /** Finishes a sign-in that offers 'webauthn' as a method, running the browser's passkey ceremony in
   *  between (only meaningful when pendingMethods includes 'webauthn'). Unlike verifyTwoFactor this opens
   *  the session itself rather than taking a typed code. */
  signInWithPasskey: () => Promise<boolean>
  /** Abandons a pending two-factor sign-in and goes back to the sign-in form. */
  cancelTwoFactor: () => void
  register: (username: string, password: string, orgName: string, invite?: string) => Promise<boolean>
  signOut: () => Promise<void>
  /** Switch to another of the person's organisations (or, with undefined, to none). */
  selectOrg: (id: string | undefined) => Promise<void>
  createOrg: (name: string) => Promise<boolean>
  /** Use an invitation code the person was given; resolves to an error message, or '' when they joined. */
  joinWithInvite: (token: string) => Promise<string>
  /** Re-read the person's organisations (after joining, leaving or being removed). */
  reloadOrgs: (prefer?: string) => Promise<void>
  setInvite: (token: string | undefined) => Promise<void>
  changePassword: (current: string, next: string) => Promise<boolean>
  /** Starts turning on two-factor authentication: a fresh secret and its otpauth:// URI, or undefined on failure (see `error`). */
  setupTwoFactor: () => Promise<{ secret: string; otpauthUrl: string } | undefined>
  /** Confirms the setup with one code from it; resolves to this account's one-time recovery codes, or undefined on failure. */
  enableTwoFactor: (code: string) => Promise<string[] | undefined>
  /** Turns two-factor authentication off; needs the current password. */
  disableTwoFactor: (password: string) => Promise<boolean>
  /** Mails a verification code to a (possibly new) address; the address shows up right away, unverified. */
  requestEmailVerification: (email: string) => Promise<boolean>
  /** Proves the mailed code was received, marking the address verified. */
  confirmEmail: (code: string) => Promise<boolean>
  /** Turns email-OTP on; only possible once the address is verified. */
  enableEmailOTP: () => Promise<boolean>
  /** Turns email-OTP off; needs the current password. Leaves the address itself verified. */
  disableEmailOTP: (password: string) => Promise<boolean>
  /** Registers a new passkey/security key on the signed-in account, running the browser ceremony in
   *  between; name labels it in settings (blank gets a generic default from the server). */
  registerPasskey: (name: string) => Promise<boolean>
  /** Changes only a passkey's own label. */
  renamePasskey: (id: string, name: string) => Promise<boolean>
  /** Removes one passkey; needs the current password, the same as turning off any other second factor. */
  removePasskey: (id: string, password: string) => Promise<boolean>
  /** Stop using the server without signing out (its session stays valid); the browser keeps working alone. */
  disconnect: () => void
  refresh: () => Promise<void>
  /** Re-read the organisation's info (what the install command uses changes when Settings → Installation is saved). */
  reloadInfo: () => Promise<void>
  conn: () => Conn | null
  /** An administrator or owner of the organisation in use. */
  isAdmin: () => boolean
  /** May change the shared topology (editor or above). */
  canEdit: () => boolean
}

const messageOf = (e: unknown) => (e instanceof Error ? e.message : 'Something went wrong.')

export const useServer = create<ServerStore>((set, get) => {
  const wipeLocal = () => {
    useRawTopology.getState().clear()
    useObserved.getState().clear()
    useHistoryView.getState().live()
    useSettings.getState().clear()
  }

  /** Make one organisation the one in use: load its info, its shared workspace, then what its agents found. */
  const activate = async (orgId: string | undefined) => {
    await useWorkspace.getState().stop(true)
    const { url, orgs } = get()
    const org = orgs.find((o) => o.id === orgId)
    if (!org) {
      wipeLocal()
      write(OWNER_KEY, '')
      set({ orgId: undefined, role: undefined, info: undefined, state: undefined, status: 'connected' })
      return
    }
    // The browser's copy of a topology belongs to exactly one organisation. Another's is never shown or uploaded.
    const mark = `${url}|${org.id}`
    const owner = read(OWNER_KEY)
    if (owner && owner !== mark) wipeLocal()
    else {
      useObserved.getState().clear()
      useHistoryView.getState().live()
      useSettings.getState().clear()
    }
    write(OWNER_KEY, mark)
    write(ORG_KEY, org.id)
    const c: Conn = { url, org: org.id }
    set({ orgId: org.id, role: org.role, state: undefined, info: undefined, error: undefined })
    const info = await api.info(c)
    set({ info, role: info.role, status: 'connected' })
    await useWorkspace.getState().start(c, atLeast(info.role, 'editor'))
    await get().refresh()
    void useSettings.getState().load(c)
  }

  /** Signed in: learn which organisations the person is in (using an invitation they arrived with), then open one. */
  const enter = async (session: Session) => {
    const c: Conn = { url: get().url }
    set({ user: session.user, orgs: session.orgs, error: undefined })
    if (session.user.mustChangePassword) {
      set({ status: 'connected' }) // the app shows the change-password screen; nothing else is loaded yet
      return
    }
    let prefer = read(ORG_KEY)
    const inv = get().invite
    if (inv) {
      try {
        const joined = await api.acceptInvite(c, inv.token)
        set({ orgs: await api.orgs(c) })
        prefer = joined.id
      } catch (e) {
        set({ error: `The invitation could not be used: ${messageOf(e)}` })
      }
      set({ invite: undefined })
    }
    const { orgs } = get()
    await activate(orgs.find((o) => o.id === prefer)?.id ?? orgs[0]?.id)
  }

  const leave = async (flush: boolean) => {
    await useWorkspace.getState().stop(flush)
  }

  return {
    url: read(URL_KEY),
    status: 'disconnected',
    checked: false,
    orgs: [],
    registration: 'open',
    pendingMethods: [],

    conn: () => (get().status === 'connected' && !get().user?.mustChangePassword && get().orgId ? { url: get().url, org: get().orgId } : null),
    isAdmin: () => atLeast(get().role, 'admin'),
    canEdit: () => atLeast(get().role, 'editor'),

    connect: async (url) => {
      const c = { url: url.trim() }
      set({ status: 'connecting', error: undefined, pendingLogin: undefined })
      const found = await probe(c)
      if (found === 'none') {
        set({ status: 'error', error: `No Continuum server answered at ${c.url || 'this address'}. Check the address and that it is running.`, checked: true })
        return false
      }
      write(URL_KEY, c.url)
      set({ url: c.url, checked: true })
      try {
        set({ registration: (await api.serverInfo(c)).registration })
      } catch {
        /* an older server: assume sign-up is not offered */
        set({ registration: 'closed' })
      }
      const inv = get().invite
      if (inv && !inv.preview) {
        try {
          set({ invite: { token: inv.token, preview: await api.previewInvite(c, inv.token) } })
        } catch (e) {
          set({ invite: undefined, error: messageOf(e) })
        }
      }
      if (found === 'signin') {
        set({ status: 'signin' })
        return true
      }
      try {
        await enter(await api.me(c))
        return true
      } catch (e) {
        set({ status: 'error', error: messageOf(e) })
        return false
      }
    },

    signIn: async (username, password) => {
      const c = { url: get().url }
      set({ error: undefined, pendingLogin: undefined, pendingMethods: [] })
      try {
        await enter(await api.login(c, username.trim(), password))
        return true
      } catch (e) {
        if (twoFactorPending(e)) {
          set({ pendingLogin: e.body.pending, pendingMethods: e.body.methods, status: 'twofactor' })
          return false
        }
        set({ error: messageOf(e), status: get().status === 'connected' ? 'connected' : 'signin' })
        return false
      }
    },

    verifyTwoFactor: async (code) => {
      const pending = get().pendingLogin
      if (!pending) return false
      const c = { url: get().url }
      set({ error: undefined })
      try {
        await enter(await api.login2FA(c, pending, code.trim()))
        set({ pendingLogin: undefined, pendingMethods: [] })
        return true
      } catch (e) {
        set({ error: messageOf(e) })
        return false
      }
    },

    requestLoginEmailCode: async () => {
      const pending = get().pendingLogin
      if (!pending) return false
      const c = { url: get().url }
      set({ error: undefined })
      try {
        await api.requestLoginEmailCode(c, pending)
        return true
      } catch (e) {
        set({ error: messageOf(e) })
        return false
      }
    },

    signInWithPasskey: async () => {
      const pending = get().pendingLogin
      if (!pending) return false
      const c = { url: get().url }
      set({ error: undefined })
      try {
        const options = await api.beginPasskeyLogin(c, pending)
        const response = await getPasskey(options)
        await enter(await api.finishPasskeyLogin(c, pending, response))
        set({ pendingLogin: undefined, pendingMethods: [] })
        return true
      } catch (e) {
        set({ error: passkeyErrorMessage(e) })
        return false
      }
    },

    cancelTwoFactor: () => {
      set({ pendingLogin: undefined, pendingMethods: [], error: undefined, status: 'signin' })
    },

    register: async (username, password, orgName, invite) => {
      const c = { url: get().url }
      set({ error: undefined })
      try {
        await enter(await api.register(c, username.trim(), password, { org: orgName.trim(), invite }))
        return true
      } catch (e) {
        set({ error: messageOf(e), status: get().status === 'connected' ? 'connected' : 'signin' })
        return false
      }
    },

    selectOrg: async (id) => {
      try {
        await activate(id)
      } catch (e) {
        set({ error: messageOf(e) })
      }
    },

    createOrg: async (name) => {
      const c = { url: get().url }
      set({ error: undefined })
      try {
        const o = await api.createOrg(c, name.trim())
        set({ orgs: [...get().orgs, o] })
        await activate(o.id)
        return true
      } catch (e) {
        set({ error: messageOf(e) })
        return false
      }
    },

    joinWithInvite: async (token) => {
      const c = { url: get().url }
      try {
        const o = await api.acceptInvite(c, token.trim())
        set({ orgs: await api.orgs(c) })
        await activate(o.id)
        return ''
      } catch (e) {
        return messageOf(e)
      }
    },

    reloadOrgs: async (prefer) => {
      const c = { url: get().url }
      try {
        const orgs = await api.orgs(c)
        set({ orgs })
        const cur = get().orgId
        const want = prefer ?? (orgs.some((o) => o.id === cur) ? cur : orgs[0]?.id)
        if (want !== cur) await activate(want)
        else if (cur) set({ role: orgs.find((o) => o.id === cur)?.role })
      } catch (e) {
        set({ error: messageOf(e) })
      }
    },

    setInvite: async (token) => {
      if (!token) {
        set({ invite: undefined })
        return
      }
      set({ invite: { token } })
    },

    signOut: async () => {
      const c = { url: get().url }
      await leave(true)
      try {
        await api.logout(c)
      } catch {
        /* the cookie is cleared by the server when it can be reached; otherwise it expires on its own */
      }
      // The workspace lives on the server now. Do not leave a copy of it behind for the next person at this browser.
      wipeLocal()
      write(OWNER_KEY, '')
      forgetPending()
      set({ status: 'signin', user: undefined, orgs: [], orgId: undefined, role: undefined, state: undefined, info: undefined, error: undefined, pendingLogin: undefined })
    },

    changePassword: async (current, next) => {
      const c = { url: get().url }
      set({ error: undefined })
      try {
        await enter(await api.changePassword(c, current, next))
        return true
      } catch (e) {
        set({ error: messageOf(e) })
        return false
      }
    },

    setupTwoFactor: async () => {
      const c = { url: get().url }
      set({ error: undefined })
      try {
        return await api.setup2FA(c)
      } catch (e) {
        set({ error: messageOf(e) })
        return undefined
      }
    },

    enableTwoFactor: async (code) => {
      const c = { url: get().url }
      set({ error: undefined })
      try {
        const { recoveryCodes } = await api.enable2FA(c, code.trim())
        set((s) => (s.user ? { user: { ...s.user, twoFactorEnabled: true } } : {}))
        return recoveryCodes
      } catch (e) {
        set({ error: messageOf(e) })
        return undefined
      }
    },

    disableTwoFactor: async (password) => {
      const c = { url: get().url }
      set({ error: undefined })
      try {
        const session = await api.disable2FA(c, password)
        set({ user: session.user })
        return true
      } catch (e) {
        set({ error: messageOf(e) })
        return false
      }
    },

    requestEmailVerification: async (email) => {
      const c = { url: get().url }
      set({ error: undefined })
      try {
        const session = await api.requestEmailVerification(c, email.trim())
        set({ user: session.user })
        return true
      } catch (e) {
        set({ error: messageOf(e) })
        return false
      }
    },

    confirmEmail: async (code) => {
      const c = { url: get().url }
      set({ error: undefined })
      try {
        const session = await api.confirmEmail(c, code.trim())
        set({ user: session.user })
        return true
      } catch (e) {
        set({ error: messageOf(e) })
        return false
      }
    },

    enableEmailOTP: async () => {
      const c = { url: get().url }
      set({ error: undefined })
      try {
        const session = await api.enableEmailOTP(c)
        set({ user: session.user })
        return true
      } catch (e) {
        set({ error: messageOf(e) })
        return false
      }
    },

    disableEmailOTP: async (password) => {
      const c = { url: get().url }
      set({ error: undefined })
      try {
        const session = await api.disableEmailOTP(c, password)
        set({ user: session.user })
        return true
      } catch (e) {
        set({ error: messageOf(e) })
        return false
      }
    },

    registerPasskey: async (name) => {
      const c = { url: get().url }
      set({ error: undefined })
      try {
        const options = await api.beginPasskeyRegistration(c)
        const response = await createPasskey(options)
        const session = await api.finishPasskeyRegistration(c, name.trim(), response)
        set({ user: session.user })
        return true
      } catch (e) {
        set({ error: passkeyErrorMessage(e) })
        return false
      }
    },

    renamePasskey: async (id, name) => {
      const c = { url: get().url }
      set({ error: undefined })
      try {
        const session = await api.renamePasskey(c, id, name.trim())
        set({ user: session.user })
        return true
      } catch (e) {
        set({ error: messageOf(e) })
        return false
      }
    },

    removePasskey: async (id, password) => {
      const c = { url: get().url }
      set({ error: undefined })
      try {
        const session = await api.removePasskey(c, id, password)
        set({ user: session.user })
        return true
      } catch (e) {
        set({ error: messageOf(e) })
        return false
      }
    },

    disconnect: () => {
      void leave(true).then(forgetPending)
      write(URL_KEY, '')
      useObserved.getState().clear()
      useHistoryView.getState().live()
      useSettings.getState().clear()
      set({ url: '', status: 'disconnected', user: undefined, orgs: [], orgId: undefined, role: undefined, state: undefined, info: undefined, error: undefined, pendingLogin: undefined })
    },

    reloadInfo: async () => {
      const c = get().conn()
      if (get().status !== 'connected' || !c || !get().orgId) return
      try {
        set({ info: await api.info(c) })
      } catch {
        // the copy already held stays; the next poll or sign-in reads it again
      }
    },

    refresh: async () => {
      const { status, user } = get()
      const c = get().conn()
      if (status !== 'connected' || !c || !user) return
      try {
        const state = await api.state(c)
        set({ state, error: undefined })
        useObserved.getState().set(state.topology.dependencies, state.topology.externalEndpoints, state.topology.paths, state.tombstones)
        const raw = useRawTopology.getState()
        useRawTopology.setState(mergeDiscovered(raw, state))
        void useWorkspace.getState().poll()
      } catch (e) {
        if (e instanceof ApiError && e.status === 401) {
          // The session ended (expired, revoked, or the account was disabled).
          await leave(false)
          useObserved.getState().clear()
          useHistoryView.getState().live()
          set({ status: 'signin', user: undefined, orgs: [], orgId: undefined, role: undefined, state: undefined, error: 'Your session ended. Sign in again.' })
        } else if (e instanceof ApiError && e.status === 404) {
          // The organisation is gone, or the person was removed from it: find out where they still belong.
          void get().reloadOrgs()
        } else if (e instanceof ApiError && e.status === 403) {
          set({ error: e.message })
        } else {
          // A network hiccup keeps the connection; the next poll tries again.
          set({ error: messageOf(e) })
        }
      }
    },
  }
})

/**
 * On page load: reconnect to the remembered server, or notice that this page was itself served by
 * a Continuum server (the usual production setup) and go straight to its sign-in.
 */
export async function resumeServer() {
  // A link like https://server/?invite=cni_… carries an invitation. Keep the code, and take it out of the address bar.
  try {
    const u = new URL(window.location.href)
    const token = u.searchParams.get('invite')
    if (token) {
      u.searchParams.delete('invite')
      window.history.replaceState(null, '', u.pathname + (u.search || '') + u.hash)
      await useServer.getState().setInvite(token)
    }
  } catch {
    /* no window.location: nothing to read */
  }
  const { url, connect } = useServer.getState()
  if (url) {
    await connect(url)
  } else if ((await probe({ url: '' })) !== 'none') {
    await connect('')
  }
  useServer.setState({ checked: true })
}

/** The connection for the organisation that is open, for components that call the API. It is stable between renders. */
export function useConn(): Conn {
  const url = useServer((s) => s.url)
  const org = useServer((s) => s.orgId)
  return useMemo(() => ({ url, org }), [url, org])
}
