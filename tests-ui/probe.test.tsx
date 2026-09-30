import { afterEach, describe, expect, test, vi } from 'vitest'
import { probe } from '@/lib/api'

// probe() hits GET /api/v1/auth/me once and turns the response into a status connect() can act on. Its
// whole reason to exist (see its own doc comment in api.ts) is to let a caller that gets a 'session' result
// use the session body it already carries instead of firing api.me() again for the exact same endpoint -
// these tests are really about that body surviving the round trip intact, not just the status flag.

function jsonResponse(status: number, body: unknown) {
  return new Response(JSON.stringify(body), { status, headers: { 'content-type': 'application/json' } })
}

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('probe (learns whether a server is there, and - on a session - carries its body along)', () => {
  test('a signed-in session (200): status "session", with the parsed session body attached', async () => {
    const session = { user: { id: 'u1', username: 'alexandrosst' }, orgs: [{ id: 'org1', name: 'Acme', role: 'admin' }] }
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse(200, session))
    vi.stubGlobal('fetch', fetchMock)

    const result = await probe({ url: 'http://test' })

    expect(result.status).toBe('session')
    expect(result.status === 'session' && result.session).toEqual(session)
    expect(fetchMock).toHaveBeenCalledTimes(1) // exactly one request - nothing here should need a second
    expect(fetchMock.mock.calls[0][0]).toBe('http://test/api/v1/auth/me')
  })

  test('not signed in (401): status "signin", no session to carry', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(jsonResponse(401, { error: 'not signed in' })))
    const result = await probe({ url: 'http://test' })
    expect(result).toEqual({ status: 'signin' })
  })

  test('a non-JSON response (a static host serving the app\'s own HTML, not a Continuum server): status "none"', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response('<!doctype html>', { status: 200, headers: { 'content-type': 'text/html' } })))
    const result = await probe({ url: 'http://test' })
    expect(result).toEqual({ status: 'none' })
  })

  test('an unreachable server (fetch itself rejects): status "none", not a thrown error', async () => {
    vi.stubGlobal('fetch', vi.fn().mockRejectedValue(new TypeError('Failed to fetch')))
    const result = await probe({ url: 'http://test' })
    expect(result).toEqual({ status: 'none' })
  })

  test('a 200 with a body that fails to parse as JSON: status "none" rather than a bogus session', async () => {
    const badRes = new Response('not actually json', { status: 200, headers: { 'content-type': 'application/json' } })
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(badRes))
    const result = await probe({ url: 'http://test' })
    expect(result).toEqual({ status: 'none' })
  })
})
