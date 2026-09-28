import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState } from 'react'
import { describe, expect, test } from 'vitest'
import TelemetryFields from '@/components/telemetry/TelemetryFields'
import { emptyTelemetry, type TelemetryInput } from '@/lib/install'

function Wrapper({ initial = emptyTelemetry }: { initial?: TelemetryInput }) {
  const [value, setValue] = useState<TelemetryInput>(initial)
  return <TelemetryFields value={value} onChange={setValue} />
}

describe('TelemetryFields', () => {
  test('renders a checkbox for every signal, including accelerators, and redaction is checked by default', () => {
    render(<Wrapper />)
    expect(screen.getByTestId('telemetry-resourceUsage')).not.toBeChecked()
    expect(screen.getByTestId('telemetry-accelerators')).not.toBeChecked()
    expect(screen.getByTestId('telemetry-redaction')).toBeChecked()
    expect(screen.getByTestId('telemetry-resource-detection')).not.toBeChecked()
  })

  test('turning on accelerators reveals its source picker and, for an existing source, its endpoint field', async () => {
    const user = userEvent.setup()
    render(<Wrapper />)
    await user.click(screen.getByTestId('telemetry-accelerators'))
    expect(screen.getByTestId('telemetry-accelerators-source')).toBeInTheDocument()
    expect(screen.queryByTestId('telemetry-accelerators-endpoint')).not.toBeInTheDocument()
    await user.click(screen.getByTestId('telemetry-accelerators-source'))
    await user.click(screen.getByRole('option', { name: 'Scrape one I already run' }))
    expect(screen.getByTestId('telemetry-accelerators-endpoint')).toBeInTheDocument()
  })

  test('picking an intent preset turns on exactly its signals, turning off any that were on before', async () => {
    const user = userEvent.setup()
    render(<Wrapper initial={{ ...emptyTelemetry, traces: true }} />)
    expect(screen.getByTestId('telemetry-traces')).toBeChecked()
    await user.click(screen.getByTestId('telemetry-intent-preset'))
    await user.click(screen.getByRole('option', { name: 'Minimal / cost-aware' }))
    expect(screen.getByTestId('telemetry-resourceUsage')).toBeChecked()
    expect(screen.getByTestId('telemetry-kubernetesState')).toBeChecked()
    expect(screen.getByTestId('telemetry-traces')).not.toBeChecked()
  })

  test('a permissions summary only appears once a signal is on', async () => {
    const user = userEvent.setup()
    render(<Wrapper />)
    expect(screen.queryByTestId('telemetry-permissions-summary')).not.toBeInTheDocument()
    await user.click(screen.getByTestId('telemetry-resourceUsage'))
    expect(screen.getByTestId('telemetry-permissions-summary')).toHaveTextContent('Resource usage')
  })

  test('credentials & processing start collapsed by default, but open automatically when a non-default value is already set', async () => {
    const user = userEvent.setup()
    const { unmount } = render(<Wrapper />)
    expect(screen.getByTestId('telemetry-redaction')).not.toBeVisible()
    expect(screen.getByTestId('telemetry-advanced')).not.toHaveAttribute('open')
    await user.click(screen.getByText('Credentials & processing'))
    expect(screen.getByTestId('telemetry-redaction')).toBeVisible()
    unmount()

    render(<Wrapper initial={{ ...emptyTelemetry, resourceDetection: true }} />)
    expect(screen.getByTestId('telemetry-advanced')).toHaveAttribute('open')
    expect(screen.getByTestId('telemetry-resource-detection')).toBeVisible()
  })

  test('typing a known-unsupported destination shows why, without hiding the field', async () => {
    const user = userEvent.setup()
    render(<Wrapper />)
    // The destination ComboField starts in dropdown mode (its value is empty, which "matches"); switch to
    // free text the same way a person picking a destination not in the list would. Found by its own
    // placeholder text rather than the surrounding Field's label, since the accessible name of a control
    // wrapped by a label alongside other label text does not reliably resolve to just that label's text.
    await user.click(screen.getByText('otel-gateway.example.com:4317').closest('button')!)
    await user.click(screen.getByRole('option', { name: 'Other…' }))
    const combo = screen.getByPlaceholderText('otel-gateway.example.com:4317')
    await user.type(combo, 'otlp.aws.example.com')
    const alerts = screen.getAllByRole('alert')
    expect(alerts.some((a) => /AWS/.test(a.textContent ?? ''))).toBe(true)
  })
})
