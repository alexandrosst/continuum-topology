// The shared-workspace behaviour of the browser: loading, saving, conflicts, read-only, sign-out.
// A small in-memory server stands in for the real one; only fetch and localStorage are faked.
import assert from 'node:assert/strict'

const mem = new Map<string, string>()
const storage = {
  getItem: (k: string) => mem.get(k) ?? null,
  setItem: (k: string, v: string) => void mem.set(k, String(v)),
  removeItem: (k: string) => void mem.delete(k),
  get length() {
    return mem.size
  },
  key: (i: number) => [...mem.keys()][i] ?? null,
}
Object.defineProperty(globalThis, 'window', { value: globalThis, configurable: true }) // zustand's persist reads window.localStorage
// Recent Node versions define their own localStorage accessor, so plain assignment is not enough.
for (const k of ['localStorage', 'sessionStorage']) Object.defineProperty(globalThis, k, { value: storage, configurable: true, writable: true })

// ---- fake server ----
type Doc = { rev: number; data?: unknown; updatedBy: string; updatedAt: string }
type Role = 'viewer' | 'editor' | 'admin' | 'owner'
const server = {
  // "org-1" is the organisation most tests work in; "org-2" only exists when a test turns it on.
  ws: { rev: 0, updatedBy: '', updatedAt: '' } as Doc,
  role: 'admin' as Role,
  ws2: { rev: 0, updatedBy: '', updatedAt: '' } as Doc,
  role2: 'owner' as Role,
  second: false,
  member1: true, // is the person a member of org-1
  invite: null as null | { token: string; org: string; role: Role },
  signedIn: false,
  puts: 0,
  putsTo: [] as string[],
  mustChange: false,
  reset() {
    this.ws = { rev: 0, updatedBy: '', updatedAt: '' }
    this.role = 'admin'
    this.ws2 = { rev: 0, updatedBy: '', updatedAt: '' }
    this.role2 = 'owner'
    this.second = false
    this.member1 = true
    this.invite = null
    this.signedIn = true
    this.puts = 0
    this.putsTo = []
    this.mustChange = false
  },
  orgs() {
    return [
      ...(this.member1 ? [{ id: 'org-1', name: 'Org One', role: this.role }] : []),
      ...(this.second ? [{ id: 'org-2', name: 'Org Two', role: this.role2 }] : []),
    ]
  },
}
const json = (status: number, body: unknown) => new Response(JSON.stringify(body), { status, headers: { 'content-type': 'application/json' } })
const user = () => ({ id: 'u-1', username: 'alex', mustChangePassword: server.mustChange, createdAt: '2026-01-01T00:00:00Z' })
const session = () => ({ user: user(), orgs: server.orgs() })

