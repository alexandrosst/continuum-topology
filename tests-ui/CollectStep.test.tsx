import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState } from 'react'
import { describe, expect, test } from 'vitest'
import CollectStep from '@/features/pipeline/components/telemetry/CollectStep'
import { TELEMETRY_INTENT_PRESETS } from '@/lib/consent'
import { emptyTelemetry, PICKABLE_SIGNALS, type TelemetryInput } from '@/lib/install'

let latest: TelemetryInput = emptyTelemetry

function Wrapper({ initial = emptyTelemetry }: { initial?: TelemetryInput }) {
  const [value, setValue] = useState(initial)
  return (
    <CollectStep
      value={value}
      onChange={(v) => {
        latest = v
        setValue(v)
      }}
      testIdPrefix="c"
    />
  )
}

const checked = (id: string) => (screen.getByTestId(`c-${id}`) as HTMLInputElement).checked

describe('CollectStep', () => {
  test('shows every signal once, grouped by layer and then modality, with n-of-m counts', () => {
    render(<Wrapper initial={{ ...emptyTelemetry, resourceUsage: true, energy: true }} />)
    for (const s of PICKABLE_SIGNALS) expect(screen.getByTestId(`c-${s.id}`)).toBeInTheDocument()
    // The network-latency signal has no emitter yet, so it is not offered at all.
    expect(screen.queryByTestId('c-networkLatency')).not.toBeInTheDocument()
    const infra = screen.getByTestId('c-collect-layer-infrastructure')
    expect(within(infra).getByTestId('c-resourceUsage')).toBeInTheDocument()
    expect(within(infra).queryByTestId('c-traces')).not.toBeInTheDocument()
    expect(screen.getByTestId('c-collect-layer-application')).toContainElement(screen.getByTestId('c-traces'))
    const metricsTotal = PICKABLE_SIGNALS.filter((s) => s.layer === 'infrastructure' && s.modality === 'metrics').length
    expect(screen.getByTestId('c-collect-infrastructure-metrics-count')).toHaveTextContent(`2 of ${metricsTotal}`)
    expect(screen.getByTestId('c-collect-infrastructure-logs-count')).toHaveTextContent(/0 of 2/)
  })

  test('every checkbox writes straight into the draft, and unticking undoes it', async () => {
    const user = userEvent.setup()
    render(<Wrapper />)
    expect(checked('resourceUsage')).toBe(false)
    await user.click(screen.getByTestId('c-resourceUsage'))
    expect(latest.resourceUsage).toBe(true)
    await user.click(screen.getByTestId('c-resourceUsage'))
    expect(latest.resourceUsage).toBe(false)
  })

  test('Select all and Clear act on one modality group only', async () => {
    const user = userEvent.setup()
    render(<Wrapper />)
    await user.click(screen.getByTestId('c-collect-infrastructure-logs-all'))
    expect(checked('systemLogs')).toBe(true)
    expect(checked('kubernetesEvents')).toBe(true)
    expect(checked('resourceUsage')).toBe(false)
    expect(screen.getByTestId('c-collect-infrastructure-logs-all')).toBeDisabled()
    await user.click(screen.getByTestId('c-collect-infrastructure-logs-none'))
    expect(checked('systemLogs')).toBe(false)
    expect(screen.getByTestId('c-collect-infrastructure-logs-none')).toBeDisabled()
  })

  test('a group with a single signal has no Select all / Clear', () => {
    render(<Wrapper />)
    expect(screen.queryByTestId('c-collect-application-traces-all')).not.toBeInTheDocument()
  })

  test('a preset sets exactly its signals, replaces what was there, and shows as the active one', async () => {
    const user = userEvent.setup()
    render(<Wrapper initial={{ ...emptyTelemetry, traces: true }} />)
    const minimal = TELEMETRY_INTENT_PRESETS.find((p) => p.id === 'minimal')!
    await user.click(screen.getByTestId('c-collect-preset-minimal'))
    for (const s of PICKABLE_SIGNALS) expect(checked(s.id)).toBe(minimal.signals.includes(s.id))
    expect(screen.getByTestId('c-collect-preset-minimal')).toHaveAttribute('aria-pressed', 'true')
    expect(screen.getByTestId('c-collect-preset-full-infra')).toHaveAttribute('aria-pressed', 'false')
    // Adjusting a row afterwards no longer matches the preset exactly.
    await user.click(screen.getByTestId('c-energy'))
    expect(screen.getByTestId('c-collect-preset-minimal')).toHaveAttribute('aria-pressed', 'false')
  })

  test('a preset leaves everything that is not a signal alone', async () => {
    const user = userEvent.setup()
    render(<Wrapper initial={{ ...emptyTelemetry, exportEndpoint: 'otel.example.com:4317' }} />)
    await user.click(screen.getByTestId('c-collect-preset-full-infra'))
    expect(latest.exportEndpoint).toBe('otel.example.com:4317')
  })

  test('energy asks no question: turning it on shows no source picker and keeps the bundled Kepler', async () => {
    const user = userEvent.setup()
    render(<Wrapper />)
    await user.click(screen.getByTestId('c-energy'))
    expect(latest.energy).toBe(true)
    expect(latest.energySource).toBe('bundle-kepler')
    expect(screen.queryByTestId('c-energy-source')).not.toBeInTheDocument()
    expect(screen.queryByTestId('c-energy-endpoint')).not.toBeInTheDocument()
    expect(screen.getByTestId('c-energy').closest('label')).toHaveTextContent('privileged pod on every node')
  })

  test('a loaded configuration that already scrapes an existing Kepler keeps its picker and endpoint', async () => {
    const user = userEvent.setup()
    render(<Wrapper initial={{ ...emptyTelemetry, energy: true, energySource: 'existing', energyExistingEndpoint: 'kepler.monitoring:9102' }} />)
    expect(screen.getByTestId('c-energy-source')).toHaveTextContent('Scrape one I already run')
    expect(screen.getByTestId('c-energy-endpoint')).toHaveValue('kepler.monitoring:9102')
    // Switching it to the bundled Kepler does not make the picker vanish under the person's hand.
    await user.click(screen.getByTestId('c-energy-source'))
    await user.click(within(screen.getByRole('listbox')).getByRole('option', { name: /Deploy Kepler/ }))
    expect(latest.energySource).toBe('bundle-kepler')
    expect(screen.getByTestId('c-energy-source')).toBeInTheDocument()
    expect(screen.queryByTestId('c-energy-endpoint')).not.toBeInTheDocument()
  })

  test('accelerators show their source picker only while they are on', async () => {
    const user = userEvent.setup()
    render(<Wrapper />)
    expect(screen.queryByTestId('c-accelerators-source')).not.toBeInTheDocument()
    await user.click(screen.getByTestId('c-accelerators'))
    expect(screen.getByTestId('c-accelerators-source')).toBeInTheDocument()
    await user.click(screen.getByTestId('c-accelerators'))
    expect(screen.queryByTestId('c-accelerators-source')).not.toBeInTheDocument()
  })
})
