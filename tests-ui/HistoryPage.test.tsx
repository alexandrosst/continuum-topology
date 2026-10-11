import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { describe, expect, test, vi } from 'vitest'
import HistoryPage, { RecordingSettings } from '@/pages/HistoryPage'
import { DEFAULT_SETTINGS, type AppSettings, type ChangeEvent, type HistoryIndex } from '@/lib/history'

// RecordingSettings is the "recording and checks" settings form on the History page. This file covers
// just the "Link is quiet after" field's unit picker (flowStaleSeconds) - the point of this whole feature
// is that the backend always stores/validates a plain number of seconds (see
// backend/internal/server/settings.go), while the field lets someone type that number in whichever unit
// is convenient (seconds through days) instead of doing the arithmetic themselves.

let settings: AppSettings
let save: ReturnType<typeof vi.fn>

vi.mock('@/store/settings', () => ({
  useSettings: Object.assign(
    (selector?: (s: unknown) => unknown) => {
      const s = { settings, loaded: true, error: undefined, save }
      return selector ? selector(s) : s
    },
    { getState: () => ({ clear: () => {} }) },
  ),
}))

// The page-level loader below talks to the server through these.
const history = vi.fn()
const events = vi.fn()
const recordNow = vi.fn(async () => undefined)
// One stable object: a fresh one per render would change `load` on every render and refetch forever.
const CONN = { url: 'https://example.test', org: 'o' }
vi.mock('@/store/server', () => ({
  useServer: (selector: (s: unknown) => unknown) => selector({ status: 'connected', role: 'admin', state: undefined }),
  useConn: () => CONN,
}))
vi.mock('@/lib/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/lib/api')>()
  return {
    ...actual,
    api: {
      ...actual.api,
      history: (...a: unknown[]) => history(...a),
      events: (...a: unknown[]) => events(...a),
      recordNow: (...a: unknown[]) => recordNow(...(a as [])),
      traffic: async () => ({ hours: 24, snapshots: 0, rates: [] }),
      storage: async () => {
        throw new Error('not under test')
      },
    },
  }
})

function renderField(over: Partial<AppSettings> = {}) {
  settings = { ...DEFAULT_SETTINGS, ...over }
  save = vi.fn().mockResolvedValue(true)
  render(
    <MemoryRouter>
      <RecordingSettings admin conn={{ url: 'https://example.test' }} />
    </MemoryRouter>,
  )
}

describe('RecordingSettings · Link is quiet after (flowStaleSeconds)', () => {
  test('300s shows as "5 minutes", not "300 seconds" or a fraction of an hour', () => {
    renderField({ flowStaleSeconds: 300 })
    expect(screen.getByTestId('setting-flowStaleSeconds')).toHaveValue(5)
    expect(screen.getByTestId('setting-flowStaleUnit')).toHaveValue('60')
  })

  test('a value that does not divide evenly into a larger unit falls back to seconds', () => {
    renderField({ flowStaleSeconds: 97 })
    expect(screen.getByTestId('setting-flowStaleSeconds')).toHaveValue(97)
    expect(screen.getByTestId('setting-flowStaleUnit')).toHaveValue('1')
  })

  test('switching the unit converts the displayed number, keeping the underlying seconds the same', () => {
    renderField({ flowStaleSeconds: 300 })
    fireEvent.change(screen.getByTestId('setting-flowStaleUnit'), { target: { value: '1' } })
    expect(screen.getByTestId('setting-flowStaleSeconds')).toHaveValue(300)
    fireEvent.change(screen.getByTestId('setting-flowStaleUnit'), { target: { value: '3600' } })
    // 300s doesn't divide evenly into hours; the nearest whole hour is what a real person would expect
    // from a unit switch, not a blocked/invalid field.
    expect(screen.getByTestId('setting-flowStaleUnit')).toHaveValue('3600')
  })

  test('typing 0 (or leaving it non-positive) is rejected and blocks Save', () => {
    renderField({ flowStaleSeconds: 300 })
    fireEvent.change(screen.getByTestId('setting-flowStaleSeconds'), { target: { value: '0' } })
    const field = screen.getByTestId('setting-flowStaleSeconds')
    expect(field).toHaveAttribute('aria-invalid', 'true')
    expect(screen.getByTestId('save-settings')).toBeDisabled()
    // A screen reader needs the error text itself, not just the invalid flag: aria-describedby must
    // resolve to the hint span that now reads the error message, not the normal explanatory text.
    const describedBy = field.getAttribute('aria-describedby')
    expect(describedBy).toBeTruthy()
    expect(document.getElementById(describedBy!)).toHaveTextContent(/between .* and 30 days/i)
  })

  test('Save sends the product of the typed number and the selected unit, in seconds', async () => {
    renderField({ flowStaleSeconds: 86400 }) // starts at 1 day
    fireEvent.change(screen.getByTestId('setting-flowStaleUnit'), { target: { value: '1' } }) // switch to seconds
    fireEvent.change(screen.getByTestId('setting-flowStaleSeconds'), { target: { value: '90' } })
    fireEvent.click(screen.getByTestId('save-settings'))
    await waitFor(() =>
      expect(save).toHaveBeenCalledWith(
        { url: 'https://example.test' },
        expect.objectContaining({ flowStaleSeconds: 90 }),
      ),
    )
  })
})