;(globalThis as any).fetch = async (input: string, init: RequestInit = {}) => {
  const url = new URL(input, 'http://server.test')
  const method = init.method ?? 'GET'
  const headers = (init.headers ?? {}) as Record<string, string>
  assert.equal(init.credentials, 'include', 'every call must carry the session cookie')
  assert.ok(headers['X-Requested-With'], 'every call must carry the CSRF header')
  assert.ok(!('Authorization' in headers), 'no bearer token any more')
  let p = url.pathname
  if (p === '/api/v1/server') return json(200, { registration: 'open', version: 'test' })
  if (p === '/api/v1/auth/me') return server.signedIn ? json(200, session()) : json(401, { error: 'sign in required' })
  if (p === '/api/v1/auth/login') {
    const b = JSON.parse(String(init.body))
    if (b.password !== 'right password!') return json(401, { error: 'wrong username or password' })
    server.signedIn = true
    return json(200, session())
  }
  if (p === '/api/v1/auth/logout') {
    server.signedIn = false
    return new Response(null, { status: 204 })
  }
  if (p === '/api/v1/invites/preview') {
    const b = JSON.parse(String(init.body))
    return server.invite && b.token === server.invite.token ? json(200, { organisation: 'Org Two', role: server.invite.role, expiresAt: '2030-01-01T00:00:00Z' }) : json(400, { error: 'this invitation is invalid, expired or already used' })
  }
  if (!server.signedIn) return json(401, { error: 'sign in required' })
  if (p === '/api/v1/invites/accept') {
    const b = JSON.parse(String(init.body))
    if (!server.invite || b.token !== server.invite.token) return json(400, { error: 'this invitation is invalid, expired or already used' })
    server.second = true
    server.role2 = server.invite.role
    server.invite = null
    return json(200, { id: 'org-2', name: 'Org Two', role: server.role2 })
  }
  if (p === '/api/v1/orgs') return json(200, server.orgs())

  // Everything else is about one organisation, and must say which.
  const m = /^\/api\/v1\/orgs\/([^/]+)(\/.*)$/.exec(p)
  if (!m) return json(404, { error: 'not scoped to an organisation: ' + p })
  const org = m[1]
  p = '/api/v1' + m[2]
  const mine = server.orgs().find((o) => o.id === org)
  if (!mine) return json(404, { error: 'no such organisation' })
  const doc = () => (org === 'org-1' ? server.ws : server.ws2)
  const setDoc = (d: Doc) => (org === 'org-1' ? (server.ws = d) : (server.ws2 = d))
  if (p === '/api/v1/info') return json(200, { orgId: org, orgName: mine.name, role: mine.role, agentAddress: 'x:1', caPin: 'ab', version: 'test', implementedTier: 2 })
  if (p === '/api/v1/state') return json(200, { generatedAt: new Date().toISOString(), agents: [], topology: { clusters: [], nodes: [], namespaces: [], services: [], suggestions: [] }, auditLog: [] })
  if (p === '/api/v1/workspace' && method === 'GET') {
    const { data, ...meta } = doc()
    return json(200, url.searchParams.get('meta') || doc().rev === 0 ? meta : { ...meta, data })
  }
  if (p === '/api/v1/workspace' && method === 'PUT') {
    if (mine.role === 'viewer') return json(403, { error: 'your role in this organisation (viewer) does not allow this' })
    server.puts++
    server.putsTo.push(org)
    const rev = Number(headers['If-Match'])
    if (rev !== doc().rev) return json(409, { error: 'changed', rev: doc().rev, updatedBy: doc().updatedBy, updatedAt: doc().updatedAt })
    const d = setDoc({ rev: rev + 1, data: JSON.parse(String(init.body)), updatedBy: 'alex', updatedAt: new Date().toISOString() })
    return json(200, { rev: d.rev, updatedBy: 'alex', updatedAt: d.updatedAt })
  }
  return json(404, { error: 'not found ' + p })
}

// The stores read localStorage when they are created, so import them only now.
const { useRawTopology, exportTopology } = await import('../src/store/topology')
const { useWorkspace, forgetPending } = await import('../src/store/workspace')
const { useServer } = await import('../src/store/server')
const { seedTopology } = await import('../src/lib/seed')
const { SCHEMA_VERSION } = await import('../src/lib/types')

const C = { url: '', org: 'org-1' }
let failed = 0
const test = async (name: string, fn: () => Promise<void>) => {
  try {
    server.reset()
    forgetPending()
    useRawTopology.getState().reset() // a fresh browser holds the sample data
    await fn()
    console.log('PASS', name)
  } catch (e) {
    failed++
    console.log('FAIL', name, '\n  ', (e as Error).message)
  } finally {
    await useWorkspace.getState().stop(false)
    server.signedIn = true
  }
}
const status = () => useWorkspace.getState().status
const clusterNames = () => useRawTopology.getState().clusters.map((c) => c.name)
const addCluster = (name: string) =>
  useRawTopology.getState().upsertCluster({ ...seedTopology().clusters[0], id: 'cl-' + name, name, source: 'manual' })
const serverDoc = (names: string[]) => ({ ...exportTopology(), clusters: names.map((n) => ({ ...seedTopology().clusters[0], id: 'cl-' + n, name: n, source: 'manual' as const })) })

server.signedIn = true

