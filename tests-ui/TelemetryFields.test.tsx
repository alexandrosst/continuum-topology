import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState } from 'react'
import { describe, expect, test } from 'vitest'
import TelemetryFields from '@/components/telemetry/TelemetryFields'
import { emptyTelemetry, type TelemetryInput } from '@/lib/install'

function Wrapper({ initial = emptyTelemetry, initialScope }: { initial?: TelemetryInput; initialScope?: { name: string; namespaces: string[] } }) {
  const [value, setValue] = useState<TelemetryInput>(initial)
  return <TelemetryFields value={value} onChange={setValue} initialScope={initialScope} />
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

  test('the destination picker hides a backend that cannot carry an already-enabled modality', async () => {
    const user = userEvent.setup()
    // Metrics is on; Jaeger (traces only) should not be offered at all.
    render(<Wrapper initial={{ ...emptyTelemetry, resourceUsage: true }} />)
    await user.click(screen.getByRole('combobox', { name: /^Send telemetry to/ }))
    expect(screen.queryByRole('option', { name: 'Jaeger (traces only)' })).not.toBeInTheDocument()
    expect(screen.getByRole('option', { name: 'Honeycomb' })).toBeInTheDocument()
  })

  test('a backend already compatible with everything that is on stays pickable, with no warning', async () => {
    const user = userEvent.setup()
    // Jaeger was already picked (traces only, nothing else on yet) - picking it is fine here.
    render(<Wrapper initial={{ ...emptyTelemetry, traces: true }} />)
    await user.click(screen.getByRole('combobox', { name: /^Send telemetry to/ }))
    await user.click(screen.getByRole('option', { name: 'Jaeger (traces only)' }))
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
  })

  test('turning on a second modality after Jaeger was already the destination warns instead of silently keeping it', async () => {
    const user = userEvent.setup()
    render(<Wrapper initial={{ ...emptyTelemetry, traces: true, exportEndpoint: 'jaeger-collector.observability:4317', exportProtocol: 'grpc' }} />)
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
    await user.click(screen.getByTestId('telemetry-resourceUsage'))
    expect(screen.getByRole('alert')).toHaveTextContent('Jaeger (traces only) only carries traces')
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
  /** Walks the navigable wizard from its landing step through layer + modality. Every application modality
   *  is a 1:1 match with its only signal (see GuidedWizard.tsx), so picking one turns that signal on and
   *  skips straight past Kind to Scope or Review - callers landing on an infrastructure combination with
   *  more than one matching signal land on Kind instead, exactly as before. */
  async function gotoModality(user: ReturnType<typeof userEvent.setup>, layer: 'infrastructure' | 'application', modality: string) {
    await user.click(screen.getByTestId('telemetry-mode-guided'))
    await user.click(screen.getByTestId(`telemetry-guided-layer-${layer}`))
    await user.click(screen.getByTestId(`telemetry-guided-modality-${modality}`))
  }

  test('defaults to the flat grid, and switching modes does not lose what was already picked', async () => {
    const user = userEvent.setup()
    render(<Wrapper initial={{ ...emptyTelemetry, resourceUsage: true }} />)
    expect(screen.getByTestId('telemetry-mode-flat')).toHaveAttribute('aria-checked', 'true')
    expect(screen.getByTestId('telemetry-facet-scope')).toBeInTheDocument()
    expect(screen.getByTestId('telemetry-resourceUsage')).toBeChecked()

    await user.click(screen.getByTestId('telemetry-mode-guided'))
    expect(screen.getByTestId('telemetry-mode-guided')).toHaveAttribute('aria-checked', 'true')
    expect(screen.queryByTestId('telemetry-facet-scope')).not.toBeInTheDocument()
    // Lands on the first step (Layer), not the signal itself - but walking to where resourceUsage lives
    // (infrastructure / metrics) shows it's still checked, exactly as the flat grid left it. It also shows
    // up immediately as a chip, before even reaching the Kind step that owns its checkbox.
    expect(screen.getByTestId('telemetry-guided-steps')).toBeInTheDocument()
    expect(screen.getByTestId('telemetry-guided-chip-resourceUsage')).toBeInTheDocument()
    await user.click(screen.getByTestId('telemetry-guided-layer-infrastructure'))
    await user.click(screen.getByTestId('telemetry-guided-modality-metrics'))
    expect(screen.getByTestId('telemetry-resourceUsage')).toBeChecked()
  })

  test('an application modality is a 1:1 match with its only signal, so picking it turns that signal on and skips straight to Scope', async () => {
    const user = userEvent.setup()
    render(<Wrapper />)
    await gotoModality(user, 'application', 'metrics')
    expect(screen.queryByTestId('telemetry-guided-step-kind')).not.toBeInTheDocument()
    expect(screen.getByTestId('telemetry-guided-step-scope')).toBeInTheDocument()
    expect(screen.getByTestId('telemetry-guided-add-scope')).toBeInTheDocument()
    expect(screen.getByTestId('telemetry-guided-attach-applicationMetrics')).toBeInTheDocument()
    expect(screen.getByTestId('telemetry-guided-chip-applicationMetrics')).toBeInTheDocument()

    // The flat grid shows the same signal actually turned on, not just implied by the chip.
    await user.click(screen.getByTestId('telemetry-mode-flat'))
    expect(screen.getByTestId('telemetry-applicationMetrics')).toBeChecked()
  })

  test('with nothing application-scoped, Continue skips straight to the destination step, then on to review', async () => {
    const user = userEvent.setup()
    render(<Wrapper />)
    await gotoModality(user, 'infrastructure', 'metrics')
    await user.click(screen.getByTestId('telemetry-resourceUsage'))
    await user.click(screen.getByTestId('telemetry-guided-continue'))
    expect(screen.queryByTestId('telemetry-guided-step-scope')).not.toBeInTheDocument()
    expect(screen.getByTestId('telemetry-guided-step-destination')).toBeInTheDocument()
    await user.click(screen.getByTestId('telemetry-guided-continue'))
    expect(screen.getByTestId('telemetry-guided-step-review')).toBeInTheDocument()
    expect(screen.getByTestId('telemetry-review-pipeline')).toHaveTextContent('Resource usage')
  })

  test('a chip\'s remove button turns that signal off directly, without navigating back to where it was picked', async () => {
    const user = userEvent.setup()
    render(<Wrapper />)
    await gotoModality(user, 'infrastructure', 'metrics')
    await user.click(screen.getByTestId('telemetry-resourceUsage'))
    // The chip strip tracks the pick live, right there on the same Kind screen it was made on - not just
    // once Review is reached.
    expect(screen.getByTestId('telemetry-guided-chip-resourceUsage')).toBeInTheDocument()
    await user.click(screen.getByTestId('telemetry-guided-chip-resourceUsage-remove'))
    expect(screen.queryByTestId('telemetry-guided-chip-resourceUsage')).not.toBeInTheDocument()
    expect(screen.getByTestId('telemetry-resourceUsage')).not.toBeChecked()
  })

  test('removing the only application-scoped signal while its scope step is showing falls back to destination, not a pointless scope screen', async () => {
    const user = userEvent.setup()
    render(<Wrapper />)
    await gotoModality(user, 'application', 'metrics')
    expect(screen.getByTestId('telemetry-guided-step-scope')).toBeInTheDocument()
    await user.click(screen.getByTestId('telemetry-guided-chip-applicationMetrics-remove'))
    expect(screen.queryByTestId('telemetry-guided-step-scope')).not.toBeInTheDocument()
    expect(screen.getByTestId('telemetry-guided-step-destination')).toBeInTheDocument()
  })

  test('"Add another" loops back to the layer step, accumulating into one draft with a visible, removable chip for each pick', async () => {
    const user = userEvent.setup()
    render(<Wrapper />)
    await gotoModality(user, 'infrastructure', 'metrics')
    await user.click(screen.getByTestId('telemetry-resourceUsage'))
    await user.click(screen.getByTestId('telemetry-guided-add-another'))
    expect(screen.getByTestId('telemetry-guided-step-layer')).toBeInTheDocument()
    // What was picked on the first pass stays visible (and removable) while building the second.
    expect(screen.getByTestId('telemetry-guided-chip-resourceUsage')).toBeInTheDocument()

    await user.click(screen.getByTestId('telemetry-guided-layer-application'))
    await user.click(screen.getByTestId('telemetry-guided-modality-logs'))
    // application/logs is also a 1:1 match, landing straight on its scope step.
    expect(screen.getByTestId('telemetry-guided-step-scope')).toBeInTheDocument()
    await user.click(screen.getByTestId('telemetry-guided-continue'))
    expect(screen.getByTestId('telemetry-guided-step-destination')).toBeInTheDocument()
    await user.click(screen.getByTestId('telemetry-guided-continue'))
    expect(screen.getByTestId('telemetry-review-pipeline')).toHaveTextContent('Resource usage')
    expect(screen.getByTestId('telemetry-review-pipeline')).toHaveTextContent('Application logs')

    // A third pass still accumulates rather than replacing - resourceUsage from the first pass is untouched.
    await user.click(screen.getByTestId('telemetry-guided-add-another'))
    await user.click(screen.getByTestId('telemetry-guided-layer-infrastructure'))
    await user.click(screen.getByTestId('telemetry-guided-modality-metrics'))
    expect(screen.getByTestId('telemetry-resourceUsage')).toBeChecked()
  })

  test('naming a scope and attaching a signal to it copies its namespaces into that signal\'s override', async () => {
    const user = userEvent.setup()
    render(<Wrapper initial={{ ...emptyTelemetry, applicationMetrics: true }} />)
    await gotoModality(user, 'application', 'metrics')
    expect(screen.getByTestId('telemetry-guided-step-scope')).toBeInTheDocument()
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

  test('an initial scope (e.g. handed off from the topology canvas) starts the form in guided mode, straight on the scope step, with a draft pre-filled from it', () => {
    render(<Wrapper initial={{ ...emptyTelemetry, applicationMetrics: true }} initialScope={{ name: 'From topology', namespaces: ['payments', 'checkout'] }} />)
    expect(screen.getByTestId('telemetry-mode-guided')).toHaveAttribute('aria-checked', 'true')
    expect(screen.getByTestId('telemetry-guided-step-scope')).toBeInTheDocument()
    const nameInputs = screen.getAllByPlaceholderText('Name this scope') as HTMLInputElement[]
    const seeded = nameInputs.find((i) => i.value === 'From topology')
    expect(seeded).toBeTruthy()
    const draftBox = seeded!.closest('div[data-testid^="telemetry-guided-draft-"]') as HTMLElement
    expect(draftBox).toHaveTextContent('payments')
    expect(draftBox).toHaveTextContent('checkout')
  })

  test('two scopes that both include the same namespace warn, and merging keeps only one', async () => {
    const user = userEvent.setup()
    render(<Wrapper initial={{ ...emptyTelemetry, applicationMetrics: true, applicationLogs: true }} />)
    await gotoModality(user, 'application', 'metrics')
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

describe('TelemetryFields extra processors', () => {
  const openProcessing = async (user: ReturnType<typeof userEvent.setup>) => {
    await user.click(screen.getByText('Credentials & processing'))
  }

  test('starts with no extra processors, and adding one opens it pre-expanded for editing', async () => {
    const user = userEvent.setup()
    render(<Wrapper />)
    await openProcessing(user)
    expect(screen.getByText('No extra processors.')).toBeInTheDocument()
    await user.click(screen.getByTestId('telemetry-processor-add-filter'))
    expect(screen.queryByText('No extra processors.')).not.toBeInTheDocument()
    const rows = screen.getAllByTestId('telemetry-processor-row')
    expect(rows).toHaveLength(1)
    expect(within(rows[0]).getByPlaceholderText('drop_debug_logs')).toBeVisible()
  })

  test('an unnamed processor with no conditions is flagged; naming it and adding a condition clears the problem', async () => {
    const user = userEvent.setup()
    render(<Wrapper />)
    await openProcessing(user)
    await user.click(screen.getByTestId('telemetry-processor-add-filter'))
    expect(screen.getByTestId('telemetry-processor-problems')).toHaveTextContent('Every processor needs a name.')

    await user.type(screen.getByPlaceholderText('drop_debug_logs'), 'drop_debug')
    expect(screen.getByTestId('telemetry-processor-problems')).toHaveTextContent('has no conditions')

    await user.click(screen.getByRole('button', { name: 'Condition' }))
    await user.type(screen.getByPlaceholderText('attribute'), 'level')
    await user.type(screen.getByPlaceholderText('value'), 'debug')
    expect(screen.queryByTestId('telemetry-processor-problems')).not.toBeInTheDocument()
  })

  test('a raw JSON override hides the typed form and marks the row "raw"', async () => {
    const user = userEvent.setup()
    render(<Wrapper />)
    await openProcessing(user)
    await user.click(screen.getByTestId('telemetry-processor-add-filter'))
    expect(screen.getByText('Signal')).toBeInTheDocument()
    await user.click(screen.getByText('Raw JSON body instead'))
    await user.type(screen.getByLabelText('Raw processor JSON body'), '{{"error_mode":"ignore"}}')
    expect(screen.queryByText('Signal')).not.toBeInTheDocument()
    expect(screen.getByText('raw')).toBeInTheDocument()
  })

  test('the up/down buttons reorder processors, and the first row\'s up button is disabled', async () => {
    const user = userEvent.setup()
    render(<Wrapper />)
    await openProcessing(user)
    await user.click(screen.getByTestId('telemetry-processor-add-filter'))
    await user.type(screen.getByPlaceholderText('drop_debug_logs'), 'first')
    await user.click(screen.getByTestId('telemetry-processor-add-transform'))
    const nameInputs = screen.getAllByPlaceholderText('drop_debug_logs')
    await user.type(nameInputs[1], 'second')

    let rows = screen.getAllByTestId('telemetry-processor-row')
    expect(within(rows[0]).getByText('first')).toBeInTheDocument()
    expect(within(rows[1]).getByText('second')).toBeInTheDocument()
    expect(screen.getByLabelText('Move first up')).toBeDisabled()

    await user.click(screen.getByLabelText('Move second up'))
    rows = screen.getAllByTestId('telemetry-processor-row')
    expect(within(rows[0]).getByText('second')).toBeInTheDocument()
    expect(within(rows[1]).getByText('first')).toBeInTheDocument()
  })

  test('removing a processor drops its row', async () => {
    const user = userEvent.setup()
    render(<Wrapper />)
    await openProcessing(user)
    await user.click(screen.getByTestId('telemetry-processor-add-filter'))
    expect(screen.getAllByTestId('telemetry-processor-row')).toHaveLength(1)
    await user.click(screen.getByLabelText('Remove Filter'))
    expect(screen.queryAllByTestId('telemetry-processor-row')).toHaveLength(0)
    expect(screen.getByText('No extra processors.')).toBeInTheDocument()
  })
})