describe('HistoryPage loading', () => {
  const index: HistoryIndex = { points: [], snapshotMinutes: 5, retentionDays: 30 }
  const ev = (name: string): ChangeEvent => ({ id: name, at: '2026-01-02T03:04:05Z', kind: 'cluster-added', targetKind: 'service', targetId: name, name, detail: '', severity: 'info' })
  // The Select is a custom listbox, not a native <select>: open it and pick the option.
  const pickKind = (label: string) => {
    fireEvent.click(screen.getByRole('combobox', { name: 'Kind of change' }))
    fireEvent.click(screen.getByRole('option', { name: label }))
  }
  const renderPage = () => render(<MemoryRouter><HistoryPage /></MemoryRouter>)

  test('a filter change after a refresh sends one request per source, not two', async () => {
    settings = { ...DEFAULT_SETTINGS }
    history.mockReset().mockResolvedValue(index)
    events.mockReset().mockResolvedValue([])
    recordNow.mockClear()
    renderPage()
    await waitFor(() => expect(events).toHaveBeenCalledTimes(1))

    // "Record now" bumps the tick the cards below depend on; before the fix that also armed a second loader effect.
    fireEvent.click(await screen.findByRole('button', { name: /record now/i }))
    await waitFor(() => expect(events).toHaveBeenCalledTimes(2))
    expect(history).toHaveBeenCalledTimes(2)

    history.mockClear()
    events.mockClear()
    pickKind('Cluster appeared')
    await waitFor(() => expect(events).toHaveBeenCalled())
    await act(async () => {
      await new Promise((r) => setTimeout(r, 50))
    })
    expect(events).toHaveBeenCalledTimes(1)
    expect(history).toHaveBeenCalledTimes(1)
  })

  test('a slow answer for an earlier filter does not replace the changes for the current one', async () => {
    settings = { ...DEFAULT_SETTINGS }
    history.mockReset().mockResolvedValue(index)
    let finishOld!: (v: ChangeEvent[]) => void
    events.mockReset().mockImplementationOnce(() => new Promise<ChangeEvent[]>((res) => { finishOld = res }))
    renderPage()
    await waitFor(() => expect(events).toHaveBeenCalledTimes(1)) // the unfiltered request, still out

    events.mockResolvedValueOnce([ev('newer-change')])
    pickKind('Cluster appeared')
    await screen.findByText('newer-change')

    await act(async () => {
      finishOld([ev('older-change')])
    })
    expect(screen.queryByText('older-change')).toBeNull()
    expect(screen.getByText('newer-change')).toBeInTheDocument()
  })
})
