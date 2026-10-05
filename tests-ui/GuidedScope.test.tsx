import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState } from 'react'
import { beforeEach, describe, expect, test } from 'vitest'
import GuidedScope from '@/components/telemetry/GuidedScope'
import { emptyTelemetry, type TelemetryInput } from '@/lib/install'
import type { Service } from '@/lib/types'
import { useRawTopology } from '@/store/topology'

// The workload half of the scope step, and the switch that lets infrastructure signals follow the scope.

let latest: TelemetryInput = emptyTelemetry
function Wrapper({ initial, clusterId }: { initial: TelemetryInput; clusterId?: string }) {
  const [value, setValue] = useState(initial)
  return (
    <GuidedScope
      value={value}
      onChange={(v) => {
        latest = v
        setValue(v)
      }}
      testIdPrefix="s"
      clusterId={clusterId}
    />
  )
}

const svc = (name: string, namespace: string, clusterId = 'cl-1'): Service =>
  ({ id: `${clusterId}/${namespace}/${name}`, name, namespace, clusterId, kind: 'Deployment', replicas: 1, nodeIds: [], status: 'healthy', labels: {} }) as unknown as Service

const narrowed = (extra: Partial<TelemetryInput> = {}): TelemetryInput => ({
  ...emptyTelemetry,
  traces: true,
  tracesScope: { namespaces: ['checkout'], exclude: [], workloads: [] },
  ...extra,
})

beforeEach(() => {
  useRawTopology.setState({ services: [] })
})

describe('GuidedScope workloads', () => {
  test('a namespace of a scope can be narrowed to workloads typed by name, and made whole again', async () => {
    const user = userEvent.setup()
    render(<Wrapper initial={narrowed()} />)
    const picker = screen.getByTestId(/^s-guided-draft-draft-\d+-wl$/)
    const id = picker.getAttribute('data-testid')!
    expect(screen.getByTestId(`${id}-checkout-summary`)).toHaveTextContent('all workloads')
    await user.click(screen.getByTestId(`${id}-checkout-toggle`))
    expect(screen.getByTestId(`${id}-checkout-summary`)).toHaveTextContent('none selected, so this namespace sends nothing')
    await user.type(screen.getByTestId(`${id}-checkout-names`), 'cart{Enter}')
    expect(screen.getByTestId(`${id}-checkout-summary`)).toHaveTextContent('1 workloads')
    expect(latest.tracesScope.workloads).toEqual([{ namespace: 'checkout', names: ['cart'] }])
    await user.click(screen.getByTestId(`${id}-checkout-toggle`))
    expect(latest.tracesScope.workloads).toEqual([])
  })

  test('discovered workloads of the cluster are offered as choices, starting with all of them chosen', async () => {
    const user = userEvent.setup()
    useRawTopology.setState({ services: [svc('cart', 'checkout'), svc('payment-api', 'checkout'), svc('other', 'checkout', 'cl-2'), svc('ledger', 'payments')] })
    render(<Wrapper initial={narrowed()} clusterId="cl-1" />)
    const id = screen.getByTestId(/^s-guided-draft-draft-\d+-wl$/).getAttribute('data-testid')!
    await user.click(screen.getByTestId(`${id}-checkout-toggle`))
    expect(screen.getByTestId(`${id}-checkout-summary`)).toHaveTextContent('2 of 2 workloads')
    expect(screen.queryByTestId(`${id}-checkout-w-other`)).not.toBeInTheDocument()
    await user.click(screen.getByTestId(`${id}-checkout-w-payment-api`))
    expect(screen.getByTestId(`${id}-checkout-summary`)).toHaveTextContent('1 of 2 workloads')
    expect(latest.tracesScope.workloads).toEqual([{ namespace: 'checkout', names: ['cart'] }])
  })

  test('taking a namespace out of the scope takes its workloads with it', async () => {
    const user = userEvent.setup()
    render(<Wrapper initial={narrowed({ tracesScope: { namespaces: ['checkout'], exclude: [], workloads: [{ namespace: 'checkout', names: ['cart'] }] } })} />)
    await user.click(screen.getByLabelText(/remove checkout/i))
    expect(latest.tracesScope.workloads).toEqual([])
  })
})

describe('GuidedScope infrastructure switch', () => {
  test('not offered when no infrastructure signal that can follow a scope is on', () => {
    render(<Wrapper initial={narrowed({ systemLogs: true, energy: true })} />)
    expect(screen.queryByTestId('s-guided-infra')).not.toBeInTheDocument()
  })

  test('not offered when nothing is narrowed', () => {
    render(<Wrapper initial={{ ...emptyTelemetry, traces: true, resourceUsage: true }} />)
    expect(screen.queryByTestId('s-guided-infra')).not.toBeInTheDocument()
  })

  test('says what the infrastructure signals follow, signal by signal, and switches off', async () => {
    const user = userEvent.setup()
    render(
      <Wrapper
        initial={narrowed({ resourceUsage: true, kubernetesEvents: true, systemLogs: true, tracesScope: { namespaces: ['checkout'], exclude: [], workloads: [{ namespace: 'checkout', names: ['cart'] }] } })}
      />,
    )
    expect(screen.getByTestId('s-guided-infra-follow')).toBeChecked()
    expect(screen.getByTestId('s-guided-infra-where')).toHaveTextContent('checkout: cart')
    expect(screen.getByTestId('s-guided-infra-effect-resourceUsage')).toHaveTextContent('Pod and container metrics follow the scope')
    expect(screen.getByTestId('s-guided-infra-effect-kubernetesEvents')).toHaveTextContent('workloads do not apply to events')
    expect(screen.getByTestId('s-guided-infra-effect-systemLogs')).toHaveTextContent('Not narrowed')
    expect(screen.queryByTestId('s-guided-infra-effect-energy')).not.toBeInTheDocument()
    await user.click(screen.getByTestId('s-guided-infra-follow'))
    expect(latest.scopeInfrastructure).toBe(false)
    expect(screen.getByTestId('s-guided-infra-effect-resourceUsage')).toHaveTextContent('Whole cluster.')
  })
})
