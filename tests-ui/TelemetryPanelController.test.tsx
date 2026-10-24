import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'
import { beforeEach, describe, expect, test, vi } from 'vitest'
import { TelemetryPanel } from '@/features/pipeline/components/telemetry/TelemetryPanel'
import type { AgentUpgradeAnswer, AgentUpgradeRequest } from '@/lib/agentUpgrade'
import { DEFAULT_SETTINGS } from '@/lib/history'
import { answerTo, forgetUpgrades, patchTo } from './upgradeServer'

// An agent that a controller keeps is changed in its IkhnosAgent resource: a helm upgrade of it is put back by the controller. The panel
// offers the same change in both words, a tab each, and shows the patch the server wrote (what the server writes is pinned by the Go
// tests); what is held here is that the tab shows what comes back, says what the controller must have been given, and says why there is
// no patch when the resource cannot state the settings, while the helm command stays.

let tweak: (a: AgentUpgradeAnswer) => AgentUpgradeAnswer = (a) => a
beforeEach(() => {
  forgetUpgrades()
  tweak = (a) => a
})

vi.mock('@/store/settings', () => ({
  useSettings: () => ({ settings: DEFAULT_SETTINGS, loaded: true, error: undefined, save: vi.fn(async () => true) }),
}))
const CONN = { url: 'https://example.test', org: 'org-1' }
const connFn = () => CONN
const reloadInfo = vi.fn(async () => undefined)
vi.mock('@/store/server', () => ({
  useServer: (selector?: (s: { role?: string; conn: () => typeof CONN; reloadInfo: () => Promise<void> }) => unknown) => (selector ? selector({ role: 'editor', conn: connFn, reloadInfo }) : { role: 'editor', conn: connFn, reloadInfo }),
  useConn: () => CONN,
}))
vi.mock('@/lib/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/lib/api')>()
  return {
    ...actual,
    api: {
      ...actual.api,
      listOperatorDestinations: async () => [],
      listTelemetryIntents: async () => [],
      agentUpgradeCommand: async (c: unknown, req: AgentUpgradeRequest) => tweak(await (await import('./upgradeServer')).answer(c, req)),
    },
  }
})

async function toRun(user: ReturnType<typeof userEvent.setup>) {
  await user.click(screen.getByTestId('tp-resourceUsage'))
  await user.click(screen.getByTestId('tp-guided-continue'))
  await user.click(screen.getByTestId('tp-guided-destination-external-preset-honeycomb'))
  await user.click(screen.getByTestId('tp-guided-continue'))
  await user.click(screen.getByTestId('tp-guided-create-command'))
}

const renderPanel = () =>
  render(
    <MemoryRouter>
      <TelemetryPanel standalone testIdPrefix="tp" />
    </MemoryRouter>,
  )

describe('TelemetryPanel: the resource of a controller', () => {
  test('the helm command is shown first, and the other tab shows the patch the server wrote, with what the controller must have been given', async () => {
    const user = userEvent.setup()
    renderPanel()
    await toRun(user)
    const helm = await screen.findByTestId('helm-command')
    expect(screen.getByTestId('tp-via-helm')).toHaveAttribute('aria-selected', 'true')
    expect(screen.queryByTestId('tp-controller')).not.toBeInTheDocument()

    await user.click(screen.getByTestId('tp-via-controller'))
    expect(screen.getByTestId('tp-via-controller')).toHaveAttribute('aria-selected', 'true')
    expect(screen.queryByTestId('helm-command')).not.toBeInTheDocument()
    const req = JSON.parse(helm.textContent!.replace('stub-command ', '')) as AgentUpgradeRequest
    expect(await screen.findByTestId('tp-controller-command')).toHaveTextContent(patchTo(req))
    expect(screen.getByTestId('tp-controller-why')).toHaveTextContent('this-cluster')
    expect(screen.getByTestId('tp-controller-permission')).toHaveTextContent('allowTelemetry')

    // Back to helm: the command is what it was.
    await user.click(screen.getByTestId('tp-via-helm'))
    expect(await screen.findByTestId('helm-command')).toHaveTextContent(answerTo(req))
  })

  test('a draft the resource cannot state says why there is no patch, and the helm command is still there', async () => {
    tweak = (a) => ({ ...a, controller: { command: '', patch: '', needsTelemetryPermission: false, problems: ['extra processors are not on a resource yet: use the helm command'] } })
    const user = userEvent.setup()
    renderPanel()
    await toRun(user)
    expect(await screen.findByTestId('helm-command')).toBeInTheDocument()
    await user.click(screen.getByTestId('tp-via-controller'))
    expect(await screen.findByTestId('tp-controller-problems')).toHaveTextContent('There is no patch yet: extra processors')
    expect(screen.queryByTestId('tp-controller-command')).not.toBeInTheDocument()
    expect(screen.queryByTestId('tp-controller-permission')).not.toBeInTheDocument()
  })

  test('the permission note is for a patch that turns telemetry on only', async () => {
    tweak = (a) => ({ ...a, controller: { ...a.controller, needsTelemetryPermission: false } })
    const user = userEvent.setup()
    renderPanel()
    await toRun(user)
    await user.click(await screen.findByTestId('tp-via-controller'))
    expect(await screen.findByTestId('tp-controller-command')).toBeInTheDocument()
    expect(screen.queryByTestId('tp-controller-permission')).not.toBeInTheDocument()
  })

  test('the block to paste is the whole one, credentials first', async () => {
    tweak = (a) => ({ ...a, controller: { ...a.controller, command: `create-secret && ${a.controller.patch}` } })
    const user = userEvent.setup()
    renderPanel()
    await toRun(user)
    await user.click(await screen.findByTestId('tp-via-controller'))
    expect(await screen.findByTestId('tp-controller-command')).toHaveTextContent(/^create-secret && stub-patch/)
  })

  test('what is wrong with the draft itself is said in place of a patch', async () => {
    tweak = (a) => ({ ...a, problems: ['An export endpoint is required once any telemetry signal is on'], command: '', controller: { ...a.controller, command: '', patch: '', problems: [] } })
    const user = userEvent.setup()
    renderPanel()
    await toRun(user)
    await user.click(await screen.findByTestId('tp-via-controller'))
    expect(await screen.findByTestId('tp-controller-problems')).toHaveTextContent('export endpoint')
    expect(screen.queryByTestId('tp-controller-nothing')).not.toBeInTheDocument()
  })
})
