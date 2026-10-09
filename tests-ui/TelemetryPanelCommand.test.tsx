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
// The store's conn is one stable function; a fresh one per render would make every polled list read again on every render.
const connFn = () => CONN
const reloadInfo = vi.fn(async () => undefined)
vi.mock('@/store/server', () => ({
  useServer: (selector?: (s: { role?: string; conn: () => typeof CONN; reloadInfo: () => Promise<void> }) => unknown) => (selector ? selector({ role: 'editor', conn: connFn, reloadInfo }) : { role: 'editor', conn: connFn, reloadInfo }),
  useConn: () => CONN,
}))
vi.mock('@/lib/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/lib/api')>()
  // An editor reads the destinations as a read model; here there are none, and nothing reaches a server.
  return { ...actual, api: { ...actual.api, listOperatorDestinations: async () => [], listTelemetryIntents: async () => [] } }
})

async function pickHoneycomb(user: ReturnType<typeof userEvent.setup>) {
  await user.click(screen.getByTestId('tp-resourceUsage'))
  await user.click(screen.getByTestId('tp-guided-continue')) // What to collect -> Where to send
  await user.click(screen.getByTestId('tp-guided-destination-external-preset-honeycomb'))
}

/** Continue from Destination to Review, then "Create the command": the last step. */
async function toRun(user: ReturnType<typeof userEvent.setup>) {
  await user.click(screen.getByTestId('tp-guided-continue'))
  await user.click(screen.getByTestId('tp-guided-create-command'))
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
    // The command is not under the destination step: it comes last, after Review.
    expect(screen.queryByTestId('helm-command')).not.toBeInTheDocument()
    await toRun(user)
    // No Secret named yet: just the upgrade command, and no hint about a credential.
    expect(screen.getByTestId('helm-command').textContent).not.toContain('kubectl')
    expect(screen.queryByTestId('tp-credential-hint')).not.toBeInTheDocument()

    await user.click(screen.getByTestId('tp-guided-back'))
    await user.click(screen.getByTestId('tp-guided-back'))
    await user.type(screen.getByTestId('tp-export-auth-secret'), 'honeycomb-token')
    await toRun(user)
    const cmd = screen.getByTestId('helm-command').textContent ?? ''
    expect(cmd).toContain('kubectl create secret generic honeycomb-token')
    expect(cmd.indexOf('kubectl create secret')).toBeLessThan(cmd.indexOf('helm upgrade'))
    expect(cmd).toContain('&&')
    expect(cmd).toContain('auth.secretName=honeycomb-token')
    expect(screen.getByTestId('tp-credential-hint')).toHaveTextContent('TELEMETRY_EXPORT_TOKEN')
  })
})

describe('TelemetryPanel command: what every install is stamped with', () => {
  test('the organisation and cluster, the (off) debug exporter and the (empty) tags are in the command for any destination', async () => {
    const user = userEvent.setup()
    render(
      <MemoryRouter>
        <TelemetryPanel standalone testIdPrefix="tp" clusterId="cl-9" />
      </MemoryRouter>,
    )
    await pickHoneycomb(user)
    await toRun(user)
    const cmd = screen.getByTestId('helm-command').textContent ?? ''
    expect(cmd).toContain('telemetry.resource.orgId=org-1')
    expect(cmd).toContain('telemetry.resource.clusterId=cl-9')
    expect(cmd).toMatch(/--set-string telemetry\.debug\.verbosity=( |$)/)
    expect(cmd).toContain("telemetry.resource.attributes='[]'")
  })
})

describe('TelemetryPanel: an unfinished draft survives leaving the page', () => {
  const panel = (agentId = 'agent-1') => (
    <MemoryRouter>
      <TelemetryPanel standalone testIdPrefix="tp" agentId={agentId} clusterId="cl-1" />
    </MemoryRouter>
  )

  test('what was chosen comes back on the same agent with a way to start over, and not on another agent or when nothing was chosen', async () => {
    const user = userEvent.setup()
    const first = render(panel())
    expect(screen.queryByTestId('tp-restored')).not.toBeInTheDocument()
      await user.click(screen.getByTestId('tp-resourceUsage'))
    first.unmount()

    const again = render(panel())
    expect(screen.getByTestId('tp-restored')).toHaveTextContent('unfinished changes')
    again.unmount()

    render(panel('agent-2'))
    expect(screen.queryByTestId('tp-restored')).not.toBeInTheDocument()
  })

  test('"Start over" drops it, and the next visit starts clean', async () => {
    const user = userEvent.setup()
    const first = render(panel())
      await user.click(screen.getByTestId('tp-resourceUsage'))
    first.unmount()
    const again = render(panel())
    await user.click(screen.getByTestId('tp-start-over'))
    expect(screen.queryByTestId('tp-restored')).not.toBeInTheDocument()
    again.unmount()
    render(panel())
    expect(screen.queryByTestId('tp-restored')).not.toBeInTheDocument()
  })
})

describe('TelemetryPanel: where the agent is installed', () => {
  test('the command targets the agent\'s own release and namespace, not the defaults, and the install info is read again on open', async () => {
    reloadInfo.mockClear()
    const user = userEvent.setup()
    render(
      <MemoryRouter>
        <TelemetryPanel standalone testIdPrefix="tp" target={{ namespace: 'observability', release: 'agent-eu' }} />
      </MemoryRouter>,
    )
    expect(reloadInfo).toHaveBeenCalledTimes(1)
    await pickHoneycomb(user)
    await toRun(user)
    await user.click(screen.getByTestId('tp-guided-back'))
    await user.click(screen.getByTestId('tp-guided-back'))
    await user.type(screen.getByTestId('tp-export-auth-secret'), 'honeycomb-token')
    await toRun(user)
    const cmd = screen.getByTestId('helm-command').textContent ?? ''
    expect(cmd).toContain('helm upgrade agent-eu ')
    expect(cmd).toContain('--namespace observability --reset-then-reuse-values')
    expect(cmd).toContain('kubectl create secret generic honeycomb-token --namespace observability')
    expect(cmd).not.toContain('helm upgrade continuum-agent ')
    expect(cmd).not.toContain('continuum-system')
  })

  test('an agent that has not reported its release gets the chart\'s documented defaults', async () => {
    const user = userEvent.setup()
    render(
      <MemoryRouter>
        <TelemetryPanel standalone testIdPrefix="tp" target={{ namespace: undefined, release: undefined }} />
      </MemoryRouter>,
    )
    await pickHoneycomb(user)
    await toRun(user)
    expect(screen.getByTestId('helm-command').textContent).toContain('helm upgrade continuum-agent ')
  })
})