await test('an untouched sample browser adopts the empty server workspace silently', async () => {
  await useWorkspace.getState().start(C, true)
  assert.equal(status(), 'saved')
  assert.deepEqual(clusterNames(), [], 'the sample data must not leak into a real workspace')
  assert.equal(server.puts, 0, 'nothing to save yet')
})

await test('signing in again with an already empty browser model does not ask what to do with it', async () => {
  server.ws = { rev: 0 } as any
  await useWorkspace.getState().start(C, true)
  await useWorkspace.getState().start(C, true)
  assert.equal(status(), 'saved')
})

await test('the server copy replaces whatever the browser held', async () => {
  server.ws = { rev: 4, data: serverDoc(['from-server']), updatedBy: 'sam', updatedAt: '2026-09-01T00:00:00Z' }
  await useWorkspace.getState().start(C, true)
  assert.deepEqual(clusterNames(), ['from-server'])
  assert.equal(useWorkspace.getState().rev, 4)
  assert.equal(status(), 'saved')
  assert.equal(server.puts, 0, 'loading must not write back')
})

await test('edits are saved with the revision the browser last saw', async () => {
  server.ws = { rev: 4, data: serverDoc(['a']), updatedBy: 'sam', updatedAt: '2026-09-01T00:00:00Z' }
  await useWorkspace.getState().start(C, true)
  addCluster('b')
  assert.equal(status(), 'dirty')
  await useWorkspace.getState().saveNow()
  assert.equal(status(), 'saved')
  assert.equal(server.ws.rev, 5)
  assert.equal(useWorkspace.getState().rev, 5)
  assert.deepEqual((server.ws.data as any).clusters.map((c: any) => c.name).sort(), ['a', 'b'])
  await useWorkspace.getState().saveNow()
  assert.equal(server.puts, 1, 'saving twice with no change must not write twice')
})

await test('what the server reports about agents is never saved as workspace content, and never lost on load', async () => {
  server.ws = { rev: 4, data: serverDoc(['a']), updatedBy: 'sam', updatedAt: '2026-09-01T00:00:00Z' }
  await useWorkspace.getState().start(C, true)
  const raw = useRawTopology.getState()
  const agent = { ...seedTopology().agents[0], id: 'ag-live', lastHeartbeat: '2026-09-19T10:00:00Z' }
  useRawTopology.setState({ agents: [agent], auditLog: [...raw.auditLog, { id: 'au-9', orgId: 'default', at: '2026-09-19T10:00:00Z', actor: 'admin', action: 'login', targetKind: 'user', targetId: 'u' } as any] })
  assert.equal(status(), 'saved', 'a heartbeat or a server audit event must not make the workspace dirty')
  useRawTopology.setState({ agents: [{ ...agent, lastHeartbeat: '2026-09-19T10:00:30Z' }] })
  assert.equal(status(), 'saved')
  addCluster('b')
  await useWorkspace.getState().saveNow()
  const saved = server.ws.data as any
  assert.deepEqual(saved.agents, [])
  assert.ok(!saved.auditLog.some((e: any) => e.id === 'au-9'))
  server.ws = { rev: 9, data: serverDoc(['a', 'b', 'c']), updatedBy: 'sam', updatedAt: '2026-09-02T00:00:00Z' }
  await useWorkspace.getState().poll()
  assert.deepEqual(clusterNames().sort(), ['a', 'b', 'c'])
  assert.deepEqual(useRawTopology.getState().agents.map((x) => x.id), ['ag-live'], 'loading their save keeps the live agents')
  assert.ok(useRawTopology.getState().auditLog.some((e) => e.id === 'au-9'))
})

await test('a server that returns keys in another order does not make the workspace look edited', async () => {
  const reorder = (v: any): any => (Array.isArray(v) ? v.map(reorder) : v && typeof v === 'object' ? Object.fromEntries(Object.keys(v).sort().reverse().map((k) => [k, reorder(v[k])])) : v)
  server.ws = { rev: 4, data: reorder(serverDoc(['a'])), updatedBy: 'sam', updatedAt: '2026-09-01T00:00:00Z' }
  await useWorkspace.getState().start(C, true)
  useRawTopology.setState({ clusters: useRawTopology.getState().clusters.map((c) => Object.fromEntries(Object.entries(c).sort(([x], [y]) => x.localeCompare(y))) as typeof c) }) // a merge that rebuilds records
  assert.equal(status(), 'saved')
  assert.equal(server.puts, 0)
})

