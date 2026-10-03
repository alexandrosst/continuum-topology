import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, test, vi } from 'vitest'
import QuickStartBackends from '@/components/telemetry/QuickStartBackends'
import { DEFAULT_SETTINGS, type AppSettings, type QuickStartBackend } from '@/lib/history'

// QuickStartBackends is a small, store-connected disclosure that sits under the telemetry destination
// field: offers a ready-to-run `helm install` for Jaeger/Prometheus/Loki when a matching modality is on,
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

// api.gatewayTokenStatus/mintGatewayToken (Part B/D): faked here so the "Generate access token"/gated-note
// tests below never touch the network, and so the status-prefetch effect has something deterministic to
// resolve to instead of a real fetch() to a URL that doesn't exist.
const gatewayTokenStatus = vi.fn(async () => ({ active: false }))
const mintGatewayToken = vi.fn(async () => ({ token: 'cnq_shown-once', createdAt: '2026-01-01T00:00:00Z', expiresAt: '2026-01-02T00:00:00Z' }))
vi.mock('@/lib/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/lib/api')>()
  return {
    ...actual,
    api: { ...actual.api, gatewayTokenStatus: (...a: Parameters<typeof gatewayTokenStatus>) => gatewayTokenStatus(...a), mintGatewayToken: (...a: Parameters<typeof mintGatewayToken>) => mintGatewayToken(...a) },
  }
})

beforeEach(() => {
  gatewayTokenStatus.mockClear()
  gatewayTokenStatus.mockResolvedValue({ active: false })
  mintGatewayToken.mockClear()
})

