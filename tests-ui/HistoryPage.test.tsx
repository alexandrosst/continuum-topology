import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { describe, expect, test, vi } from 'vitest'
import { RecordingSettings } from '@/pages/HistoryPage'
import { DEFAULT_SETTINGS, type AppSettings } from '@/lib/history'

// RecordingSettings is the "recording and checks" settings form on the History page. This file covers
// just the "Link is quiet after" field's unit picker (flowStaleSeconds) - the point of this whole feature
// is that the backend always stores/validates a plain number of seconds (see
// backend/internal/server/settings.go), while the field lets someone type that number in whichever unit
// is convenient (seconds through days) instead of doing the arithmetic themselves.

let settings: AppSettings
let save: ReturnType<typeof vi.fn>

vi.mock('@/store/settings', () => ({
  useSettings: () => ({ settings, loaded: true, error: undefined, save }),
}))

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