await test('an edit made just before a reload is kept and saved, not replaced by the server copy', async () => {
  server.ws = { rev: 4, data: serverDoc(['a']), updatedBy: 'sam', updatedAt: '2026-09-01T00:00:00Z' }
  await useWorkspace.getState().start(C, true)
  addCluster('typed-just-now')
  assert.equal(status(), 'dirty')
  await useWorkspace.getState().stop(false) // the page goes away inside the save delay
  await useWorkspace.getState().start(C, true) // ...and comes back
  assert.equal(status(), 'dirty')
  assert.deepEqual(clusterNames().sort(), ['a', 'typed-just-now'])
  await useWorkspace.getState().saveNow()
  assert.equal(server.ws.rev, 5)
  assert.deepEqual((server.ws.data as any).clusters.map((c: any) => c.name).sort(), ['a', 'typed-just-now'])
  await useWorkspace.getState().stop(false)
  await useWorkspace.getState().start(C, true)
  assert.equal(status(), 'saved', 'once saved, nothing is held back on the next load')
})

await test('an edit held across a reload that meets a newer server copy becomes a conflict', async () => {
  server.ws = { rev: 4, data: serverDoc(['a']), updatedBy: 'sam', updatedAt: '2026-09-01T00:00:00Z' }
  await useWorkspace.getState().start(C, true)
  addCluster('mine')
  await useWorkspace.getState().stop(false)
  server.ws = { rev: 6, data: serverDoc(['a', 'theirs']), updatedBy: 'sam', updatedAt: '2026-09-02T00:00:00Z' }
  await useWorkspace.getState().start(C, true)
  assert.equal(status(), 'conflict')
  assert.ok(clusterNames().includes('mine'), 'nothing is discarded until the person chooses')
  await useWorkspace.getState().useTheirs()
  assert.deepEqual(clusterNames().sort(), ['a', 'theirs'])
})

await test('signing out forgets unsaved edits so the next person does not inherit them', async () => {
  server.ws = { rev: 4, data: serverDoc(['a']), updatedBy: 'sam', updatedAt: '2026-09-01T00:00:00Z' }
  await useWorkspace.getState().start(C, true)
  addCluster('left-behind')
  await useWorkspace.getState().stop(false)
  forgetPending()
  await useWorkspace.getState().start(C, true)
  assert.deepEqual(clusterNames(), ['a'])
})

await test('work already in the browser can be uploaded to an empty server, or dropped', async () => {
  addCluster('mine')
  await useWorkspace.getState().start(C, true)
  assert.equal(status(), 'choose')
  assert.ok(useWorkspace.getState().local!.clusters >= 1)
  assert.equal(server.puts, 0, 'nothing is uploaded before the person chooses')
  await useWorkspace.getState().adoptLocal()
  assert.equal(status(), 'saved')
  assert.equal(server.ws.rev, 1)
  assert.ok((server.ws.data as any).clusters.some((c: any) => c.name === 'mine'))

  server.reset()
  server.signedIn = true
  await useWorkspace.getState().stop(false)
  addCluster('discard-me')
  await useWorkspace.getState().start(C, true)
  await useWorkspace.getState().startEmpty()
  assert.deepEqual(clusterNames(), [])
  assert.equal(server.puts, 0)
})

await test('what an agent discovered is not the browser\'s own work: a reload after connecting a cluster does not ask to upload it', async () => {
  server.reset()
  server.signedIn = true
  await useWorkspace.getState().stop(false)
  useRawTopology.getState().upsertCluster({ ...seedTopology().clusters[0], id: 'cl-found', name: 'found-by-agent', source: 'discovered' })
  await useWorkspace.getState().start(C, true)
  assert.notEqual(status(), 'choose')
  assert.equal(useWorkspace.getState().local, undefined)
  assert.equal(server.puts, 0, 'discovered records are never uploaded as the browser\'s work')
})

