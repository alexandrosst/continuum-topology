import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { beforeEach, describe, expect, test, vi } from 'vitest'
import { ApiError, type FusionApplications, type FusionRetention, type FusionServices, type FusionStatus } from '@/lib/api'
import FusionPage from '@/pages/FusionPage'

let admin = true
let status: FusionStatus
let retention: FusionRetention | Error
const getFusion = vi.fn(async () => status)
const enableFusion = vi.fn(async () => (status = { ...status, state: 'starting' }))
const disableFusion = vi.fn(async () => (status = { available: true, state: 'off', data: true }))
const getFusionRetention = vi.fn(async () => { if (retention instanceof Error) throw retention; return retention })
const listFusionTokens = vi.fn(async () => [])
const getFusionApplications = vi.fn()
const getFusionServices = vi.fn()
const openFusionPage = vi.fn(async (_c: unknown, page: string) => ({ path: `/fusion/${page}/?ikhnos_ticket=t1` }))

const connFn = () => ({ url: '', org: 'o' })
vi.mock('@/store/server', () => ({
  useServer: (sel?: (s: { conn: typeof connFn; isAdmin: () => boolean; orgId: string }) => unknown) => {
    const state = { conn: connFn, isAdmin: () => admin, orgId: 'o' }
    return sel ? sel(state) : state
  },
}))
vi.mock('@/lib/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/lib/api')>()
  return {
    ...actual,
    api: {
      ...actual.api,
      getFusion: () => getFusion(),
      enableFusion: () => enableFusion(),
      disableFusion: () => disableFusion(),
      getFusionRetention: () => getFusionRetention(),
      listFusionTokens: () => listFusionTokens(),
      getFusionApplications: () => getFusionApplications(),
      getFusionServices: () => getFusionServices(),
      openFusionPage: (...a: Parameters<typeof openFusionPage>) => openFusionPage(...a),
    },
  }
})

const GIB = 2 ** 30
const part = (component: 'metrics' | 'logs' | 'traces' | 'central', over: object = {}) =>
  ({ component, label: { metrics: 'Prometheus', logs: 'Loki', traces: 'Tempo', central: 'Central operator' }[component], desired: 1, ready: 1, ...over })
const parts = () => [part('metrics'), part('logs'), part('traces'), part('central')]
const store = (component: 'metrics' | 'logs' | 'traces', over: object = {}) => ({
  component, label: { metrics: 'Prometheus', logs: 'Loki', traces: 'Tempo' }[component], value: '7d', days: 7, exactDays: true, minDays: 1, maxDays: 365,
  volumeKnown: true, volumeBytes: 10 * GIB, capacityBytes: 10 * GIB, usedBytes: 1 * GIB, usedSource: 'volume', share: 0.85, ...over,
})
const central = { operatorId: 'op-central', endpoint: 'central.svc:4317', exposed: false, exists: true }
const running = (over: Partial<FusionStatus> = {}): FusionStatus => ({
  available: true, state: 'running', data: true, components: parts(), central, lastDataAt: new Date(Date.now() - 12_000).toISOString(), since: new Date(Date.now() - 3600_000).toISOString(),
  links: { prometheus: '/fusion/prometheus/', grafana: '/fusion/grafana/' }, ...over,
})
const retentionOf = (...stores: object[]): FusionRetention => ({ available: true, running: true, stores } as FusionRetention)

function renderAt(path = '/fusion') {
  return render(<MemoryRouter initialEntries={[path]}><Routes><Route path="/fusion/*" element={<FusionPage />} /></Routes></MemoryRouter>)
}

beforeEach(() => {
  admin = true
  status = running()
  retention = retentionOf(store('metrics'), store('logs'), store('traces'))
  for (const f of [getFusion, enableFusion, disableFusion, getFusionRetention, listFusionTokens, getFusionApplications, getFusionServices, openFusionPage]) f.mockClear()
  getFusionApplications.mockReset()
  getFusionServices.mockReset()
})

