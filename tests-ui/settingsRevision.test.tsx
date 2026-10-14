import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'
import type { Conn } from '@/lib/api'
import { DEFAULT_SETTINGS } from '@/lib/history'
import { SETTINGS_CONFLICT, useSettings } from '@/store/settings'

// The real api and store, with only fetch replaced: what goes over the wire is the point.
const conn: Conn = { url: 'https://x.example', org: 'org-1' }
const doc = (rev: number, extra: Record<string, unknown> = {}) => ({ ...DEFAULT_SETTINGS, rev, deciderConfigured: false, deciderSecretSet: false, ...extra })
const reply = (status: number, body: unknown) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })

const fetchMock = vi.fn()

beforeEach(() => {
  vi.stubGlobal('fetch', fetchMock)
  fetchMock.mockReset()
  useSettings.getState().clear()
})
afterEach(() => vi.unstubAllGlobals())

const sent = (i: number) => {
  const [url, init] = fetchMock.mock.calls[i] as [string, RequestInit]
  return { url, method: init.method, headers: init.headers as Record<string, string>, body: init.body ? JSON.parse(String(init.body)) : undefined }
}

describe('useSettings revisions', () => {
  test('a save sends the revision it started from in If-Match, never in the body, and keeps the new one', async () => {
    fetchMock.mockResolvedValueOnce(reply(200, doc(4)))
    await useSettings.getState().load(conn)
    expect(useSettings.getState().settings.rev).toBe(4)

    fetchMock.mockResolvedValueOnce(reply(200, doc(5, { retentionDays: 9 })))
    const s = useSettings.getState().settings
    expect(await useSettings.getState().save(conn, { ...s, retentionDays: 9 })).toBe(true)

    const put = sent(1)
    expect(put.method).toBe('PUT')
    expect(put.headers['If-Match']).toBe('4')
    expect(put.body.retentionDays).toBe(9)
    expect(put.body).not.toHaveProperty('rev')
    expect(useSettings.getState().settings.rev).toBe(5)
  })

  test('a save from a partial document uses the revision the store holds', async () => {
    fetchMock.mockResolvedValueOnce(reply(200, doc(7)))
    await useSettings.getState().load(conn)
    fetchMock.mockResolvedValueOnce(reply(200, doc(8)))
    await useSettings.getState().save(conn, { retentionDays: 3 })
    expect(sent(1).headers['If-Match']).toBe('7')
  })

  test('a save from a copy older than the store goes with the copy\'s revision, so the server refuses it', async () => {
    fetchMock.mockResolvedValueOnce(reply(200, doc(2)))
    await useSettings.getState().load(conn)
    const stale = useSettings.getState().settings
    useSettings.setState({ settings: { ...stale, rev: 3 } })
    fetchMock.mockResolvedValueOnce(reply(409, { ...doc(3), error: 'someone else changed the settings since you opened them' }))
    await useSettings.getState().save(conn, { ...stale, retentionDays: 9 })
    expect(sent(1).headers['If-Match']).toBe('2')
  })

  test('on 409 it adopts the current document, reports the conflict and returns false', async () => {
    fetchMock.mockResolvedValueOnce(reply(200, doc(1)))
    await useSettings.getState().load(conn)
    fetchMock.mockResolvedValueOnce(reply(409, { ...doc(2, { retentionDays: 77 }), error: 'someone else changed the settings since you opened them' }))
    const ok = await useSettings.getState().save(conn, { ...useSettings.getState().settings, retentionDays: 9 })

    expect(ok).toBe(false)
    const st = useSettings.getState()
    expect(st.error).toBe(SETTINGS_CONFLICT)
    expect(st.error).toMatch(/Someone else changed the settings; reloaded/)
    expect(st.settings.retentionDays).toBe(77)
    expect(st.settings.rev).toBe(2)
    expect(st.loaded).toBe(true)
    expect(fetchMock).toHaveBeenCalledTimes(2)

    // The next attempt starts from the reloaded document, and what the server told us is not sent back.
    fetchMock.mockResolvedValueOnce(reply(200, doc(3, { retentionDays: 9 })))
    expect(await useSettings.getState().save(conn, { ...st.settings, retentionDays: 9 })).toBe(true)
    expect(sent(2).headers['If-Match']).toBe('2')
    expect(sent(2).body).not.toHaveProperty('error')
    expect(useSettings.getState().error).toBeUndefined()
  })

  test('on a 409 without a document it reads the settings again', async () => {
    fetchMock.mockResolvedValueOnce(reply(200, doc(1)))
    await useSettings.getState().load(conn)
    fetchMock.mockResolvedValueOnce(reply(409, { error: 'conflict' }))
    fetchMock.mockResolvedValueOnce(reply(200, doc(6, { retentionDays: 12 })))
    expect(await useSettings.getState().save(conn, { retentionDays: 9 })).toBe(false)

    expect(sent(2).method).toBe('GET')
    expect(useSettings.getState().settings.rev).toBe(6)
    expect(useSettings.getState().settings.retentionDays).toBe(12)
    expect(useSettings.getState().error).toBe(SETTINGS_CONFLICT)
  })

  test('another failure keeps the server\'s message and the document as it was', async () => {
    fetchMock.mockResolvedValueOnce(reply(200, doc(1)))
    await useSettings.getState().load(conn)
    fetchMock.mockResolvedValueOnce(reply(400, { error: 'the snapshot interval is 1 to 1440' }))
    expect(await useSettings.getState().save(conn, { snapshotMinutes: 0 })).toBe(false)
    expect(useSettings.getState().error).toBe('the snapshot interval is 1 to 1440')
    expect(useSettings.getState().settings.rev).toBe(1)
  })
})
