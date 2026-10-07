import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, test, vi } from 'vitest'
import ErrorBoundary from '@/components/ErrorBoundary'
import { holdReload } from '@/lib/staleBuild'

const reloadSpy = vi.fn()
vi.mock('@/lib/staleBuild', async (orig) => ({ ...(await orig<typeof import('@/lib/staleBuild')>()), reloadForNewVersion: () => reloadSpy() }))

function Broken({ message }: { message: string }): never {
  throw new TypeError(message)
}

describe('ErrorBoundary and a new deploy', () => {
  afterEach(() => reloadSpy.mockReset())

  test('a page file that cannot be fetched loads the new version, saying so', () => {
    reloadSpy.mockReturnValue(true)
    vi.spyOn(console, 'error').mockImplementation(() => undefined)
    render(<ErrorBoundary><Broken message="Failed to fetch dynamically imported module: https://x/assets/Wizard-1.js" /></ErrorBoundary>)
    expect(screen.getByTestId('updating')).toHaveTextContent('Ikhnos was updated')
  })

  test('when a reload was just done, it says what happened and what to do instead of looping', () => {
    reloadSpy.mockReturnValue(false)
    vi.spyOn(console, 'error').mockImplementation(() => undefined)
    render(<ErrorBoundary><Broken message="Failed to fetch dynamically imported module: https://x/assets/Wizard-1.js" /></ErrorBoundary>)
    expect(screen.queryByTestId('updating')).not.toBeInTheDocument()
    expect(screen.getByRole('alert')).toHaveTextContent('Reload the page')
  })

  test('any other error keeps its own message and does not reload', () => {
    vi.spyOn(console, 'error').mockImplementation(() => undefined)
    render(<ErrorBoundary><Broken message="boom" /></ErrorBoundary>)
    expect(screen.getByRole('alert')).toHaveTextContent('boom')
    expect(reloadSpy).not.toHaveBeenCalled()
  })

  test('while a dialog holds the reload (a secret shown once), it does not reload: it says why and offers Reload now', async () => {
    // The real reloadForNewVersion refuses while held; the mock here stands for that.
    reloadSpy.mockReturnValue(false)
    vi.spyOn(console, 'error').mockImplementation(() => undefined)
    const release = holdReload()
    const reload = vi.fn()
    const location = window.location
    Object.defineProperty(window, 'location', { value: { ...location, reload }, configurable: true })
    try {
      render(<ErrorBoundary><Broken message="Failed to fetch dynamically imported module: https://x/assets/Wizard-1.js" /></ErrorBoundary>)
      const notice = screen.getByTestId('updating')
      expect(notice).toHaveTextContent('has not reloaded by itself')
      expect(reload).not.toHaveBeenCalled()
      await userEvent.setup().click(screen.getByRole('button', { name: 'Reload now' }))
      expect(reload).toHaveBeenCalledTimes(1)
    } finally {
      Object.defineProperty(window, 'location', { value: location, configurable: true })
      release()
    }
  })
})