describe('FusionPage', () => {
  test('a person who is not an administrator is told so, and nothing is read', () => {
    admin = false
    renderAt()
    expect(screen.getByText('Administrators only')).toBeInTheDocument()
    expect(getFusion).not.toHaveBeenCalled()
  })

  test('has four sections as links, each its own address, with the open one marked', async () => {
    renderAt('/fusion/access')
    const nav = await screen.findByRole('navigation', { name: 'FUSION sections' })
    expect(within(nav).getAllByRole('link').map((l) => [l.textContent?.trim(), l.getAttribute('href')])).toEqual([
      ['Overview', '/fusion'], ['Data', '/fusion/data'], ['Access', '/fusion/access'], ['Settings', '/fusion/settings'],
    ])
    expect(within(nav).getByRole('link', { name: 'Access' })).toHaveAttribute('aria-current', 'page')
    expect(await screen.findByTestId('fusion-access')).toBeInTheDocument()
  })

  test('the status beside the tabs is the word, and when data last arrived; the live region carries only the word', async () => {
    renderAt()
    const state = await screen.findByTestId('fusion-state')
    expect(state).toHaveTextContent('Healthy')
    expect(state).toHaveTextContent(/Last data \d+ s ago/)
    expect(screen.getByTestId('fusion-live')).toHaveTextContent(/^FUSION: Healthy$/)
  })

  test('the Overview says nothing needs attention, and shows each part with the same words', async () => {
    renderAt()
    expect(await screen.findByTestId('fusion-fine')).toBeInTheDocument()
    const list = screen.getByTestId('fusion-parts')
    expect(within(list).getByTestId('fusion-part-metrics')).toHaveTextContent('Healthy')
    expect(within(list).getByTestId('fusion-part-central')).toHaveTextContent('Healthy')
    expect(screen.getByTestId('fusion-exposure')).toHaveTextContent('inside this cluster only')
  })

  test('a part that is down is a problem with its cause and one action, and its row says Not working', async () => {
    status = running({ state: 'attention', message: 'Loki is not ready.', central: { ...central, namespace: 'ikhnos-fusion' } as never, components: [part('metrics'), part('logs', { ready: 0, reason: 'Waiting for a volume' }), part('traces'), part('central')] })
    renderAt()
    const problem = await screen.findByTestId('fusion-problem-down-logs')
    expect(problem).toHaveAttribute('data-health', 'broken')
    expect(problem).toHaveTextContent('Loki is not running')
    expect(problem).toHaveTextContent('Waiting for a volume.')
    expect(within(problem).getByRole('button', { name: 'Copy the kubectl command' })).toBeInTheDocument()
    expect(screen.getByTestId('fusion-part-logs')).toHaveTextContent('Not working')
    expect(screen.getByTestId('fusion-state')).toHaveTextContent('Not working')
  })

  test('a volume that is nearly full is told on the Overview, with a link to the retention', async () => {
    retention = retentionOf(store('metrics', { usedBytes: 9.2 * GIB }), store('logs'), store('traces'))
    renderAt()
    const problem = await screen.findByTestId('fusion-problem-disk-metrics')
    expect(problem).toHaveTextContent("Prometheus's volume is 92% full")
    expect(within(problem).getByRole('link', { name: 'Change retention' })).toHaveAttribute('href', '/fusion/settings')
    expect(screen.getByTestId('fusion-state')).toHaveTextContent('Needs attention')
  })

  test('each store row on the Overview has its disk as a bar with the figure in words, and says when it will fill only when the figures support it', async () => {
    retention = retentionOf(store('metrics'), store('logs', { days: 30, usedBytes: 9.2 * GIB, bytesPerDay: GIB, usedSource: 'volume' }), store('traces'))
    renderAt()
    const parts = await screen.findByTestId('fusion-parts')
    expect(within(parts).getByRole('meter', { name: 'Prometheus disk use' })).toHaveAttribute('aria-valuetext', '1 GiB of 10 GiB used')
    expect(within(parts).getByRole('meter', { name: 'Loki disk use' })).toHaveAttribute('aria-valuenow', '92')
    expect(screen.getByTestId('fusion-fills-logs')).toHaveTextContent('fills in about 1 day')
    expect(screen.queryByTestId('fusion-fills-metrics')).not.toBeInTheDocument()
  })

  test('no data for a while points at the pipeline', async () => {
    status = running({ lastDataAt: new Date(Date.now() - 30 * 60_000).toISOString() })
    renderAt()
    const problem = await screen.findByTestId('fusion-problem-no-data')
    expect(problem).toHaveTextContent('No new data')
    expect(within(problem).getByRole('link', { name: 'Open Pipeline' })).toHaveAttribute('href', '/pipeline')
  })

  test('while FUSION starts there is one spinner, and the parts that are coming up read Starting', async () => {
    status = running({ state: 'starting', lastDataAt: undefined, components: [part('metrics'), part('logs', { ready: 0 }), part('traces'), part('central', { ready: 0 })] })
    renderAt()
    expect(await screen.findByTestId('fusion-waiting')).toBeInTheDocument()
    expect(screen.getAllByRole('status')).toHaveLength(1)
    expect(screen.getByTestId('fusion-part-logs')).toHaveTextContent('Starting')
    expect(screen.getByTestId('fusion-part-metrics')).toHaveTextContent('Healthy')
  })

  test('an exposed central operator says where other clusters reach it', async () => {
    status = running({ central: { ...central, exposed: true, endpoint: 'fusion.example.com:4317' } })
    renderAt()
    expect(await screen.findByTestId('fusion-exposure')).toHaveTextContent('reachable from other clusters at fusion.example.com:4317')
  })

  test('FUSION off is one message with one way forward, on every section that needs it running', async () => {
    status = { available: true, state: 'off', data: true }
    const user = userEvent.setup()
    renderAt()
    expect(await screen.findByText('FUSION is off')).toBeInTheDocument()
    expect(screen.queryByTestId('fusion-parts')).not.toBeInTheDocument()
    await user.click(screen.getByTestId('fusion-enable'))
    await waitFor(() => expect(enableFusion).toHaveBeenCalledTimes(1))
    expect(await screen.findByTestId('fusion-waiting')).toBeInTheDocument()
  })

  test('a server that cannot run FUSION says why', async () => {
    status = { available: false, state: 'off', reason: 'not-installed', message: "FUSION's workloads are not in this release." }
    renderAt()
    expect(await screen.findByText('FUSION is not available here')).toBeInTheDocument()
    expect(screen.getByText("FUSION's workloads are not in this release.")).toBeInTheDocument()
  })

  test('an unknown address goes back to the Overview', async () => {
    renderAt('/fusion/nothing-here')
    expect(await screen.findByTestId('fusion-fine')).toBeInTheDocument()
  })
})