await test('a save that loses the race becomes a conflict, and the person picks a side', async () => {
  server.ws = { rev: 1, data: serverDoc(['base']), updatedBy: 'sam', updatedAt: '2026-09-01T00:00:00Z' }
  await useWorkspace.getState().start(C, true)
  addCluster('mine')
  server.ws = { rev: 2, data: serverDoc(['base', 'theirs']), updatedBy: 'sam', updatedAt: '2026-09-02T00:00:00Z' } // someone else saved first
  await useWorkspace.getState().saveNow()
  assert.equal(status(), 'conflict')
  assert.equal(useWorkspace.getState().conflict!.updatedBy, 'sam')
  assert.equal(server.ws.rev, 2, 'the other person\'s save must not be overwritten')

  addCluster('while-in-conflict')
  await useWorkspace.getState().saveNow()
  assert.equal(server.puts, 2, 'no further saves are attempted while the conflict stands')

  await useWorkspace.getState().overwrite()
  assert.equal(status(), 'saved')
  assert.equal(server.ws.rev, 3)
  assert.ok(clusterNames().includes('mine') && (server.ws.data as any).clusters.some((c: any) => c.name === 'while-in-conflict'))

  // and the other way round
  addCluster('later')
  server.ws = { rev: 4, data: serverDoc(['final']), updatedBy: 'sam', updatedAt: '2026-09-03T00:00:00Z' }
  await useWorkspace.getState().saveNow()
  assert.equal(status(), 'conflict')
  await useWorkspace.getState().useTheirs()
  assert.equal(status(), 'saved')
  assert.deepEqual(clusterNames(), ['final'])
  assert.equal(useWorkspace.getState().rev, 4)
})

await test('polling loads someone else\'s save when nothing is unsaved, and flags a conflict when something is', async () => {
  server.ws = { rev: 1, data: serverDoc(['one']), updatedBy: 'sam', updatedAt: '2026-09-01T00:00:00Z' }
  await useWorkspace.getState().start(C, true)
  await useWorkspace.getState().poll()
  assert.equal(server.puts, 0)
  server.ws = { rev: 2, data: serverDoc(['one', 'two']), updatedBy: 'sam', updatedAt: '2026-09-02T00:00:00Z' }
  await useWorkspace.getState().poll()
  assert.deepEqual(clusterNames().sort(), ['one', 'two'])
  assert.equal(status(), 'saved')

  addCluster('unsaved')
  server.ws = { rev: 3, data: serverDoc(['three']), updatedBy: 'sam', updatedAt: '2026-09-03T00:00:00Z' }
  await useWorkspace.getState().poll()
  assert.equal(status(), 'conflict')
  assert.ok(clusterNames().includes('unsaved'), 'unsaved work must not be thrown away by a poll')
})

await test('a viewer follows the server but never saves', async () => {
  server.role = 'viewer'
  server.ws = { rev: 1, data: serverDoc(['one']), updatedBy: 'sam', updatedAt: '2026-09-01T00:00:00Z' }
  await useWorkspace.getState().start(C, false)
  assert.equal(status(), 'readonly')
  addCluster('local-only')
  await useWorkspace.getState().saveNow()
  assert.equal(server.puts, 0)
  server.ws = { rev: 2, data: serverDoc(['two']), updatedBy: 'sam', updatedAt: '2026-09-02T00:00:00Z' }
  await useWorkspace.getState().poll()
  assert.deepEqual(clusterNames(), ['two'], 'a viewer follows the shared copy')
})

await test('losing write permission mid-session turns the workspace read-only instead of erroring forever', async () => {
  server.ws = { rev: 1, data: serverDoc(['one']), updatedBy: 'sam', updatedAt: '2026-09-01T00:00:00Z' }
  await useWorkspace.getState().start(C, true)
  server.role = 'viewer'
  addCluster('x')
  await useWorkspace.getState().saveNow()
  assert.equal(status(), 'readonly')
})

