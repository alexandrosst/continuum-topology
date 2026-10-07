import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { StrictMode } from 'react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, test, vi } from 'vitest'
import { expiryText, FusionAccess, fusionStatusCurl } from '@/components/operators/FusionAccess'
import { FusionPanel } from '@/components/operators/FusionPanel'
import { isReloadHeld } from '@/lib/staleBuild'
import type { CreatedFusionAccessToken, FusionAccessToken, FusionStatus } from '@/lib/api'

const listFusionTokens = vi.fn()
const createFusionToken = vi.fn()
const revokeFusionToken = vi.fn()
// conn must be the same function on every render, as the real store's is: the list is read again whenever it changes.
const conn = () => ({ url: 'https://ikhnos.example', org: 'o' })
vi.mock('@/store/server', () => ({ useServer: (sel: (s: { conn: typeof conn }) => unknown) => sel({ conn }) }))
vi.mock('@/lib/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/lib/api')>()
  return {
    ...actual,
    api: {
      ...actual.api,
      listFusionTokens: (...a: unknown[]) => listFusionTokens(...a),
      createFusionToken: (...a: unknown[]) => createFusionToken(...a),
      revokeFusionToken: (...a: unknown[]) => revokeFusionToken(...a),
    },
  }
})

const day = 86_400_000
const token = (over: Partial<FusionAccessToken> = {}): FusionAccessToken => ({
  id: 'fk-1', name: 'Decision engine', signals: ['metrics', 'logs', 'traces'], namespaces: [], clusters: [], createdBy: 'alex',
  createdAt: new Date(Date.now() - 2 * day).toISOString(), expiresAt: new Date(Date.now() + 29.5 * day).toISOString(), ...over,
})

beforeEach(() => {
  listFusionTokens.mockReset()
  createFusionToken.mockReset()
  revokeFusionToken.mockReset()
})

describe('expiryText', () => {
  test('counts whole days, and says so when it is today or over', () => {
    const now = Date.parse('2026-10-05T12:00:00Z')
    expect(expiryText('2026-10-20T12:00:00Z', now)).toBe('expires in 15 days')
    expect(expiryText('2026-10-05T18:00:00Z', now)).toBe('expires today')
    expect(expiryText('2026-10-05T11:00:00Z', now)).toBe('expired')
  })
})

