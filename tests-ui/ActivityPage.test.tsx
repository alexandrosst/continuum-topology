import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, test, vi } from 'vitest'
import ActivityPage from '@/pages/ActivityPage'
import type { AuditRow } from '@/lib/api'

const audit = vi.fn(async (): Promise<{ rows: AuditRow[]; source: 'graph' | 'local' }> => ({ source: 'local', rows: [] }))
const workspaceRevisions = vi.fn(async () => [])

vi.mock('@/store/server', () => ({
  useServer: (selector: (s: { status: string; role: string }) => unknown) => selector({ status: 'connected', role: 'admin' }),
  useConn: () => ({ url: '', org: 'o' }),
}))
vi.mock('@/lib/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/lib/api')>()
  return {
    ...actual,
    api: { ...actual.api, audit: (...a: Parameters<typeof audit>) => audit(...a), workspaceRevisions: (...a: Parameters<typeof workspaceRevisions>) => workspaceRevisions(...a) },
  }
})

// jsdom has no real download machinery; stand in for the parts downloadCsv touches so we can assert on them.
const clickSpy = vi.fn()
const createObjectURL = vi.fn(() => 'blob:fake')
const revokeObjectURL = vi.fn()

beforeEach(() => {
  audit.mockClear()
  workspaceRevisions.mockClear()
  clickSpy.mockClear()
  createObjectURL.mockClear()
  revokeObjectURL.mockClear()
  vi.stubGlobal('URL', { ...URL, createObjectURL, revokeObjectURL })
  vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(clickSpy)
})

const row = (over: Partial<AuditRow> = {}): AuditRow => ({ id: 1, at: '2026-01-02T03:04:05Z', actor: 'ann', action: 'agent-approved', targetKind: 'agent', targetId: 'a1', detail: '', ...over })

describe('ActivityPage: CSV export', () => {
  test('the download button is disabled until rows have loaded, and disabled again when there are none', async () => {
    audit.mockResolvedValueOnce({ source: 'local', rows: [] })
    render(<ActivityPage />)
    await waitFor(() => expect(audit).toHaveBeenCalled())
    const btn = await screen.findByTestId('audit-download-csv')
    expect(btn).toBeDisabled()
  })

  test('clicking it downloads a CSV of exactly the loaded rows', async () => {
    audit.mockResolvedValueOnce({ source: 'local', rows: [row(), row({ id: 2, actor: 'bo', action: 'settings-changed', targetKind: undefined, targetId: undefined, detail: 'timezone' })] })
    render(<ActivityPage />)
    const btn = await screen.findByTestId('audit-download-csv')
    await waitFor(() => expect(btn).toBeEnabled())

    await userEvent.click(btn)

    expect(createObjectURL).toHaveBeenCalledTimes(1)
    const blob = createObjectURL.mock.calls[0][0] as Blob
    const text = await blob.text()
    expect(text).toBe(
      'When,Who,Did what,Target kind,Target,Detail\r\n' +
        '2026-01-02T03:04:05Z,ann,agent approved,agent,a1,\r\n' +
        '2026-01-02T03:04:05Z,bo,settings changed,,,timezone\r\n',
    )
    expect(clickSpy).toHaveBeenCalledTimes(1)
    expect(revokeObjectURL).toHaveBeenCalledWith('blob:fake')
  })
})