describe('FusionPage - opening Grafana and Prometheus', () => {
  const tab = () => ({ location: { href: '' }, close: vi.fn(), opener: {} as unknown })

  test('a page that is not up yet is shown, disabled, and the tab opens once it is', async () => {
    status = running({ links: { prometheus: '/fusion/prometheus/' } })
    const t = tab()
    const openSpy = vi.spyOn(window, 'open').mockReturnValue(t as unknown as Window)
    const user = userEvent.setup()
    renderAt()
    const grafana = await screen.findByTestId('fusion-open-grafana')
    expect(grafana).toHaveAttribute('aria-disabled', 'true')
    await user.click(screen.getByTestId('fusion-open-prometheus'))
    expect(openSpy).toHaveBeenCalledWith('', '_blank')
    await waitFor(() => expect(t.location.href).toBe('/fusion/prometheus/?ikhnos_ticket=t1'))
    expect(t.opener).toBeNull()
    openSpy.mockRestore()
  })

  test('the server\'s refusal is shown and the empty tab closed; a blocked tab is said so', async () => {
    const t = tab()
    const openSpy = vi.spyOn(window, 'open').mockReturnValue(t as unknown as Window)
    openFusionPage.mockRejectedValueOnce(new ApiError(409, 'Grafana is not running yet'))
    const user = userEvent.setup()
    renderAt()
    await user.click(await screen.findByTestId('fusion-open-grafana'))
    expect(await screen.findByTestId('fusion-open-error')).toHaveTextContent('Grafana is not running yet')
    expect(t.close).toHaveBeenCalled()
    openSpy.mockReturnValue(null)
    await user.click(screen.getByTestId('fusion-open-prometheus'))
    expect(await screen.findByTestId('fusion-open-error')).toHaveTextContent('blocked the new tab')
    expect(openFusionPage).toHaveBeenCalledTimes(1)
    openSpy.mockRestore()
  })

  test('a double click mints one ticket and opens one tab', async () => {
    let finish: (r: { path: string }) => void = () => undefined
    openFusionPage.mockImplementationOnce(() => new Promise<{ path: string }>((r) => { finish = r }))
    const t = tab()
    const openSpy = vi.spyOn(window, 'open').mockReturnValue(t as unknown as Window)
    const user = userEvent.setup()
    renderAt()
    await user.dblClick(await screen.findByTestId('fusion-open-grafana'))
    expect(openSpy).toHaveBeenCalledTimes(1)
    expect(openFusionPage).toHaveBeenCalledTimes(1)
    expect(screen.getByTestId('fusion-open-prometheus')).toBeDisabled()
    finish({ path: '/fusion/grafana/?ikhnos_ticket=t1' })
    await waitFor(() => expect(t.location.href).toBe('/fusion/grafana/?ikhnos_ticket=t1'))
    await waitFor(() => expect(screen.getByTestId('fusion-open-grafana')).toBeEnabled())
    openSpy.mockRestore()
  })
})

