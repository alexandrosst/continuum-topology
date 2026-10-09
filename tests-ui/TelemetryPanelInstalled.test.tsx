import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'
import { describe, expect, test, vi } from 'vitest'
import { TelemetryPanel } from '@/components/agents/AgentInsight'
import type { AgentDiagnostics } from '@/lib/consent'
import { DEFAULT_SETTINGS } from '@/lib/history'

// The panel of an install that already has telemetry: what it shows has to be what is installed, and the command has to say what removing
// something means (helm upgrade --reset-then-reuse-values keeps everything a command does not name).

vi.mock('@/store/settings', () => ({
  useSettings: () => ({ settings: DEFAULT_SETTINGS, loaded: true, error: undefined, save: vi.fn(async () => true) }),
}))
let CONN = { url: 'https://example.test', org: 'org-1' }
const connFn = () => CONN
const reloadInfo = vi.fn(async () => undefined)
vi.mock('@/store/server', () => ({
  useServer: (selector?: (s: { role?: string; conn: () => typeof CONN; reloadInfo: () => Promise<void> }) => unknown) => (selector ? selector({ role: 'editor', conn: connFn, reloadInfo }) : { role: 'editor', conn: connFn, reloadInfo }),
  useConn: () => CONN,
}))
vi.mock('@/lib/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/lib/api')>()
  return { ...actual, api: { ...actual.api, listOperatorDestinations: async () => [], listTelemetryIntents: async () => [] } }
})

const diag = (over: Partial<AgentDiagnostics> = {}): AgentDiagnostics =>
  ({ reportedAt: new Date().toISOString(), agentVersion: 'x', uptimeSeconds: 1, installedTier: 1, approvedTier: 1, effectiveTier: 1, pausedCollectors: [], excludedNamespaces: 0, collectors: [], informers: [], problems: [], ...over }) as AgentDiagnostics

const KEPLER = diag({
  installedTelemetry: ['energy'],
  installedTelemetryConfig: { exportEndpoint: 'gw.example.com:4317', redactionEnabled: true, resourceDetectionEnabled: false, energySource: 'bundle-kepler' },
})

const tree = (diagnostics?: AgentDiagnostics, agentId = 'a1') => (
  <MemoryRouter>
    <TelemetryPanel standalone testIdPrefix="tp" agentId={agentId} clusterId="cl-1" diagnostics={diagnostics} />
  </MemoryRouter>
)

/** What to collect -> Where to send -> Review -> "Create the command": the last screen, which is the only one with a command on it. */
async function toRun(user: ReturnType<typeof userEvent.setup>) {
  await user.click(screen.getByTestId('tp-guided-continue'))
  await user.click(screen.getByTestId('tp-guided-continue'))
  await user.click(screen.getByTestId('tp-guided-create-command'))
}