describe('QuickStartBackends', () => {
  test('renders nothing when no modality is on at all (every modality now has a matching backend)', () => {
    render(<QuickStartBackends enabledModalities={new Set()} onUseAsDestination={vi.fn()} />)
    expect(screen.queryByTestId('quickstart-toggle-jaeger')).not.toBeInTheDocument()
    expect(screen.queryByTestId('quickstart-toggle-prometheus')).not.toBeInTheDocument()
    expect(screen.queryByTestId('quickstart-toggle-loki')).not.toBeInTheDocument()
  })

  test('offers Loki when logs is on, and expanding it shows a command with the default namespace/retention', async () => {
    const user = userEvent.setup()
    settings = DEFAULT_SETTINGS
    save = vi.fn().mockResolvedValue(true)
    role = 'admin'
    render(<QuickStartBackends enabledModalities={new Set(['logs'])} onUseAsDestination={vi.fn()} />)
    expect(screen.getByTestId('quickstart-toggle-loki')).toBeInTheDocument()
    expect(screen.queryByTestId('quickstart-toggle-jaeger')).not.toBeInTheDocument()
    expect(screen.queryByTestId('quickstart-toggle-prometheus')).not.toBeInTheDocument()
    await user.click(screen.getByTestId('quickstart-toggle-loki'))
    expect(screen.getByTestId('quickstart-namespace-loki')).toHaveValue('observability')
    expect(screen.getByTestId('quickstart-retention-loki')).toHaveValue('168h')
    expect(screen.getByText(/grafana\/loki/)).toBeInTheDocument()
  })

  test('a saved Loki backend\'s "Use as destination" fills the OTLP/HTTP endpoint and protocol', async () => {
    const user = userEvent.setup()
    const saved: QuickStartBackend = { id: 'qsb-2', kind: 'loki', modality: 'logs', namespace: 'obs', retention: '168h', label: 'Loki (logs)' }
    settings = { ...DEFAULT_SETTINGS, quickStartBackends: [saved] }
    save = vi.fn()
    role = 'admin'
    const onUseAsDestination = vi.fn()
    render(<QuickStartBackends enabledModalities={new Set(['logs'])} onUseAsDestination={onUseAsDestination} />)
    await user.click(screen.getByText('Use as destination'))
    expect(onUseAsDestination).toHaveBeenCalledWith('loki-quickstart.obs.svc:3100/otlp', 'http')
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
    expect(onUseAsDestination).toHaveBeenCalledWith('jaeger-quickstart.obs.svc:4317', 'grpc')
  })

  test('"Use as destination" is disabled once a modality this backend cannot carry is also on', async () => {
    const saved: QuickStartBackend = { id: 'qsb-1', kind: 'jaeger', modality: 'traces', namespace: 'obs', retention: '48h', label: 'Jaeger (traces)' }
    settings = { ...DEFAULT_SETTINGS, quickStartBackends: [saved] }
    save = vi.fn()
    role = 'admin'
    render(<QuickStartBackends enabledModalities={new Set(['traces', 'metrics'])} onUseAsDestination={vi.fn()} />)
    await waitFor(() => expect(gatewayTokenStatus).toHaveBeenCalled())
    expect(screen.getByText('Use as destination')).toBeDisabled()
  })

  test('an admin can change a saved backend\'s retention', async () => {
    const user = userEvent.setup()
    const saved: QuickStartBackend = { id: 'qsb-1', kind: 'jaeger', modality: 'traces', namespace: 'obs', retention: '48h', label: 'Jaeger (traces)' }
    settings = { ...DEFAULT_SETTINGS, quickStartBackends: [saved] }
    save = vi.fn().mockResolvedValue(true)
    role = 'admin'
    render(<QuickStartBackends enabledModalities={new Set(['traces'])} onUseAsDestination={vi.fn()} />)
    await user.click(screen.getByText('48h'))
    const field = screen.getByLabelText('Jaeger (traces) retention')
    await user.clear(field)
    await user.type(field, '168h')
    await user.click(screen.getByText('Save'))
    expect(save).toHaveBeenCalledTimes(1)
    const [, body] = save.mock.calls[0]
    expect(body.quickStartBackends[0]).toMatchObject({ id: 'qsb-1', retention: '168h' })
  })

  test('an administrator can disable a built-in kind, hiding its quick-start offer', async () => {
    const user = userEvent.setup()
    settings = DEFAULT_SETTINGS
    save = vi.fn().mockResolvedValue(true)
    role = 'admin'
    render(<QuickStartBackends enabledModalities={new Set(['traces'])} onUseAsDestination={vi.fn()} />)
    expect(screen.getByTestId('quickstart-toggle-jaeger')).toBeInTheDocument()
    await user.click(screen.getByTestId('allowed-kind-jaeger'))
    expect(save).toHaveBeenCalledTimes(1)
    const [, body] = save.mock.calls[0]
    expect(body.allowedBackendKinds).toEqual(expect.arrayContaining(['prometheus', 'loki']))
    expect(body.allowedBackendKinds).not.toContain('jaeger')
  })

  test('a non-administrator never sees the allow-list control', () => {
    settings = DEFAULT_SETTINGS
    save = vi.fn()
    role = 'viewer'
    render(<QuickStartBackends enabledModalities={new Set(['traces'])} onUseAsDestination={vi.fn()} />)
    expect(screen.queryByTestId('allowed-kind-jaeger')).not.toBeInTheDocument()
  })

  test('"custom" is off by default, and turning it on reveals the add-custom-backend flow', async () => {
    const user = userEvent.setup()
    settings = DEFAULT_SETTINGS
    save = vi.fn().mockResolvedValue(true)
    role = 'admin'
    render(<QuickStartBackends enabledModalities={new Set(['traces'])} onUseAsDestination={vi.fn()} />)
    expect(screen.queryByTestId('quickstart-toggle-custom')).not.toBeInTheDocument()
    await user.click(screen.getByTestId('allowed-kind-custom'))
    expect(save).toHaveBeenCalledTimes(1)
    const [, body] = save.mock.calls[0]
    expect(body.allowedBackendKinds).toEqual(expect.arrayContaining(['jaeger', 'prometheus', 'loki', 'custom']))
  })

  test('adding a custom backend saves it with kind "custom" and the chosen signal', async () => {
    const user = userEvent.setup()
    settings = { ...DEFAULT_SETTINGS, allowedBackendKinds: ['jaeger', 'prometheus', 'loki', 'custom'] }
    save = vi.fn().mockResolvedValue(true)
    role = 'admin'
    render(<QuickStartBackends enabledModalities={new Set(['traces'])} onUseAsDestination={vi.fn()} />)
    await user.click(screen.getByTestId('quickstart-toggle-custom'))
    await user.type(screen.getByTestId('quickstart-custom-label'), 'Elastic APM')
    await user.type(screen.getByTestId('quickstart-custom-url'), 'https://apm.example.com')
    await user.click(screen.getByTestId('quickstart-custom-save'))
    expect(save).toHaveBeenCalledTimes(1)
    const [, body] = save.mock.calls[0]
    expect(body.quickStartBackends).toHaveLength(1)
    expect(body.quickStartBackends[0]).toMatchObject({ kind: 'custom', modality: 'traces', label: 'Elastic APM', toolUrl: 'https://apm.example.com' })
  })

  test('once a custom backend has no tool URL, a non-administrator still sees it (read-only) if its signal is on', () => {
    const saved: QuickStartBackend = { id: 'qsb-3', kind: 'custom', modality: 'traces', namespace: 'obs', retention: 'n/a', label: 'Elastic APM' }
    settings = { ...DEFAULT_SETTINGS, allowedBackendKinds: ['jaeger', 'prometheus', 'loki', 'custom'], quickStartBackends: [saved] }
    save = vi.fn()
    role = 'viewer'
    render(<QuickStartBackends enabledModalities={new Set(['traces'])} onUseAsDestination={vi.fn()} />)
    expect(screen.getByText('Elastic APM')).toBeInTheDocument()
  })

  test('once a tool URL is known, "Open" replaces the URL form, linking straight to it', async () => {
    const saved: QuickStartBackend = { id: 'qsb-1', kind: 'jaeger', modality: 'traces', namespace: 'obs', retention: '48h', label: 'Jaeger (traces)', toolUrl: 'http://localhost:16686' }
    settings = { ...DEFAULT_SETTINGS, quickStartBackends: [saved] }
    save = vi.fn()
    role = 'admin'
    render(<QuickStartBackends enabledModalities={new Set(['traces'])} onUseAsDestination={vi.fn()} />)
    await waitFor(() => expect(gatewayTokenStatus).toHaveBeenCalled())
    const link = screen.getByText(/Open Jaeger/)
    expect(link.closest('a')).toHaveAttribute('href', 'http://localhost:16686')
  })

  test('an administrator can generate a gateway token for a saved backend, shown once', async () => {
    const user = userEvent.setup()
    const saved: QuickStartBackend = { id: 'qsb-1', kind: 'jaeger', modality: 'traces', namespace: 'obs', retention: '48h', label: 'Jaeger (traces)', toolUrl: 'http://localhost:16686' }
    settings = { ...DEFAULT_SETTINGS, quickStartBackends: [saved] }
    save = vi.fn()
    role = 'admin'
    render(<QuickStartBackends enabledModalities={new Set(['traces'])} onUseAsDestination={vi.fn()} />)
    await user.click(screen.getByTestId('quickstart-gateway-token-jaeger'))
    expect(mintGatewayToken).toHaveBeenCalledWith(expect.anything(), 'qsb-1')
    expect(await screen.findByText(/Jaeger \(traces\) access token created/)).toBeInTheDocument()
    expect(screen.getByText('cnq_shown-once')).toBeInTheDocument()
    // The manifest for this backend's own Service/port is shown alongside the token.
    expect(screen.getByText(/jaeger-quickstart\.obs\.svc\.cluster\.local:16686/)).toBeInTheDocument()
  })

  test('a non-administrator never sees "Generate access token"', async () => {
    const saved: QuickStartBackend = { id: 'qsb-1', kind: 'jaeger', modality: 'traces', namespace: 'obs', retention: '48h', label: 'Jaeger (traces)' }
    settings = { ...DEFAULT_SETTINGS, quickStartBackends: [saved] }
    save = vi.fn()
    role = 'viewer'
    render(<QuickStartBackends enabledModalities={new Set(['traces'])} onUseAsDestination={vi.fn()} />)
    await waitFor(() => expect(gatewayTokenStatus).toHaveBeenCalled())
    expect(screen.queryByTestId('quickstart-gateway-token-jaeger')).not.toBeInTheDocument()
  })

  test('once a token is active, "Open" carries a gated note; a custom backend never offers the action at all', async () => {
    gatewayTokenStatus.mockResolvedValue({ active: true, expired: false })
    const jaeger: QuickStartBackend = { id: 'qsb-1', kind: 'jaeger', modality: 'traces', namespace: 'obs', retention: '48h', label: 'Jaeger (traces)', toolUrl: 'http://localhost:16686' }
    const custom: QuickStartBackend = { id: 'qsb-4', kind: 'custom', modality: 'traces', namespace: 'obs', retention: 'n/a', label: 'Elastic APM', toolUrl: 'https://apm.example.com' }
    settings = { ...DEFAULT_SETTINGS, allowedBackendKinds: ['jaeger', 'prometheus', 'loki', 'custom'], quickStartBackends: [jaeger, custom] }
    save = vi.fn()
    role = 'admin'
    render(<QuickStartBackends enabledModalities={new Set(['traces'])} onUseAsDestination={vi.fn()} />)
    expect(await screen.findByLabelText('Gated')).toBeInTheDocument()
    expect(screen.getByTestId('quickstart-gateway-token-jaeger')).toHaveTextContent('Regenerate access token')
    // "custom" has no gateway manifest (see hasGatewayManifest) - no action to generate one at all.
    expect(screen.queryByTestId('quickstart-gateway-token-custom')).not.toBeInTheDocument()
  })
})
