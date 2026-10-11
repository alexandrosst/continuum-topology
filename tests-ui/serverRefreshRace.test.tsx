import { beforeEach, describe, expect, test, vi } from 'vitest'

const apiMock = { state: vi.fn() }

vi.mock('@/lib/api', async () => {
  const actual = await vi.importActual<typeof import('@/lib/api')>('@/lib/api')
  return { ...actual, api: apiMock }
})
// The merge itself is not under test here; only which response the store applies.
vi.mock('@/lib/discovered', async () => {
  const actual = await vi.importActual<typeof import('@/lib/discovered')>('@/lib/discovered')
  return { ...actual, mergeDiscovered: (raw: unknown) => raw }
})

// Imported after the mocks so the store closes over the mocked `api`.
const { useServer } = await import('@/store/server')

function deferred<T>() {
  let resolve!: (v: T) => void
  let reject!: (e: unknown) => void
  const promise = new Promise<T>((res, rej) => {
    resolve = res
    reject = rej
  })
  return { promise, resolve, reject }
}

const stateFor = (tag: string) => ({ tag, tombstones: [], topology: { dependencies: [], externalEndpoints: [], paths: [], clusterLinks: [], clusterPairConnectivity: [], agents: [] } })

function connectTo(orgId: string) {
  useServer.setState({ url: 'https://a.example', status: 'connected', user: { name: 'ann' } as never, orgId, state: undefined, error: undefined })
}

beforeEach(() => {
  vi.clearAllMocks()
  // let any refresh left over from the previous test settle before the next starts
  connectTo('org-a')
})

describe('useServer.refresh in-flight sharing', () => {
  test('two calls for the same organisation share one request', async () => {
    const a = deferred<unknown>()
    apiMock.state.mockReturnValueOnce(a.promise)
    const first = useServer.getState().refresh()
    const second = useServer.getState().refresh()
    expect(apiMock.state).toHaveBeenCalledTimes(1)
    a.resolve(stateFor('a'))
    await Promise.all([first, second])
    expect(useServer.getState().state).toMatchObject({ tag: 'a' })
  })

  test('a request still pending for the organisation just left is not handed to the one just entered', async () => {
    const a = deferred<unknown>()
    apiMock.state.mockReturnValueOnce(a.promise)
    const forA = useServer.getState().refresh()

    // Switch organisation while A's request is still out, as activate() does before it awaits refresh().
    connectTo('org-b')
    const b = deferred<unknown>()
    apiMock.state.mockReturnValueOnce(b.promise)
    const forB = useServer.getState().refresh()
    expect(apiMock.state).toHaveBeenCalledTimes(2)
    expect(apiMock.state.mock.calls[1][0]).toMatchObject({ org: 'org-b' })

    b.resolve(stateFor('b'))
    await forB
    expect(useServer.getState().state).toMatchObject({ tag: 'b' })

    // A's slow answer arrives last and must change nothing.
    a.resolve(stateFor('a'))
    await forA
    expect(useServer.getState().state).toMatchObject({ tag: 'b' })

    // ... and its finishing must not have released the slot while B's own request is out: a later call for B shares it.
    const b2 = deferred<unknown>()
    apiMock.state.mockReturnValueOnce(b2.promise)
    const p1 = useServer.getState().refresh()
    const p2 = useServer.getState().refresh()
    expect(apiMock.state).toHaveBeenCalledTimes(3)
    b2.resolve(stateFor('b2'))
    await Promise.all([p1, p2])
  })

  test('a late failure for the organisation just left does not set an error on the current one', async () => {
    const a = deferred<unknown>()
    apiMock.state.mockReturnValueOnce(a.promise)
    const forA = useServer.getState().refresh()
    connectTo('org-b')
    a.reject(new Error('A timed out'))
    await forA
    expect(useServer.getState().error).toBeUndefined()
  })
})