await test('signing in loads the workspace; signing out saves pending work, then leaves nothing behind', async () => {
  server.signedIn = false
  server.ws = { rev: 1, data: serverDoc(['secret-topology']), updatedBy: 'sam', updatedAt: '2026-09-01T00:00:00Z' }
  assert.equal(await useServer.getState().connect(''), true)
  assert.equal(useServer.getState().status, 'signin')
  assert.equal(await useServer.getState().signIn('alex', 'wrong'), false)
  assert.match(useServer.getState().error ?? '', /wrong username or password/)
  assert.equal(useServer.getState().status, 'signin')
  assert.deepEqual(clusterNames().includes('secret-topology'), false, 'nothing is loaded before signing in')

  assert.equal(await useServer.getState().signIn('alex', 'right password!'), true)
  assert.equal(useServer.getState().status, 'connected')
  assert.deepEqual(clusterNames(), ['secret-topology'])
  assert.equal(useWorkspace.getState().status, 'saved')

  addCluster('typed-just-now')
  assert.equal(useWorkspace.getState().status, 'dirty')
  await useServer.getState().signOut()
  assert.ok((server.ws.data as any).clusters.some((c: any) => c.name === 'typed-just-now'), 'pending edits are flushed on sign-out')
  assert.equal(useServer.getState().status, 'signin')
  assert.equal(server.signedIn, false)
  assert.deepEqual(clusterNames(), [], 'the next person at this browser must not see the topology')
  assert.ok(!(mem.get('continuum-topology/v1') ?? '').includes('secret-topology'), 'and it must not remain in browser storage')
})

await test('a session that ends on the server sends the person back to sign-in', async () => {
  server.signedIn = false
  await useServer.getState().connect('')
  await useServer.getState().signIn('alex', 'right password!')
  server.signedIn = false // expired
  await useServer.getState().refresh()
  assert.equal(useServer.getState().status, 'signin')
  assert.match(useServer.getState().error ?? '', /session ended/i)
})

await test('an account that must change its password is held before the app loads', async () => {
  server.signedIn = false
  server.mustChange = true
  await useServer.getState().connect('')
  await useServer.getState().signIn('alex', 'right password!')
  assert.equal(useServer.getState().user?.mustChangePassword, true)
  assert.equal(useServer.getState().conn(), null, 'no API access from the UI until the password is changed')
  assert.equal(useWorkspace.getState().status, 'off', 'the workspace is not loaded yet')
})

await test('a static host that is not a Continuum server is ignored', async () => {
  const real = (globalThis as any).fetch
  ;(globalThis as any).fetch = async () => new Response('<html></html>', { status: 200, headers: { 'content-type': 'text/html' } })
  useServer.setState({ status: 'disconnected', url: '' })
  assert.equal(await useServer.getState().connect(''), false)
  assert.equal(useServer.getState().status, 'error')
  ;(globalThis as any).fetch = real
  useServer.setState({ status: 'disconnected', error: undefined })
})

const fresh = () => useServer.setState({ status: 'disconnected', user: undefined, orgs: [], orgId: undefined, role: undefined, info: undefined, state: undefined, invite: undefined, error: undefined })

await test('two organisations: switching loads the other one and nothing of the first stays in view or is saved to the second', async () => {
  fresh()
  server.second = true
  server.ws = { rev: 1, data: serverDoc(['one-secret']), updatedBy: 'sam', updatedAt: '2026-09-01T00:00:00Z' }
  server.ws2 = { rev: 1, data: serverDoc(['two-public']), updatedBy: 'kim', updatedAt: '2026-09-01T00:00:00Z' }
  server.signedIn = false
  await useServer.getState().connect('')
  await useServer.getState().signIn('alex', 'right password!')
  const first = useServer.getState().orgId
  assert.ok(first === 'org-1' || first === 'org-2')
  await useServer.getState().selectOrg('org-1')
  assert.deepEqual(clusterNames(), ['one-secret'])
  assert.equal(useServer.getState().role, 'admin')
  addCluster('typed-in-one')
  await useServer.getState().selectOrg('org-2')
  assert.equal(useServer.getState().orgId, 'org-2')
  assert.equal(useServer.getState().role, 'owner')
  assert.deepEqual(clusterNames(), ['two-public'], 'the first organisation must not be visible in the second')
  assert.ok((server.ws.data as any).clusters.some((c: any) => c.name === 'typed-in-one'), 'the pending edit was flushed to the organisation it was made in')
  assert.ok(!(server.ws2.data as any).clusters.some((c: any) => c.name === 'typed-in-one'), 'and never to the other one')
  assert.ok(server.putsTo.every((o) => o === 'org-1'))
  assert.equal(useServer.getState().conn()?.org, 'org-2', 'calls now go to the second organisation')
})