describe('FusionPage - Data', () => {
  const sources = { prometheus: true, loki: true, tempo: true }
  const apps = (over: Partial<FusionApplications> = {}): FusionApplications => ({
    applications: [{ id: 'a1', name: 'shop', services: [{ name: 'cart' }, { name: 'pay' }], signals: ['metrics', 'traces'], namespaces: ['shop'], clusters: ['edge-1'], unresolvedMembers: 1 }],
    sources, warnings: [], ...over,
  } as FusionApplications)
  const services = (over: Partial<FusionServices> = {}): FusionServices => ({ services: [{ name: 'cart', signals: ['metrics'], applications: ['shop'] }, { name: 'node-exporter', signals: ['metrics'], applications: [] }], sources, warnings: [], ...over } as FusionServices)

  test('lists each application with its services and the signals that arrived, and the services in none', async () => {
    getFusionApplications.mockResolvedValue(apps())
    getFusionServices.mockResolvedValue(services())
    renderAt('/fusion/data')
    const row = await screen.findByTestId('fusion-app-shop')
    expect(row).toHaveTextContent('1 not running')
    expect(row).toHaveTextContent('edge-1')
    expect(row).toHaveTextContent('Metrics')
    expect(row).toHaveTextContent('Traces')
    expect(within(screen.getByTestId('fusion-services')).getByText('node-exporter')).toBeInTheDocument()
    expect(within(screen.getByTestId('fusion-services')).queryByText('cart')).not.toBeInTheDocument()
  })

  test('an application with no data yet says so, and a store that could not be read is a warning', async () => {
    getFusionApplications.mockResolvedValue(apps({ applications: [{ id: 'a1', name: 'shop', services: [], signals: [], namespaces: [], clusters: [] }] as never, warnings: ['Loki could not be read.'] }))
    getFusionServices.mockResolvedValue(services({ services: [], warnings: ['Loki could not be read.'] }))
    renderAt('/fusion/data')
    expect(await screen.findByTestId('fusion-app-shop')).toHaveTextContent('No data yet')
    expect(screen.getAllByText('Loki could not be read.')).toHaveLength(1)
    expect(screen.queryByTestId('fusion-services')).not.toBeInTheDocument()
  })

  test('no declared application is an empty table with the reason, and a failed read shows the server\'s message', async () => {
    getFusionApplications.mockResolvedValue(apps({ applications: [] }))
    getFusionServices.mockResolvedValue(services({ services: [] }))
    const { unmount } = renderAt('/fusion/data')
    expect(await screen.findByText('No application has been declared in Ikhnos yet.')).toBeInTheDocument()
    unmount()
    getFusionApplications.mockRejectedValue(new ApiError(502, 'The stores did not answer.'))
    getFusionServices.mockRejectedValue(new ApiError(502, 'The stores did not answer.'))
    renderAt('/fusion/data')
    expect(await screen.findByText('The stores did not answer.')).toBeInTheDocument()
  })
})

describe('FusionPage - Settings', () => {
  test('shows the retention and the switch; turning FUSION off asks first and says what happens', async () => {
    const user = userEvent.setup()
    renderAt('/fusion/settings')
    expect(await screen.findByTestId('fusion-retention')).toBeInTheDocument()
    await user.click(screen.getByTestId('fusion-disable'))
    const dialog = screen.getByRole('dialog', { name: 'Turn FUSION off?' })
    expect(dialog).toHaveTextContent('stays on their volumes')
    expect(disableFusion).not.toHaveBeenCalled()
    await user.click(within(dialog).getByRole('button', { name: 'Turn off' }))
    await waitFor(() => expect(disableFusion).toHaveBeenCalled())
    expect(await screen.findByTestId('fusion-enable')).toBeInTheDocument()
  })

  test('on a server whose FUSION belongs to another organisation there are no tokens and no retention', async () => {
    status = { available: true, state: 'running', data: false, components: parts(), message: 'FUSION is managed from the main organisation.' }
    retention = { available: false, reason: 'other-org', message: 'x', running: true, stores: [] } as FusionRetention
    renderAt('/fusion/access')
    expect(await screen.findByText('API tokens are managed elsewhere')).toBeInTheDocument()
    expect(listFusionTokens).not.toHaveBeenCalled()
  })
})
