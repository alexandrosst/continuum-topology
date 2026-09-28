import { render, screen, within } from '@testing-library/react'
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

describe('TelemetryFields facets and application scope overrides', () => {
  test('facets narrow the visible signal checkboxes, and default to showing everything', async () => {
    const user = userEvent.setup()
    render(<Wrapper />)
    expect(screen.getByTestId('telemetry-resourceUsage')).toBeInTheDocument()
    expect(screen.getByTestId('telemetry-kubernetesState')).toBeInTheDocument()
    expect(screen.getByTestId('telemetry-traces')).toBeInTheDocument()

    await user.click(screen.getByTestId('telemetry-facet-scope'))
    await user.click(screen.getByRole('option', { name: 'Cluster' }))
    expect(screen.getByTestId('telemetry-kubernetesState')).toBeInTheDocument()
    expect(screen.queryByTestId('telemetry-resourceUsage')).not.toBeInTheDocument()
    expect(screen.queryByTestId('telemetry-traces')).not.toBeInTheDocument()

    // purely a browsing aid - resetting back to "All" shows everything again, and nothing about it was
    // ever written into the TelemetryInput value the checkboxes themselves are bound to
    await user.click(screen.getByTestId('telemetry-facet-scope'))
    await user.click(screen.getByRole('option', { name: 'All' }))
    expect(screen.getByTestId('telemetry-resourceUsage')).toBeInTheDocument()
    expect(screen.getByTestId('telemetry-traces')).toBeInTheDocument()
  })

  test('the accelerators "apply scope" checkbox only appears once accelerators is checked', async () => {
    const user = userEvent.setup()
    render(<Wrapper />)
    expect(screen.queryByTestId('telemetry-accelerators-apply-scope')).not.toBeInTheDocument()
    await user.click(screen.getByTestId('telemetry-accelerators'))
    expect(screen.getByTestId('telemetry-accelerators-apply-scope')).toBeInTheDocument()
    expect(screen.getByTestId('telemetry-accelerators-apply-scope')).not.toBeChecked()
  })

  test('application scope overrides only appear once an application-layer signal is on, start collapsed, and open automatically when a kind already carries one', async () => {
    const user = userEvent.setup()
    const { unmount } = render(<Wrapper />)
    expect(screen.queryByTestId('telemetry-scope-overrides')).not.toBeInTheDocument()
    await user.click(screen.getByTestId('telemetry-applicationMetrics'))
    expect(screen.getByTestId('telemetry-scope-overrides')).toBeInTheDocument()
    expect(screen.getByTestId('telemetry-scope-overrides')).not.toHaveAttribute('open')
    expect(screen.getByTestId('telemetry-applicationMetrics-scope-namespaces')).not.toBeVisible()
    await user.click(screen.getByText('Application scope overrides'))
    expect(screen.getByTestId('telemetry-applicationMetrics-scope-namespaces')).toBeVisible()
    unmount()

    render(
      <Wrapper
        initial={{
          ...emptyTelemetry,
          applicationMetrics: true,
          applicationMetricsScope: { namespaces: ['shop'], exclude: [] },
        }}
      />,
    )
    expect(screen.getByTestId('telemetry-scope-overrides')).toHaveAttribute('open')
  })
})


describe('TelemetryFields guided mode', () => {
  test('defaults to the flat grid, and switching modes does not lose what was already picked', async () => {
    const user = userEvent.setup()
    render(<Wrapper initial={{ ...emptyTelemetry, resourceUsage: true }} />)
    expect(screen.getByTestId('telemetry-mode-flat')).toHaveAttribute('aria-checked', 'true')
    expect(screen.getByTestId('telemetry-facet-scope')).toBeInTheDocument()
    expect(screen.getByTestId('telemetry-resourceUsage')).toBeChecked()

    await user.click(screen.getByTestId('telemetry-mode-guided'))
    expect(screen.getByTestId('telemetry-mode-guided')).toHaveAttribute('aria-checked', 'true')
    expect(screen.queryByTestId('telemetry-facet-scope')).not.toBeInTheDocument()
    expect(screen.getByTestId('telemetry-resourceUsage')).toBeChecked()
  })

  test('the scope wizard only appears once an application-scoped signal is on', async () => {
    const user = userEvent.setup()
    render(<Wrapper />)
    await user.click(screen.getByTestId('telemetry-mode-guided'))
    expect(screen.queryByTestId('telemetry-guided-add-scope')).not.toBeInTheDocument()
    await user.click(screen.getByTestId('telemetry-applicationMetrics'))
    expect(screen.getByTestId('telemetry-guided-add-scope')).toBeInTheDocument()
    expect(screen.getByTestId('telemetry-guided-attach-applicationMetrics')).toBeInTheDocument()
  })

  test('naming a scope and attaching a signal to it copies its namespaces into that signal\'s override', async () => {
    const user = userEvent.setup()
    render(<Wrapper initial={{ ...emptyTelemetry, applicationMetrics: true }} />)
    await user.click(screen.getByTestId('telemetry-mode-guided'))
    await user.click(screen.getByTestId('telemetry-guided-add-scope'))
    const nameInput = screen.getByPlaceholderText('Name this scope')
    await user.clear(nameInput)
    await user.type(nameInput, 'Payments')
    const draftBox = nameInput.closest('div[data-testid^="telemetry-guided-draft-"]') as HTMLElement
    const nsInput = within(draftBox).getByPlaceholderText('shop payments')
    await user.type(nsInput, 'shop{Enter}')

    const attachSelect = screen.getByTestId('telemetry-guided-attach-applicationMetrics')
    await user.selectOptions(attachSelect, 'Use Payments')

    // Switching that signal to "custom" reveals its own fields pre-filled with what was just copied in.
    await user.selectOptions(attachSelect, 'Custom, just for this signal')
    const customNsInput = screen.getByTestId('telemetry-guided-custom-namespaces-applicationMetrics')
    expect(customNsInput.closest('div')).toHaveTextContent('shop')
  })

  test('two scopes that both include the same namespace warn, and merging keeps only one', async () => {
    const user = userEvent.setup()
    render(<Wrapper initial={{ ...emptyTelemetry, applicationMetrics: true, applicationLogs: true }} />)
    await user.click(screen.getByTestId('telemetry-mode-guided'))
    await user.click(screen.getByTestId('telemetry-guided-add-scope'))
    await user.click(screen.getByTestId('telemetry-guided-add-scope'))

    const nameInputs = screen.getAllByPlaceholderText('Name this scope')
    expect(nameInputs).toHaveLength(2)
    for (const nameInput of nameInputs) {
      const draftBox = nameInput.closest('div[data-testid^="telemetry-guided-draft-"]') as HTMLElement
      await user.type(within(draftBox).getByPlaceholderText('shop payments'), 'shop{Enter}')
    }

    const alerts = screen.getAllByRole('alert')
    expect(alerts.some((a) => /shop/.test(a.textContent ?? ''))).toBe(true)
    await user.click(screen.getAllByText(/Merge into/)[0])
    expect(screen.getAllByPlaceholderText('Name this scope')).toHaveLength(1)
  })
})