await test('a topology left in the browser by one organisation is not offered to, or shown in, another', async () => {
  fresh()
  server.member1 = false
  server.second = true
  server.ws2 = { rev: 0, updatedBy: '', updatedAt: '' }
  // Someone worked in org-1 in this browser and the tab was closed: their copy stays behind, marked as org-1's.
  mem.set('continuum-workspace-owner', '|org-1')
  useRawTopology.getState().clear()
  addCluster('left-behind-from-org-1')
  server.signedIn = false
  await useServer.getState().connect('')
  await useServer.getState().signIn('alex', 'right password!')
  assert.equal(useServer.getState().orgId, 'org-2')
  assert.deepEqual(clusterNames(), [], 'the other organisation\'s topology was wiped, not shown')
  assert.equal(useWorkspace.getState().status, 'saved', 'and the person is not asked whether to upload it')
  assert.equal(server.puts, 0)
})

await test('an invitation link: arrive, sign in, and land in the organisation it named', async () => {
  fresh()
  server.invite = { token: 'cni_' + 'a'.repeat(32), org: 'org-2', role: 'editor' }
  server.signedIn = false
  await useServer.getState().setInvite(server.invite.token)
  await useServer.getState().connect('')
  assert.equal(useServer.getState().status, 'signin')
  assert.equal(useServer.getState().invite?.preview?.organisation, 'Org Two', 'the sign-in screen can say what the invitation is for')
  await useServer.getState().signIn('alex', 'right password!')
  assert.equal(useServer.getState().orgId, 'org-2')
  assert.equal(useServer.getState().role, 'editor')
  assert.equal(useServer.getState().invite, undefined, 'a used invitation is dropped')
  assert.equal(useWorkspace.getState().status, 'saved', 'an editor can save')
  assert.equal(useServer.getState().orgs.length, 2)
})

await test('a person in no organisation is held on a screen of their own, with no API access', async () => {
  fresh()
  server.member1 = false
  server.signedIn = false
  await useServer.getState().connect('')
  await useServer.getState().signIn('alex', 'right password!')
  assert.equal(useServer.getState().status, 'connected')
  assert.equal(useServer.getState().orgId, undefined)
  assert.equal(useServer.getState().conn(), null)
  server.invite = { token: 'cni_' + 'b'.repeat(32), org: 'org-2', role: 'viewer' }
  assert.equal(await useServer.getState().joinWithInvite('cni_' + 'x'.repeat(32)), 'this invitation is invalid, expired or already used')
  assert.equal(await useServer.getState().joinWithInvite(server.invite.token), '')
  assert.equal(useServer.getState().orgId, 'org-2')
  assert.equal(useServer.getState().role, 'viewer')
  assert.equal(useWorkspace.getState().status, 'readonly')
})

await test('being removed from the organisation in use moves the person to one they still belong to', async () => {
  fresh()
  server.second = true
  server.signedIn = false
  await useServer.getState().connect('')
  await useServer.getState().signIn('alex', 'right password!')
  await useServer.getState().selectOrg('org-1')
  server.member1 = false // removed by an administrator
  await useServer.getState().refresh()
  await new Promise((r) => setTimeout(r, 50))
  assert.equal(useServer.getState().orgId, 'org-2')
})

assert.equal(SCHEMA_VERSION >= 3, true)
if (failed) {
  console.log(`\n${failed} failed`)
  process.exit(1)
}
console.log('\nall passed')
process.exit(0)
