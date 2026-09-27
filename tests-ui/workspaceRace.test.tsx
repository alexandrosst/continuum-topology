import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'
import type { Conn } from '@/lib/api'

const apiMock = {
  workspace: vi.fn(),
  workspaceMeta: vi.fn(),
  saveWorkspace: vi.fn(),
}

vi.mock('@/lib/api', async () => {
  const actual = await vi.importActual<typeof import('@/lib/api')>('@/lib/api')
  return { ...actual, api: apiMock }
})

// Imported after the mock so the store closes over the mocked `api`.
const { useWorkspace } = await import('@/store/workspace')
const { useRawTopology } = await import('@/store/topology')

const connA: Conn = { url: 'https://a.example', org: 'org-a' }
const connB: Conn = { url: 'https://b.example', org: 'org-b' }

function deferred<T>() {
  let resolve!: (v: T) => void
  const promise = new Promise<T>((res) => {
    resolve = res
  })
  return { promise, resolve }
}

beforeEach(() => {
  vi.clearAllMocks()
  useRawTopology.getState().clear()
  useWorkspace.setState({ status: 'off', rev: 0, updatedBy: undefined, updatedAt: undefined, conflict: undefined, error: undefined, local: undefined, note: undefined })
})

afterEach(async () => {
  await useWorkspace.getState().stop(false)
})

describe('useWorkspace cross-session races', () => {
  test('a save left in flight when the organisation is switched does not clobber the new organisation', async () => {
    apiMock.workspace.mockResolvedValueOnce({ rev: 1, data: { schemaVersion: 4, clusters: [], nodes: [], services: [], dependencies: [] }, updatedBy: 'alice', updatedAt: 't1' })
    await useWorkspace.getState().start(connA, true)
    expect(useWorkspace.getState().rev).toBe(1)

    // A local edit makes the document differ from what was last saved, so saveNow() actually hits the network
    // instead of short-circuiting on "nothing changed".
    useRawTopology.setState({
      sites: [{ id: 's-a', orgId: 'org-a', name: 'A', kind: 'edge-site', lat: 0, lng: 0, country: 'GR' }] as never,
    })

    const slow = deferred<{ rev: number; updatedBy: string; updatedAt: string }>()
    apiMock.saveWorkspace.mockReturnValueOnce(slow.promise)
    const saveDone = useWorkspace.getState().saveNow()
    expect(useWorkspace.getState().status).toBe('saving')

    // Before org A's save comes back, the browser leaves that organisation (a switch, a sign-out) - without
    // flushing, exactly as a fast org switch would.
    await useWorkspace.getState().stop(false)
    apiMock.workspace.mockResolvedValueOnce({ rev: 9, data: { schemaVersion: 4, clusters: [], nodes: [], services: [], dependencies: [] }, updatedBy: 'bob', updatedAt: 't9' })
    await useWorkspace.getState().start(connB, true)
    expect(useWorkspace.getState().rev).toBe(9)
    expect(useWorkspace.getState().status).toBe('saved')

    // Org A's slow save finally resolves, well after org B is the live session.
    slow.resolve({ rev: 2, updatedBy: 'alice', updatedAt: 't2' })
    await saveDone

    // Org B's state must be exactly as it was - untouched by org A's stale response.
    expect(useWorkspace.getState().rev).toBe(9)
    expect(useWorkspace.getState().updatedBy).toBe('bob')
    expect(useWorkspace.getState().updatedAt).toBe('t9')
    expect(useWorkspace.getState().status).toBe('saved')
  })

  test('a poll answered after the organisation changed does not resurrect the old organisation as a conflict', async () => {
    apiMock.workspace.mockResolvedValueOnce({ rev: 1, data: { schemaVersion: 4, clusters: [], nodes: [], services: [], dependencies: [] }, updatedBy: 'alice', updatedAt: 't1' })
    await useWorkspace.getState().start(connA, true)

    const slowMeta = deferred<{ rev: number; updatedBy: string; updatedAt: string }>()
    apiMock.workspaceMeta.mockReturnValueOnce(slowMeta.promise)
    const pollDone = useWorkspace.getState().poll()

    await useWorkspace.getState().stop(false)
    apiMock.workspace.mockResolvedValueOnce({ rev: 9, data: { schemaVersion: 4, clusters: [], nodes: [], services: [], dependencies: [] }, updatedBy: 'bob', updatedAt: 't9' })
    await useWorkspace.getState().start(connB, true)

    // A newer revision than org B's current one - would normally read as "someone else saved" for org A.
    slowMeta.resolve({ rev: 5, updatedBy: 'carol', updatedAt: 't5' })
    await pollDone

    expect(useWorkspace.getState().rev).toBe(9)
    expect(useWorkspace.getState().status).toBe('saved')
    expect(useWorkspace.getState().conflict).toBeUndefined()
  })
})
