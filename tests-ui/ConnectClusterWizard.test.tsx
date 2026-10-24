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

const NONE = { collectors: { nodeProbe: false, flowObserver: false, measurements: false }, scope: { namespaces: [], exclude: [], selector: '' }, purgeIdentity: false }
const CONTROLLER = {
  available: true,
  reason: '',
  namespace: 'ikhnos-system',
  maxTier: 2,
  install: 'if helm status ikhnos-controller --namespace ikhnos-system >/dev/null 2>&1; then\n  echo present\nelse\n  helm install ikhnos-controller oci://r.example/ikhnos-controller \\\n  --set maxAccessTier=2\nfi',
  raise: 'helm upgrade ikhnos-controller oci://r.example/ikhnos-controller --namespace ikhnos-system --reset-then-reuse-values --set maxAccessTier=2',
  secret: "kubectl apply --server-side -f - <<'IKHNOS_SECRET'\nstringData:\n  token: \"ikh_x\"\nIKHNOS_SECRET",
  manifest: 'kind: IkhnosAgent\n',
  manifestCommand: "kubectl apply -f - <<'YAML'\nkind: IkhnosAgent\nYAML",
}
const SERVED = 'helm install ikhnos-agent oci://r.example/ikhnos-agent \\\n  --namespace ikhnos-system \\\n  --set enrollment.key=PIN.ikh_x'

beforeEach(() => {
  createToken.mockReset()
  createToken.mockResolvedValue({ token: 'ikh_x', install: SERVED, controller: CONTROLLER, meta: { id: 't1', name: 'cluster-1', tier: 2, createdAt: new Date().toISOString(), expiresAt: new Date(Date.now() + 3_600_000).toISOString() } })
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
      purgeIdentity: false,
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
    expect(createToken).toHaveBeenCalledWith(CONN, 'cluster-1', 1, { collectors: { nodeProbe: true, flowObserver: false, measurements: false }, scope: NONE.scope, purgeIdentity: false })
  })

  test('the identity is only asked to be deleted at uninstall when that was ticked', async () => {
    const user = userEvent.setup()
    wizard()
    await open(user)
    expect(screen.getByTestId('purge-identity-toggle')).not.toBeChecked()
    await user.click(screen.getByTestId('purge-identity-toggle'))
    await create(user)
    await waitFor(() => expect(createToken).toHaveBeenCalledTimes(1))
    expect(createToken.mock.calls[0][3].purgeIdentity).toBe(true)
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

describe('ConnectClusterWizard: the same install as a resource for the controller', () => {
  const made = async () => {
    const user = userEvent.setup()
    wizard()
    await create(user)
    await screen.findByTestId('install-command')
    return user
  }

  test('opens on the helm command and offers the controller beside it', async () => {
    await made()
    expect(screen.getByTestId('install-tab-helm')).toHaveAttribute('aria-selected', 'true')
    expect(screen.getByTestId('install-tab-controller')).toHaveAttribute('aria-selected', 'false')
    expect(screen.queryByTestId('controller-steps')).not.toBeInTheDocument()
  })

  test('shows the three things to run, in order, as the server wrote them', async () => {
    const user = await made()
    await user.click(screen.getByTestId('install-tab-controller'))
    const steps = await screen.findByTestId('controller-steps')
    expect(screen.queryByTestId('install-command')).not.toBeInTheDocument()
    const order = ['controller-install', 'controller-secret', 'controller-manifest'].map((id) => screen.getByTestId(id))
    expect(order.map((e) => e.textContent)).toEqual([CONTROLLER.install, CONTROLLER.secret, CONTROLLER.manifestCommand])
    // In the order the page lists them, top to bottom.
    for (let i = 1; i < order.length; i++) expect(order[i - 1].compareDocumentPosition(order[i]) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    // The token is in the Secret's command and in no other.
    expect(order.map((e) => e.textContent?.includes('ikh_x'))).toEqual([false, true, false])
    expect(steps).toHaveTextContent('maxAccessTier=2')
    // A controller that is there is left alone, and the command for a ceiling too low is the server's, not a placeholder.
    expect(screen.getByTestId('controller-install')).toHaveTextContent('helm status ikhnos-controller')
    expect(screen.getByTestId('controller-raise').textContent).toBe(CONTROLLER.raise)
    expect(steps).not.toHaveTextContent('<chart>')
  })

  test('goes back to the helm command', async () => {
    const user = await made()
    await user.click(screen.getByTestId('install-tab-controller'))
    await user.click(screen.getByTestId('install-tab-helm'))
    expect(screen.getByTestId('install-command').textContent).toBe(SERVED)
    expect(screen.queryByTestId('controller-steps')).not.toBeInTheDocument()
  })

  test('says why there is nothing to run when the server cannot name the controller chart', async () => {
    createToken.mockResolvedValue({
      token: 'ikh_x', install: SERVED, meta: { id: 't1', name: 'cluster-1', tier: 2, createdAt: new Date().toISOString(), expiresAt: new Date(Date.now() + 3_600_000).toISOString() },
      controller: { available: false, reason: 'This server has no image registry configured.', namespace: 'ikhnos-system', maxTier: 2, install: '', raise: '', secret: '', manifest: '', manifestCommand: '' },
    })
    const user = await made()
    await user.click(screen.getByTestId('install-tab-controller'))
    expect(await screen.findByTestId('controller-unavailable')).toHaveTextContent('This server has no image registry configured.')
    expect(screen.queryByTestId('controller-steps')).not.toBeInTheDocument()
    // The way that works is still there.
    await user.click(screen.getByTestId('install-tab-helm'))
    expect(screen.getByTestId('install-command').textContent).toBe(SERVED)
  })

  test('names the ceiling the controller was given', async () => {
    createToken.mockResolvedValue({
      token: 'ikh_x', install: SERVED, meta: { id: 't1', name: 'cluster-1', tier: 1, createdAt: new Date().toISOString(), expiresAt: new Date(Date.now() + 3_600_000).toISOString() },
      controller: { ...CONTROLLER, maxTier: 1, install: CONTROLLER.install.replace('=2', '=1'), raise: CONTROLLER.raise.replace('=2', '=1') },
    })
    const user = await made()
    await user.click(screen.getByTestId('install-tab-controller'))
    expect(await screen.findByTestId('controller-steps')).toHaveTextContent('access tier 1 and no more')
    expect(screen.getByTestId('controller-install')).toHaveTextContent('--set maxAccessTier=1')
    expect(screen.getByTestId('controller-raise')).toHaveTextContent('--set maxAccessTier=1')
    expect(screen.getByTestId('controller-steps')).toHaveTextContent('lower ceiling than tier 1')
  })
})
