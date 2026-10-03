import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, test, vi } from 'vitest'
import QuickStartBackends from '@/components/telemetry/QuickStartBackends'
import { DEFAULT_SETTINGS, type AppSettings, type QuickStartBackend } from '@/lib/history'

// QuickStartBackends is a small, store-connected disclosure that sits under the telemetry destination
// field: offers a ready-to-run `helm install` for Jaeger/Prometheus when a matching modality is on,
// remembers one was set up (in Settings.quickStartBackends), and surfaces "Open <tool>"/"Use as
// destination" once it is. It never deploys or dials anything itself - every assertion below is about what
// renders and what gets saved, never about anything actually reaching a cluster.

let settings: AppSettings
let save: ReturnType<typeof vi.fn>
let role: 'viewer' | 'editor' | 'admin' | 'owner' | undefined

vi.mock('@/store/settings', () => ({
  useSettings: () => ({ settings, loaded: true, error: undefined, save }),
}))
vi.mock('@/store/server', () => ({
  useServer: (selector?: (s: { role?: string }) => unknown) => (selector ? selector({ role }) : { role }),
  useConn: () => ({ url: 'https://example.test', org: 'org-1' }),
}))

describe('QuickStartBackends', () => {
  test('renders nothing when no enabled modality has a matching quick-start backend', () => {
    render(<QuickStartBackends enabledModalities={new Set(['logs'])} onUseAsDestination={vi.fn()} />)
    expect(screen.queryByTestId('quickstart-toggle-jaeger')).not.toBeInTheDocument()
  })

  test('offers Jaeger when traces is on, and expanding it shows a command with the default namespace/retention', async () => {
    const user = userEvent.setup()
    settings = DEFAULT_SETTINGS
    save = vi.fn().mockResolvedValue(true)
    role = 'admin'
    render(<QuickStartBackends enabledModalities={new Set(['traces'])} onUseAsDestination={vi.fn()} />)
    expect(screen.getByTestId('quickstart-toggle-jaeger')).toBeInTheDocument()
    expect(screen.queryByTestId('quickstart-toggle-prometheus')).not.toBeInTheDocument()
    await user.click(screen.getByTestId('quickstart-toggle-jaeger'))
    expect(screen.getByTestId('quickstart-namespace-jaeger')).toHaveValue('observability')
    expect(screen.getByTestId('quickstart-retention-jaeger')).toHaveValue('72h')
    expect(screen.getByText(/jaegertracing\/jaeger/)).toBeInTheDocument()
  })

  test('a non-administrator sees the setup explanation but no form to act on it', async () => {
    const user = userEvent.setup()
    settings = DEFAULT_SETTINGS
    save = vi.fn()
    role = 'viewer'
    render(<QuickStartBackends enabledModalities={new Set(['traces'])} onUseAsDestination={vi.fn()} />)
    await user.click(screen.getByTestId('quickstart-toggle-jaeger'))
    expect(screen.getByText('Only administrators can set this up.')).toBeInTheDocument()
    expect(screen.queryByTestId('quickstart-save-jaeger')).not.toBeInTheDocument()
  })

  test("'I've installed it' saves a new quick-start backend record alongside any existing ones", async () => {
    const user = userEvent.setup()
    settings = DEFAULT_SETTINGS
    save = vi.fn().mockResolvedValue(true)
    role = 'admin'
    render(<QuickStartBackends enabledModalities={new Set(['traces'])} onUseAsDestination={vi.fn()} />)
    await user.click(screen.getByTestId('quickstart-toggle-jaeger'))
    await user.click(screen.getByTestId('quickstart-save-jaeger'))
    expect(save).toHaveBeenCalledTimes(1)
    const [, body] = save.mock.calls[0]
    expect(body.quickStartBackends).toHaveLength(1)
    expect(body.quickStartBackends[0]).toMatchObject({ kind: 'jaeger', modality: 'traces', namespace: 'observability', retention: '72h' })
  })

  test('an already-saved backend shows a summary with "Use as destination" instead of the setup form', async () => {
    const user = userEvent.setup()
    const saved: QuickStartBackend = { id: 'qsb-1', kind: 'jaeger', modality: 'traces', namespace: 'obs', retention: '48h', label: 'Jaeger (traces)' }
    settings = { ...DEFAULT_SETTINGS, quickStartBackends: [saved] }
    save = vi.fn().mockResolvedValue(true)
    role = 'admin'
    const onUseAsDestination = vi.fn()
    render(<QuickStartBackends enabledModalities={new Set(['traces'])} onUseAsDestination={onUseAsDestination} />)
    expect(screen.queryByTestId('quickstart-toggle-jaeger')).not.toBeInTheDocument()
    await user.click(screen.getByText('Use as destination'))
    expect(onUseAsDestination).toHaveBeenCalledWith('jaeger-quickstart-collector.obs.svc:4317', 'grpc')
  })

  test('once a tool URL is known, "Open" replaces the URL form, linking straight to it', () => {
    const saved: QuickStartBackend = { id: 'qsb-1', kind: 'jaeger', modality: 'traces', namespace: 'obs', retention: '48h', label: 'Jaeger (traces)', toolUrl: 'http://localhost:16686' }
    settings = { ...DEFAULT_SETTINGS, quickStartBackends: [saved] }
    save = vi.fn()
    role = 'admin'
    render(<QuickStartBackends enabledModalities={new Set(['traces'])} onUseAsDestination={vi.fn()} />)
    const link = screen.getByText(/Open Jaeger/)
    expect(link.closest('a')).toHaveAttribute('href', 'http://localhost:16686')
  })
})