describe('FusionAccess', () => {
  test('lists each token with what it may read, when it expires and when it was last used', async () => {
    listFusionTokens.mockResolvedValue([
      token(),
      token({ id: 'fk-2', name: 'Shop dashboard', signals: ['traces', 'logs'], namespaces: ['shop', 'pay'], clusters: ['cl-1'], lastUsedAt: new Date(Date.now() - 3 * 3600_000).toISOString() }),
    ])
    render(<FusionAccess />)
    const first = await screen.findByTestId('fusion-token-fk-1')
    expect(within(first).getByText('Decision engine')).toBeInTheDocument()
    expect(within(first).getByText('all signals')).toBeInTheDocument()
    expect(within(first).getByText('all namespaces')).toBeInTheDocument()
    expect(within(first).getByText(/never used/)).toBeInTheDocument()
    const second = screen.getByTestId('fusion-token-fk-2')
    expect(within(second).getByText('traces + logs')).toBeInTheDocument()
    expect(within(second).getByText('namespaces: shop, pay')).toBeInTheDocument()
    expect(within(second).getByText('clusters: cl-1')).toBeInTheDocument()
    expect(within(second).getByText(/last used 3 h ago/)).toBeInTheDocument()
  })

  test('an empty list says so, and a failed one says why', async () => {
    listFusionTokens.mockResolvedValueOnce([])
    const { unmount } = render(<FusionAccess />)
    expect(await screen.findByTestId('fusion-token-empty')).toBeInTheDocument()
    unmount()
    const { ApiError } = await import('@/lib/api')
    listFusionTokens.mockRejectedValueOnce(new ApiError(403, 'Only an administrator can do that.'))
    render(<FusionAccess />)
    expect(await screen.findByText('Only an administrator can do that.')).toBeInTheDocument()
  })

  test('makes a token from the name, the signals, the namespaces and the lifetime, and shows its secret once', async () => {
    const user = userEvent.setup()
    listFusionTokens.mockResolvedValue([])
    const created: CreatedFusionAccessToken = { token: 'cnf_SECRETSECRETSECRET', details: token({ id: 'fk-9', name: 'Shop dashboard', namespaces: ['shop'] }) }
    createFusionToken.mockResolvedValue(created)
    render(<FusionAccess />)
    await screen.findByTestId('fusion-token-empty')
    await user.click(screen.getByTestId('fusion-token-new'))

    const create = screen.getByTestId('fusion-token-create')
    expect(create).toBeDisabled() // no name yet
    await user.type(screen.getByTestId('fusion-token-name'), 'Shop dashboard')
    expect(create).toBeEnabled()
    await user.click(screen.getByTestId('checkbox-metrics')) // not metrics
    await user.type(screen.getByTestId('fusion-token-namespaces'), 'shop ')
    expect(screen.getByText(/can use the structured filters only/)).toBeInTheDocument()
    await user.click(create)

    expect(createFusionToken).toHaveBeenCalledWith(expect.anything(), { name: 'Shop dashboard', signals: ['logs', 'traces'], namespaces: ['shop'], clusters: [], expiresInDays: 90 })
    const secret = await screen.findByTestId('fusion-token-secret')
    expect(within(secret).getByText('cnf_SECRETSECRETSECRET')).toBeInTheDocument()
    expect(screen.getByTestId('fusion-token-curl')).toHaveTextContent('curl -H "Authorization: Bearer cnf_SECRETSECRETSECRET" https://ikhnos.example/api/v1/fusion/status')
    await waitFor(() => expect(listFusionTokens).toHaveBeenCalledTimes(2)) // the list is read again
    await user.click(screen.getByRole('button', { name: 'Done' }))
    expect(screen.queryByText('cnf_SECRETSECRETSECRET')).not.toBeInTheDocument()
  })

  test('a token must be allowed to read something', async () => {
    const user = userEvent.setup()
    listFusionTokens.mockResolvedValue([])
    render(<FusionAccess />)
    await user.click(await screen.findByTestId('fusion-token-new'))
    await user.type(screen.getByTestId('fusion-token-name'), 'x')
    for (const s of ['metrics', 'logs', 'traces']) await user.click(screen.getByTestId(`checkbox-${s}`))
    expect(screen.getByTestId('fusion-token-create')).toBeDisabled()
  })

  test("the server's refusal is shown in the form", async () => {
    const user = userEvent.setup()
    listFusionTokens.mockResolvedValue([])
    const { ApiError } = await import('@/lib/api')
    createFusionToken.mockRejectedValue(new ApiError(400, '"shop;drop" is not a valid namespace'))
    render(<FusionAccess />)
    await user.click(await screen.findByTestId('fusion-token-new'))
    await user.type(screen.getByTestId('fusion-token-name'), 'x')
    await user.click(screen.getByTestId('fusion-token-create'))
    expect(await screen.findByText(/is not a valid namespace/)).toBeInTheDocument()
    expect(screen.queryByTestId('fusion-token-secret')).not.toBeInTheDocument()
  })

  test('revoking asks first, then ends the token and reads the list again', async () => {
    const user = userEvent.setup()
    listFusionTokens.mockResolvedValueOnce([token()]).mockResolvedValue([])
    revokeFusionToken.mockResolvedValue(undefined)
    render(<FusionAccess />)
    await user.click(await screen.findByRole('button', { name: 'Revoke Decision engine' }))
    expect(revokeFusionToken).not.toHaveBeenCalled()
    expect(screen.getByText(/stops being able to read FUSION at once/)).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Revoke' }))
    await waitFor(() => expect(revokeFusionToken).toHaveBeenCalledWith(expect.anything(), 'fk-1'))
    expect(await screen.findByTestId('fusion-token-empty')).toBeInTheDocument()
  })
})

describe('the FUSION card', () => {
  const fusion = (status: FusionStatus) => ({ status, busy: false, error: '', refresh: vi.fn(), enable: vi.fn(), disable: vi.fn() }) as never

  test('offers access only where the server serves the data API', async () => {
    listFusionTokens.mockResolvedValue([])
    const off: FusionStatus = { available: true, state: 'off', data: false }
    const { rerender } = render(<FusionPanel fusion={fusion(off)} />)
    expect(screen.queryByTestId('fusion-access')).not.toBeInTheDocument()
    rerender(<FusionPanel fusion={fusion({ ...off, data: true })} />)
    expect(await screen.findByTestId('fusion-access')).toBeInTheDocument()
  })

  test('is there even when the switch is not available to this server', async () => {
    listFusionTokens.mockResolvedValue([])
    render(<FusionPanel fusion={fusion({ available: false, state: 'off', reason: 'not-configured', message: 'No switch.', data: true })} />)
    expect(await screen.findByTestId('fusion-access')).toBeInTheDocument()
  })
})

