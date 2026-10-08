import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, test, vi } from 'vitest'
import { FusionRetentionCard } from '@/components/operators/FusionRetention'
import type { FusionRetention, FusionRetentionStore } from '@/lib/api'
import { formatBytes, formChanges, formError, GIB, initialForm, neededGiB, restartedBy, retentionVerdict, usageText } from '@/lib/fusionRetention'

const getRetention = vi.fn()
const setRetention = vi.fn()
const conn = () => ({ url: 'https://ikhnos.example', org: 'o' })
vi.mock('@/store/server', () => ({ useServer: (sel: (s: { conn: typeof conn; orgId: string }) => unknown) => sel({ conn, orgId: 'o' }) }))
vi.mock('@/lib/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/lib/api')>()
  return { ...actual, api: { ...actual.api, getFusionRetention: (...a: unknown[]) => getRetention(...a), setFusionRetention: (...a: unknown[]) => setRetention(...a) } }
})

const store = (over: Partial<FusionRetentionStore> = {}): FusionRetentionStore => ({
  component: 'logs', label: 'Loki', value: '168h', days: 7, exactDays: true, minDays: 1, maxDays: 365, volumeKnown: true,
  volumeBytes: 10 * GIB, capacityBytes: 10 * GIB, storageClass: 'fast', share: 0.87, ...over,
})
const doc = (stores: FusionRetentionStore[], over: Partial<FusionRetention> = {}): FusionRetention => ({ available: true, running: true, stores, ...over })

// Prometheus grows 1 GiB a day, Loki 0.5 GiB, Tempo is not measured.
const stores = () => [
  store({ component: 'metrics', label: 'Prometheus', value: '15d', days: 15, maxDays: 1095, share: 0.85, usedBytes: 4 * GIB, usedSource: 'database', bytesPerDay: GIB, dataDays: 4 }),
  store({ usedBytes: 3 * GIB, usedSource: 'volume', bytesPerDay: GIB / 2, dataDays: 6 }),
  store({ component: 'traces', label: 'Tempo', value: '72h', days: 3 }),
]

beforeEach(() => {
  getRetention.mockReset()
  setRetention.mockReset()
})

describe('the numbers behind the card', () => {
  test('sizes are written in binary units', () => {
    expect(formatBytes(512 * 1024 ** 2)).toBe('512 MiB')
    expect(formatBytes(4.25 * GIB)).toBe('4.3 GiB')
    expect(formatBytes(20 * GIB)).toBe('20 GiB')
    expect(formatBytes(1.5 * 1024 * GIB)).toBe('1.5 TiB')
  })

  test('the room a retention needs is the growth times the days, with the headroom the store works in', () => {
    const [prom, loki, tempo] = stores()
    expect(neededGiB(prom!, 30)).toBe(36) // 30 GiB / 0.85, rounded up
    expect(neededGiB(loki!, 14)).toBe(9) // 7 GiB / 0.87
    expect(neededGiB(tempo!, 10)).toBeNull() // growth not measured
  })

  test('says whether the retention fits the volume, and what to do when it does not', () => {
    const [prom, loki, tempo] = stores()
    expect(retentionVerdict(loki!, 7, 10).tone).toBe('ok')
    const tooMuch = retentionVerdict(prom!, 30, 10)
    expect(tooMuch.tone).toBe('warn')
    expect(tooMuch.suggestGiB).toBe(36)
    expect(tooMuch.text).toMatch(/size limit would drop the oldest data after about 8 days/) // 10 GiB * 0.85 / 1 GiB a day
    expect(retentionVerdict(loki!, 365, 10).text).toMatch(/volume would fill up after about 17 days/)
    expect(retentionVerdict(tempo!, 7, 10).tone).toBe('muted')
  })

  test('use is described, or said not to be measured', () => {
    const [prom, , tempo] = stores()
    expect(usageText(prom!)).toBe('4 GiB used, about 1 GiB a day')
    expect(usageText(tempo!)).toBe('use not measured')
  })

  test('only what differs is sent, and volumes only grow', () => {
    const ss = stores()
    const form = initialForm(ss)
    expect(formChanges(ss, form)).toEqual({})
    form.logs = { days: '14', gib: '20' }
    form.traces = { days: '3', gib: '10' }
    expect(formChanges(ss, form)).toEqual({ logs: { days: 14, volumeGiB: 20 } })
    expect(formError(ss[1]!, { days: '14', gib: '5' })).toMatch(/can be grown but not made smaller/)
    expect(formError(ss[1]!, { days: '0', gib: '10' })).toMatch(/between 1 and 365/)
    expect(formError(ss[1]!, { days: '1.5', gib: '10' })).not.toBe('')
    expect(formError(ss[1]!, { days: '14', gib: '' })).not.toBe('')
  })

  test('a change of retention restarts its store; growing a volume alone restarts nothing but Prometheus (its size limit may move)', () => {
    const ss = stores()
    expect(restartedBy(ss, { logs: { days: 14 }, traces: { volumeGiB: 20 } })).toEqual(['Loki'])
    expect(restartedBy(ss, { metrics: { volumeGiB: 20 } })).toEqual(['Prometheus'])
  })
})

