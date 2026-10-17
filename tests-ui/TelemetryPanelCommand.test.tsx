import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'
import { beforeEach, describe, expect, test, vi } from 'vitest'
import { TelemetryPanel } from '@/components/agents/AgentInsight'
import { DEFAULT_SETTINGS } from '@/lib/history'
import { answerTo, forgetUpgrades, lastUpgrade } from './upgradeServer'

// The command the Telemetry panel shows once something is on. The server writes it (what it writes from a request is pinned by the Go tests
// against tests/install_commands.golden.json): what is held here is what the panel ASKS for, and that it shows what comes back. When the
// chosen destination names a credential Secret the panel says where the credential is read from, and the credential never passes
// through the page.

beforeEach(() => forgetUpgrades())

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
  return { ...actual, api: { ...actual.api, listOperatorDestinations: async () => [], listTelemetryIntents: async () => [], agentUpgradeCommand: async (...a: Parameters<typeof import('./upgradeServer').answer>) => (await import('./upgradeServer')).answer(...a) } }
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
  test('a destination with a credential Secret is asked for with that Secret named, and the panel says where the credential is read from', async () => {
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
    // No Secret named yet: the request names none, and there is no hint about a credential.
    expect(await screen.findByTestId('helm-command')).toHaveTextContent(answerTo(lastUpgrade()))
    expect(lastUpgrade().telemetry?.destination.authSecret).toBe('')
    expect(screen.queryByTestId('tp-credential-hint')).not.toBeInTheDocument()

    await user.click(screen.getByTestId('tp-guided-back'))
    await user.click(screen.getByTestId('tp-guided-back'))
    await user.type(screen.getByTestId('tp-export-auth-secret'), 'honeycomb-token')
    await toRun(user)
    await waitFor(() => expect(screen.getByTestId('helm-command')).toHaveTextContent('"authSecret":"honeycomb-token"'))
    expect(lastUpgrade().telemetry?.destination.authSecret).toBe('honeycomb-token')
    expect(screen.getByTestId('tp-credential-hint')).toHaveTextContent('TELEMETRY_EXPORT_TOKEN')
  })
})

describe('TelemetryPanel command: what every install is stamped with', () => {
  test('the organisation and cluster, the (off) debug exporter and the (empty) tags are asked for with any destination', async () => {
    const user = userEvent.setup()
    render(
      <MemoryRouter>
        <TelemetryPanel standalone testIdPrefix="tp" clusterId="cl-9" />
      </MemoryRouter>,
    )
    await pickHoneycomb(user)
    await toRun(user)
    await screen.findByTestId('helm-command')
    const t = lastUpgrade().telemetry
    expect(t?.owner).toEqual({ orgId: 'org-1', clusterId: 'cl-9' })
    expect(t?.processing.debug).toBe('')
    expect(t?.processing.tags).toEqual([])
    // A new configuration states every group itself: none is left "as installed".
    expect(t?.kept).toEqual([])
  })
})

describe('TelemetryPanel: where the agent is installed', () => {
  test('the command is asked for at the agent\'s own release and namespace, not the defaults, and the install info is read again on open', async () => {
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
    await screen.findByTestId('helm-command')
    expect(lastUpgrade()).toMatchObject({ namespace: 'observability', release: 'agent-eu' })
  })

  test('an agent that has not reported its release is asked for without one, which the server answers with the chart\'s documented defaults', async () => {
    const user = userEvent.setup()
    render(
      <MemoryRouter>
        <TelemetryPanel standalone testIdPrefix="tp" target={{ namespace: undefined, release: undefined }} />
      </MemoryRouter>,
    )
    await pickHoneycomb(user)
    await toRun(user)
    await screen.findByTestId('helm-command')
    expect(lastUpgrade().namespace).toBeUndefined()
    expect(lastUpgrade().release).toBeUndefined()
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
