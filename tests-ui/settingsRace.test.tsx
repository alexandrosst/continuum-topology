import { beforeEach, describe, expect, test, vi } from 'vitest'
import type { Conn } from '@/lib/api'
import { DEFAULT_SETTINGS } from '@/lib/history'

const apiMock = {
  settings: vi.fn(),
  saveSettings: vi.fn(),
}

vi.mock('@/lib/api', async () => {
  const actual = await vi.importActual<typeof import('@/lib/api')>('@/lib/api')
  return { ...actual, api: apiMock }
})

// Imported after the mock so the store closes over the mocked `api`.
const { useSettings } = await import('@/store/settings')

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
  useSettings.setState({ settings: DEFAULT_SETTINGS, loaded: false, error: undefined })
})

describe('useSettings cross-organisation races', () => {
  test('a load left in flight when the organisation is switched does not clobber the new organisation', async () => {
    const slow = deferred<Partial<typeof DEFAULT_SETTINGS>>()
    apiMock.settings.mockReturnValueOnce(slow.promise)
    const loadDoneA = useSettings.getState().load(connA)

    // The organisation is left (a switch, a sign-out) before org A's settings come back - exactly what
    // server.ts's activate() does: clear() first, then load() the new organisation.
    useSettings.getState().clear()
    apiMock.settings.mockResolvedValueOnce({ ...DEFAULT_SETTINGS, consistencyMinutes: 42 })
    await useSettings.getState().load(connB)
    expect(useSettings.getState().settings.consistencyMinutes).toBe(42)

    // Org A's slow response finally resolves, well after org B is the live session.
    slow.resolve({ ...DEFAULT_SETTINGS, consistencyMinutes: 1 })
    await loadDoneA

    // Org B's settings must be exactly as they were - untouched by org A's stale response.
    expect(useSettings.getState().settings.consistencyMinutes).toBe(42)
  })

  test('a save left in flight when the organisation is switched does not clobber the new organisation', async () => {
    const slow = deferred<typeof DEFAULT_SETTINGS>()
    apiMock.saveSettings.mockReturnValueOnce(slow.promise)
    const saveDoneA = useSettings.getState().save(connA, { consistencyMinutes: 7 })

    useSettings.getState().clear()
    apiMock.settings.mockResolvedValueOnce({ ...DEFAULT_SETTINGS, consistencyMinutes: 42 })
    await useSettings.getState().load(connB)
    expect(useSettings.getState().settings.consistencyMinutes).toBe(42)

    slow.resolve({ ...DEFAULT_SETTINGS, consistencyMinutes: 7 })
    const ok = await saveDoneA
    expect(ok).toBe(false)

    expect(useSettings.getState().settings.consistencyMinutes).toBe(42)
  })
})
