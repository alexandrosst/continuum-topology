import { render, screen } from '@testing-library/react'
import { afterEach, describe, expect, test, vi } from 'vitest'
import ErrorBoundary from '@/components/ErrorBoundary'

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
})
