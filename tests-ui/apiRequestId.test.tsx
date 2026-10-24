import { afterEach, describe, expect, test, vi } from 'vitest'
import { api, ApiError } from '@/lib/api'

const conn = { url: 'http://x', org: 'o' } as Parameters<typeof api.me>[0]
const answer = (status: number, body: unknown, id?: string) => new Response(JSON.stringify(body), { status, headers: id ? { 'X-Request-Id': id } : {} })

afterEach(() => vi.unstubAllGlobals())

describe('the request id of a failed request', () => {
  test('a failure of the server’s own carries its reference in the message, for whoever reads the log', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => answer(500, { error: 'internal error' }, 'abc123def4567890')))
    const e = await api.me(conn).catch((x) => x as ApiError)
    expect(e).toBeInstanceOf(ApiError)
    expect(e.message).toBe('internal error (reference abc123def4567890)')
    expect(e.requestId).toBe('abc123def4567890')
  })

  test('a refusal the person caused says what it said and keeps the id beside it', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => answer(403, { error: 'not allowed' }, 'abc123def4567890')))
    const e = await api.me(conn).catch((x) => x as ApiError)
    expect(e.message).toBe('not allowed')
    expect(e.requestId).toBe('abc123def4567890')
  })

  test('an older server with no id changes nothing', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => answer(500, { error: 'internal error' })))
    const e = await api.me(conn).catch((x) => x as ApiError)
    expect(e.message).toBe('internal error')
    expect(e.requestId).toBeUndefined()
  })
})