describe('TelemetryPanel on an install that has telemetry', () => {
  test('the form starts from what is installed: only Kepler ticked, and nothing else added (no debug exporter, no other signal)', async () => {
    const user = userEvent.setup()
    render(tree(KEPLER))
    expect(screen.getByTestId('tp-energy')).toBeChecked()
    expect(screen.getByTestId('tp-resourceUsage')).not.toBeChecked()
    // The install already sends somewhere, so Where to send opens on that and Review can be reached at once.
    await toRun(user)
    const cmd = screen.getByTestId('helm-command').textContent ?? ''
    // Exactly one signal is on, and the settings this agent does not report (the debug exporter, the tags, the scope) are left as installed.
    expect(cmd.match(/(metrics|logs|traces)\.enabled=true/g)).toHaveLength(1)
    expect(cmd).toContain('--set telemetry.energy.metrics.enabled=true')
    expect(cmd).not.toContain('telemetry.debug.verbosity')
    expect(cmd).not.toContain('telemetry.resource.attributes')
    expect(cmd).not.toContain('telemetry.resource.scope')
    expect(cmd).toMatch(/--set telemetry\.energy\.metrics\.source=bundle-kepler/)
  })

  test('unticking the last signal gives the command that turns telemetry off, not the bare upgrade that changes nothing', async () => {
    const user = userEvent.setup()
    render(tree(KEPLER))
    await user.click(screen.getByTestId('tp-energy'))
    expect(screen.getByTestId('tp-guided-continue')).toHaveTextContent('Review turning everything off')
    await user.click(screen.getByTestId('tp-guided-continue'))
    expect(screen.getByTestId('tp-review-changes')).toHaveTextContent('Turns every telemetry signal off')
    await user.click(screen.getByTestId('tp-guided-create-command'))
    const cmd = screen.getByTestId('helm-command').textContent ?? ''
    expect(cmd).toContain('--set telemetry.energy.metrics.enabled=false')
    expect(cmd).toContain('--set telemetry.resourceUsage.metrics.enabled=false')
    expect(cmd).toMatch(/telemetry\.export\.routes\.metrics\.endpoint=/)
    expect(cmd).not.toContain('telemetry.export.otlp.endpoint')
    // Nothing is left to arrive, so there is no "check that data arrives".
    expect(screen.queryByTestId('tp-guided-check')).not.toBeInTheDocument()
  })

  test('a diagnostics report that arrives after the panel opened is what an untouched draft follows', () => {
    const { rerender } = render(tree(undefined))
    expect(screen.getByTestId('tp-energy')).not.toBeChecked()
    rerender(tree(KEPLER))
    expect(screen.getByTestId('tp-energy')).toBeChecked()
  })

  test('a draft someone has edited is not replaced by a later report', async () => {
    const user = userEvent.setup()
    const { rerender } = render(tree(undefined))
    await user.click(screen.getByTestId('tp-resourceUsage'))
    rerender(tree(KEPLER))
    expect(screen.getByTestId('tp-resourceUsage')).toBeChecked()
  })

  test('another agent, or another organisation, starts from its own install - never from the draft of the last', async () => {
    const user = userEvent.setup()
    const { rerender } = render(tree(KEPLER, 'a1'))
    await user.click(screen.getByTestId('tp-resourceUsage'))
    rerender(tree(diag({ installedTelemetry: ['traces'], installedTelemetryConfig: { exportEndpoint: 'x:4317', redactionEnabled: true, resourceDetectionEnabled: false } }), 'a2'))
    expect(screen.getByTestId('tp-traces')).toBeChecked()
    expect(screen.getByTestId('tp-resourceUsage')).not.toBeChecked()
    expect(screen.getByTestId('tp-energy')).not.toBeChecked()
    CONN = { url: 'https://example.test', org: 'org-2' }
    rerender(tree(diag({ installedTelemetry: ['traces'], installedTelemetryConfig: { exportEndpoint: 'x:4317', redactionEnabled: true, resourceDetectionEnabled: false } }), 'a2'))
    await toRun(user)
    expect(screen.getByTestId('helm-command').textContent).toContain('telemetry.resource.orgId=org-2')
    CONN = { url: 'https://example.test', org: 'org-1' }
  })

  test('what the install does not report is said to be left as installed, not shown as if it were known', async () => {
    const user = userEvent.setup()
    render(tree(diag({ installedTelemetry: ['energy'], installedTelemetryConfig: { exportEndpoint: 'gw.example.com:4317', redactionEnabled: true, resourceDetectionEnabled: false } })))
    await user.click(screen.getByTestId('tp-guided-continue'))
    await user.click(screen.getByTestId('tp-guided-continue'))
    expect(screen.getByTestId('tp-review-unchanged')).toHaveTextContent("Everything else in the agent's release, including")
    expect(screen.getByTestId('tp-review-unchanged')).toHaveTextContent('extra processors')
  })

  test('a draft with nowhere to send cannot reach Review, and says so, so there is never a bare upgrade to copy', async () => {
    const user = userEvent.setup()
    render(tree(diag({ installedTelemetry: ['energy'], installedTelemetryConfig: { exportEndpoint: '', redactionEnabled: true, resourceDetectionEnabled: false } })))
    await user.click(screen.getByTestId('tp-resourceUsage'))
    await user.click(screen.getByTestId('tp-guided-continue'))
    expect(screen.getByTestId('tp-guided-continue')).toBeDisabled()
    expect(screen.getByTestId('tp-guided-why')).toHaveTextContent('Choose where to send this.')
    expect(screen.queryByTestId('helm-command')).not.toBeInTheDocument()
  })
})