describe('FusionAccess under React StrictMode (main.tsx renders the app inside it)', () => {
  test('the token list still fills in: the flag that stops a late answer is set back on the second mount', async () => {
    listFusionTokens.mockResolvedValue([token()])
    render(<StrictMode><FusionAccess /></StrictMode>)
    expect(await screen.findByTestId('fusion-token-fk-1')).toBeInTheDocument()
  })
})

describe('a token that is shown once', () => {
  const created: CreatedFusionAccessToken = { token: 'cnf_SECRETSECRETSECRET', details: token({ id: 'fk-9', name: 'Shop dashboard' }) }

  test('the created view ignores Escape and a click outside, holds the page from reloading, and Done is the way out', async () => {
    const user = userEvent.setup()
    listFusionTokens.mockResolvedValue([])
    createFusionToken.mockResolvedValue(created)
    render(<FusionAccess />)
    await user.click(await screen.findByTestId('fusion-token-new'))
    await user.type(screen.getByTestId('fusion-token-name'), 'Shop dashboard')
    await user.click(screen.getByTestId('fusion-token-create'))
    const dialog = await screen.findByRole('dialog', { name: 'Access token created' })
    expect(isReloadHeld()).toBe(true)
    await user.keyboard('{Escape}')
    fireEvent.mouseDown(dialog.parentElement!)
    expect(screen.getByRole('dialog', { name: 'Access token created' })).toBeInTheDocument()
    expect(screen.getByText('cnf_SECRETSECRETSECRET')).toBeInTheDocument()
    await user.click(screen.getByTestId('fusion-token-done'))
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    expect(isReloadHeld()).toBe(false)
  })

  test('while the token is being made there is no way out of the form: Escape, a click outside and Cancel do nothing', async () => {
    const user = userEvent.setup()
    listFusionTokens.mockResolvedValue([])
    let finish: (c: CreatedFusionAccessToken) => void = () => undefined
    createFusionToken.mockImplementation(() => new Promise<CreatedFusionAccessToken>((r) => { finish = r }))
    render(<FusionAccess />)
    await user.click(await screen.findByTestId('fusion-token-new'))
    await user.type(screen.getByTestId('fusion-token-name'), 'Shop dashboard')
    await user.click(screen.getByTestId('fusion-token-create'))
    const dialog = screen.getByRole('dialog', { name: 'New access token' })
    await user.keyboard('{Escape}')
    fireEvent.mouseDown(dialog.parentElement!)
    expect(screen.getByRole('dialog', { name: 'New access token' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Cancel' })).toBeDisabled()
    await user.click(screen.getByTestId('fusion-token-create')) // a second press while it runs
    expect(createFusionToken).toHaveBeenCalledTimes(1)
    finish(created)
    expect(await screen.findByRole('dialog', { name: 'Access token created' })).toBeInTheDocument()
  })

  test('before anything is sent the form closes the ordinary ways', async () => {
    const user = userEvent.setup()
    listFusionTokens.mockResolvedValue([])
    render(<FusionAccess />)
    await user.click(await screen.findByTestId('fusion-token-new'))
    await user.keyboard('{Escape}')
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  })
})

describe('fusionStatusCurl', () => {
  test('keeps the familiar double-quoted header for a plain token, and quotes anything else for the shell', () => {
    expect(fusionStatusCurl('cnf_abc', 'https://ikhnos.example')).toBe('curl -H "Authorization: Bearer cnf_abc" https://ikhnos.example/api/v1/fusion/status')
    expect(fusionStatusCurl('a"; rm -rf ~ #', 'https://x')).toBe(`curl -H 'Authorization: Bearer a"; rm -rf ~ #' https://x/api/v1/fusion/status`)
    expect(fusionStatusCurl('t', 'https://x/a b')).toBe(`curl -H "Authorization: Bearer t" 'https://x/a b/api/v1/fusion/status'`)
  })
})
