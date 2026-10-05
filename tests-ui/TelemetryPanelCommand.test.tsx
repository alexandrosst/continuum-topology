import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'
import { describe, expect, test, vi } from 'vitest'
import { TelemetryPanel } from '@/components/agents/AgentInsight'
import { DEFAULT_SETTINGS } from '@/lib/history'

// The command the Telemetry panel shows once something is on: when the chosen destination names a credential
// Secret, the command that creates that Secret comes first, in the same block, and says where the
// credential is read from - so a person pastes one thing, and the credential never passes through the page.

vi.mock('@/store/settings', () => ({
  useSettings: () => ({ settings: DEFAULT_SETTINGS, loaded: true, error: undefined, save: vi.fn(async () => true) }),
}))
const CONN = { url: 'https://example.test', org: 'org-1' }
vi.mock('@/store/server', () => ({
  useServer: (selector?: (s: { role?: string }) => unknown) => (selector ? selector({ role: 'editor' }) : { role: 'editor' }),
  useConn: () => CONN,
}))

async function pickHoneycomb(user: ReturnType<typeof userEvent.setup>) {
  await user.click(screen.getByTestId('tp-mode-guided'))
  await user.click(screen.getByTestId('tp-guided-layer-infrastructure'))
  await user.click(screen.getByTestId('tp-guided-modality-metrics'))
  await user.click(screen.getByTestId('tp-resourceUsage'))
  await user.click(screen.getByTestId('tp-guided-continue'))
  await user.click(screen.getByTestId('tp-guided-destination-external-preset-honeycomb'))
}

describe('TelemetryPanel command', () => {
  test('a destination with a credential Secret gets one block: create the Secret, then upgrade', async () => {
    const user = userEvent.setup()
    render(
      <MemoryRouter>
        <TelemetryPanel standalone testIdPrefix="tp" />
      </MemoryRouter>,
    )
    await pickHoneycomb(user)
    // No Secret named yet: just the upgrade command, and no hint about a credential.
    expect(screen.getByTestId('helm-command').textContent).not.toContain('kubectl')
    expect(screen.queryByTestId('tp-credential-hint')).not.toBeInTheDocument()

    await user.type(screen.getByTestId('tp-export-auth-secret'), 'honeycomb-token')
    const cmd = screen.getByTestId('helm-command').textContent ?? ''
    expect(cmd).toContain('kubectl create secret generic honeycomb-token')
    expect(cmd.indexOf('kubectl create secret')).toBeLessThan(cmd.indexOf('helm upgrade'))
    expect(cmd).toContain('&&')
    expect(cmd).toContain('auth.secretName=honeycomb-token')
    expect(screen.getByTestId('tp-credential-hint')).toHaveTextContent('TELEMETRY_EXPORT_TOKEN')
  })
})
