import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'
import { beforeEach, describe, expect, test, vi } from 'vitest'
import ConnectClusterWizard from '@/features/discovery/components/ConnectClusterWizard'
import { ApiError } from '@/lib/api'

// The wizard that connects a cluster: what the person chooses (the access level, the per-node collectors, the namespaces the agent may
// report) is sent to the server with the request for the token, and the command it shows is the one the server wrote - the page appends
// nothing to it. What the server writes from those choices is pinned in backend/internal/server/admin_token_install_test.go.

const CONN = { url: '', org: 'org-1' }
const createToken = vi.fn()
const refresh = vi.fn(async () => undefined)
const server = { conn: () => CONN, url: '', refresh, state: undefined, info: { implementedTier: 2, install: { imagesConfigured: true } }, reloadInfo: async () => undefined }
vi.mock('@/store/server', () => ({
  useServer: (selector?: (s: typeof server) => unknown) => (selector ? selector(server) : server),
}))
const topology = { clusters: [], agents: [], nodes: [], services: [], sites: [], decideDerived: vi.fn() }
vi.mock('@/store/topology', () => ({ useRawTopology: (selector: (s: typeof topology) => unknown) => selector(topology) }))
vi.mock('@/lib/places-data', () => ({ usePlaceIndex: () => undefined }))
vi.mock('@/lib/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/lib/api')>()
  return { ...actual, api: { ...actual.api, createToken: (...a: unknown[]) => createToken(...a) } }
})

const NONE = { collectors: { nodeProbe: false, flowObserver: false, measurements: false }, scope: { namespaces: [], exclude: [], selector: '' } }
const SERVED = 'helm install ikhnos-agent oci://r.example/ikhnos-agent \\\n  --namespace ikhnos-system \\\n  --set enrollment.key=PIN.ikh_x'

beforeEach(() => {
  createToken.mockReset()
  createToken.mockResolvedValue({ token: 'ikh_x', install: SERVED, meta: { id: 't1', name: 'cluster-1', tier: 2, createdAt: new Date().toISOString(), expiresAt: new Date(Date.now() + 3_600_000).toISOString() } })
})

const wizard = (props: { nodeProbe?: boolean } = {}) =>
  render(
    <MemoryRouter>
      <ConnectClusterWizard open onClose={() => undefined} {...props} />
    </MemoryRouter>,
  )
const open = async (user: ReturnType<typeof userEvent.setup>) => user.click(screen.getByText('More options'))
const create = (user: ReturnType<typeof userEvent.setup>) => user.click(screen.getByRole('button', { name: 'Create install command' }))

describe('ConnectClusterWizard: what is sent for the command', () => {
  test('with nothing chosen: the recommended access level, no collectors, no scope', async () => {
    const user = userEvent.setup()
    wizard()
    await create(user)
    await waitFor(() => expect(createToken).toHaveBeenCalledTimes(1))
    expect(createToken).toHaveBeenCalledWith(CONN, 'cluster-1', 2, NONE)
  })

  test('shows the command the server wrote, as it wrote it', async () => {
    const user = userEvent.setup()
    wizard()
    await create(user)
    expect(await screen.findByTestId('install-command')).toHaveTextContent('--set enrollment.key=PIN.ikh_x')
    expect(screen.getByTestId('install-command').textContent).toBe(SERVED)
  })

  test('every collector and the scope that were chosen', async () => {
    const user = userEvent.setup()
    wizard()
    await open(user)
    await user.click(screen.getByTestId('node-probe-toggle'))
    await user.click(screen.getByTestId('flow-observer-toggle'))
    await user.click(screen.getByTestId('measurements-toggle'))
    await user.click(screen.getByTestId('scope-toggle'))
    await user.type(screen.getByTestId('scope-include'), 'shop{enter}pay{enter}')
    await user.type(screen.getByTestId('scope-exclude'), 'pay-test{enter}')
    await user.type(screen.getByTestId('scope-label'), 'ikhnos.io/scope=yes')
    await create(user)
    await waitFor(() => expect(createToken).toHaveBeenCalledTimes(1))
    expect(createToken).toHaveBeenCalledWith(CONN, 'cluster-1', 2, {
      collectors: { nodeProbe: true, flowObserver: true, measurements: true },
      scope: { namespaces: ['shop', 'pay'], exclude: ['pay-test'], selector: 'ikhnos.io/scope=yes' },
    })
  })

  test('at the lower access level the collectors and scope that level cannot have are not asked for, whatever was ticked before', async () => {
    const user = userEvent.setup()
    wizard()
    await open(user)
    await user.click(screen.getByTestId('node-probe-toggle'))
    await user.click(screen.getByTestId('flow-observer-toggle'))
    await user.click(screen.getByTestId('measurements-toggle'))
    await user.click(screen.getByTestId('scope-toggle'))
    await user.type(screen.getByTestId('scope-include'), 'shop{enter}')
    await user.click(screen.getByTestId('wizard-tier-1'))
    await create(user)
    await waitFor(() => expect(createToken).toHaveBeenCalledTimes(1))
    expect(createToken).toHaveBeenCalledWith(CONN, 'cluster-1', 1, { collectors: { nodeProbe: true, flowObserver: false, measurements: false }, scope: NONE.scope })
  })

  test('a node probe asked for by the page that opened this is on from the start', async () => {
    const user = userEvent.setup()
    wizard({ nodeProbe: true })
    await create(user)
    await waitFor(() => expect(createToken).toHaveBeenCalledTimes(1))
    expect(createToken.mock.calls[0][3].collectors).toEqual({ nodeProbe: true, flowObserver: false, measurements: false })
  })

  test('a namespace that is not a name asks for nothing: Create stays off and says why', async () => {
    const user = userEvent.setup()
    wizard()
    await open(user)
    await user.click(screen.getByTestId('scope-toggle'))
    await user.type(screen.getByTestId('scope-include'), 'Not A Name{enter}')
    expect(screen.getByRole('alert')).toHaveTextContent('is not a valid namespace name')
    expect(screen.getByRole('button', { name: 'Create install command' })).toBeDisabled()
    expect(createToken).not.toHaveBeenCalled()
  })

  test('a refusal from the server is shown and nothing is installed from it', async () => {
    createToken.mockRejectedValue(new ApiError(400, 'the traffic observer needs access tier 2 or higher'))
    const user = userEvent.setup()
    wizard()
    await create(user)
    expect(await screen.findByText('the traffic observer needs access tier 2 or higher')).toBeInTheDocument()
    expect(screen.queryByTestId('install-command')).not.toBeInTheDocument()
  })
})