describe('FusionRetentionCard', () => {
  test('shows each store\'s retention, volume and use', async () => {
    getRetention.mockResolvedValue(doc(stores()))
    render(<FusionRetentionCard state="running" />)
    const loki = await screen.findByTestId('fusion-retention-logs')
    expect(within(loki).getByText('keeps 7 days')).toBeInTheDocument()
    expect(loki).toHaveTextContent('10 GiB volume (fast), 3 GiB used, about 512 MiB a day')
    expect(screen.getByTestId('fusion-retention-metrics')).toHaveTextContent('keeps 15 days')
    expect(screen.getByTestId('fusion-retention-traces')).toHaveTextContent('use not measured')
  })

  test('a retention that is not whole days is shown as it is set', async () => {
    getRetention.mockResolvedValue(doc([store({ value: '36h', days: 2, exactDays: false })]))
    render(<FusionRetentionCard state="running" />)
    expect(await screen.findByTestId('fusion-retention-logs')).toHaveTextContent(/keeps 2 days\s*\(36h\)/)
  })

  test('warns when Prometheus\' size limit keeps fewer days than the retention', async () => {
    getRetention.mockResolvedValue(doc([store({ component: 'metrics', label: 'Prometheus', days: 15, sizeLimitDays: 8.4, sizeLimitBytes: 8 * GIB })]))
    render(<FusionRetentionCard state="running" />)
    expect(await screen.findByTestId('fusion-retention-sizecap-metrics')).toHaveTextContent('only about 8 days')
  })

  test('shows a volume that is still being grown', async () => {
    getRetention.mockResolvedValue(doc([store({ volumeBytes: 20 * GIB, resizing: true, resizeNote: 'waiting for the file system' })]))
    render(<FusionRetentionCard state="running" />)
    expect(await screen.findByTestId('fusion-retention-growing-logs')).toHaveTextContent('growing to 20 GiB - waiting for the file system')
  })

  test('explains when the server does not manage FUSION, and says nothing for another organisation', async () => {
    getRetention.mockResolvedValueOnce({ available: false, reason: 'unmanaged', message: 'Set it with Helm values.', running: true, stores: [] })
    const { unmount } = render(<FusionRetentionCard state="running" />)
    expect(await screen.findByTestId('fusion-retention-unavailable')).toHaveTextContent('Set it with Helm values.')
    expect(screen.queryByTestId('fusion-retention-change')).not.toBeInTheDocument()
    unmount()
    getRetention.mockResolvedValueOnce({ available: false, reason: 'other-org', message: 'x', running: false, stores: [] })
    const { container } = render(<FusionRetentionCard state="off" />)
    await waitFor(() => expect(getRetention).toHaveBeenCalledTimes(2))
    expect(container).toBeEmptyDOMElement()
  })

  test('changes the retention and the volume together, with the room check and the restart notice', async () => {
    const user = userEvent.setup()
    getRetention.mockResolvedValue(doc(stores()))
    const after = doc(stores().map((s) => (s.component === 'metrics' ? { ...s, days: 30, value: '30d', volumeBytes: 36 * GIB, resizing: true } : s)))
    setRetention.mockResolvedValue(after)
    render(<FusionRetentionCard state="running" />)
    await user.click(await screen.findByTestId('fusion-retention-change'))
    const save = screen.getByTestId('fusion-retention-save')
    expect(save).toBeDisabled() // nothing changed yet

    const days = screen.getByTestId('fusion-retention-input-days-metrics')
    await user.clear(days)
    await user.type(days, '30')
    // 30 days at 1 GiB a day does not fit 10 GiB: it says so and offers the size that does.
    expect(screen.getByTestId('fusion-retention-verdict-metrics')).toHaveTextContent('drop the oldest data after about 8 days')
    expect(screen.getByTestId('fusion-retention-restart-note')).toHaveTextContent('restarts Prometheus')
    await user.click(screen.getByTestId('fusion-retention-suggest-metrics'))
    expect(screen.getByTestId('fusion-retention-input-gib-metrics')).toHaveValue(36)
    expect(screen.getByTestId('fusion-retention-verdict-metrics')).toHaveTextContent(/about 35.3 GiB is needed; the volume has 36 GiB/)

    await user.click(save)
    expect(setRetention).toHaveBeenCalledWith(expect.anything(), { metrics: { days: 30, volumeGiB: 36 } })
    await waitFor(() => expect(screen.queryByTestId('fusion-retention-save')).not.toBeInTheDocument())
    expect(screen.getByTestId('fusion-retention-metrics')).toHaveTextContent('keeps 30 days')
    expect(screen.getByTestId('fusion-retention-growing-metrics')).toBeInTheDocument()
  })

  test('will not shrink a volume or save a retention out of range', async () => {
    const user = userEvent.setup()
    getRetention.mockResolvedValue(doc(stores()))
    render(<FusionRetentionCard state="running" />)
    await user.click(await screen.findByTestId('fusion-retention-change'))
    const gib = screen.getByTestId('fusion-retention-input-gib-logs')
    await user.clear(gib)
    await user.type(gib, '5')
    expect(screen.getByTestId('fusion-retention-error-logs')).toHaveTextContent('can be grown but not made smaller')
    expect(screen.getByTestId('fusion-retention-save')).toBeDisabled()
    await user.clear(gib)
    await user.type(gib, '10')
    const days = screen.getByTestId('fusion-retention-input-days-logs')
    await user.clear(days)
    await user.type(days, '400')
    expect(screen.getByTestId('fusion-retention-error-logs')).toHaveTextContent('between 1 and 365')
    expect(screen.getByTestId('fusion-retention-save')).toBeDisabled()
  })

  test("the cluster's refusal (a storage class that cannot grow) is shown and the form stays open", async () => {
    const user = userEvent.setup()
    getRetention.mockResolvedValue(doc(stores()))
    const { ApiError } = await import('@/lib/api')
    setRetention.mockRejectedValue(new ApiError(409, "Loki's volume cannot be grown: its storage class (fast) does not allow volume expansion."))
    render(<FusionRetentionCard state="running" />)
    await user.click(await screen.findByTestId('fusion-retention-change'))
    const gib = screen.getByTestId('fusion-retention-input-gib-logs')
    await user.clear(gib)
    await user.type(gib, '20')
    await user.click(screen.getByTestId('fusion-retention-save'))
    expect(await screen.findByTestId('fusion-retention-save-error')).toHaveTextContent('does not allow volume expansion')
    expect(screen.getByTestId('fusion-retention-save')).toBeEnabled()
  })

  test('a store whose volume cannot be read can still have its days changed, but not its size', async () => {
    const user = userEvent.setup()
    getRetention.mockResolvedValue(doc([store({ volumeKnown: false, volumeBytes: 0, capacityBytes: 0 })]))
    setRetention.mockResolvedValue(doc([store({ days: 14 })]))
    render(<FusionRetentionCard state="running" />)
    await user.click(await screen.findByTestId('fusion-retention-change'))
    expect(screen.queryByTestId('fusion-retention-input-gib-logs')).not.toBeInTheDocument()
    const days = screen.getByTestId('fusion-retention-input-days-logs')
    await user.clear(days)
    await user.type(days, '14')
    await user.click(screen.getByTestId('fusion-retention-save'))
    expect(setRetention).toHaveBeenCalledWith(expect.anything(), { logs: { days: 14 } })
  })
})
