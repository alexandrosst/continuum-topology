import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, test, vi } from 'vitest'
import TelemetryBackendWizard from '@/components/telemetry/TelemetryBackendWizard'
import type { QuickStartBackend } from '@/lib/history'
import { newProcessorEntry } from '@/lib/processorCatalog'

// TelemetryBackendWizard is the guided path to a quick-start backend: pick a kind (from the org's
// allow-list), set its namespace/retention, then see what - if anything - looks like it won't play well
// with what's already configured before getting the install command. It never deploys or dials anything
// itself; every assertion below is about what renders and what gets handed to `onSave`.

const baseProps = {
  open: true,
  onClose: vi.fn(),
  allowedKinds: ['jaeger', 'zipkin', 'prometheus', 'loki'] as const,
  enabledModalities: new Set<'traces' | 'metrics' | 'logs'>(['traces', 'metrics', 'logs']),
  existingBackends: [] as QuickStartBackend[],
  currentEndpoint: '',
  currentProtocol: 'grpc' as const,
  extraProcessors: [],
  admin: true,
  busy: false,
  error: null,
  onSave: vi.fn(),
}

describe('TelemetryBackendWizard', () => {
  test('only offers kinds in the allow-list', () => {
    render(<TelemetryBackendWizard {...baseProps} allowedKinds={['jaeger']} />)
    expect(screen.getByTestId('backend-wizard-kind-jaeger')).toBeInTheDocument()
    expect(screen.queryByTestId('backend-wizard-kind-prometheus')).not.toBeInTheDocument()
    expect(screen.queryByTestId('backend-wizard-kind-loki')).not.toBeInTheDocument()
    expect(screen.queryByTestId('backend-wizard-kind-custom')).not.toBeInTheDocument()
  })

  test('picking a kind seeds its default namespace and retention on the Details step', async () => {
    const user = userEvent.setup()
    render(<TelemetryBackendWizard {...baseProps} />)
    await user.click(screen.getByTestId('backend-wizard-kind-loki'))
    expect(screen.getByTestId('backend-wizard-namespace')).toHaveValue('observability')
    expect(screen.getByTestId('backend-wizard-retention')).toHaveValue('168h')
  })

  test('Zipkin takes a span count, and its install command deploys Zipkin alone', async () => {
    const user = userEvent.setup()
    render(<TelemetryBackendWizard {...baseProps} />)
    await user.click(screen.getByTestId('backend-wizard-kind-zipkin'))
    expect(screen.getByTestId('backend-wizard-retention')).toHaveValue('500000')
    await user.click(screen.getByTestId('backend-wizard-continue'))
    expect(screen.getByTestId('backend-wizard-step-review')).toHaveTextContent('openzipkin/zipkin-slim')
    expect(screen.getByTestId('backend-wizard-step-review')).not.toHaveTextContent('zipkin-quickstart-otlp')
  })

  test('a non-administrator sees nothing editable on Details, and no way to continue', async () => {
    const user = userEvent.setup()
    render(<TelemetryBackendWizard {...baseProps} admin={false} />)
    await user.click(screen.getByTestId('backend-wizard-kind-jaeger'))
    expect(screen.getByText('Only administrators can set this up.')).toBeInTheDocument()
    expect(screen.queryByTestId('backend-wizard-continue')).not.toBeInTheDocument()
  })

  test('Review warns when the matching modality is not turned on anywhere yet', async () => {
    const user = userEvent.setup()
    render(<TelemetryBackendWizard {...baseProps} enabledModalities={new Set(['metrics', 'logs'])} />)
    await user.click(screen.getByTestId('backend-wizard-kind-jaeger'))
    await user.click(screen.getByTestId('backend-wizard-continue'))
    expect(screen.getByTestId('backend-wizard-step-review')).toBeInTheDocument()
    expect(screen.getByText(/No traces signal is turned on/)).toBeInTheDocument()
  })

  test('Review flags an existing destination as something "Use as destination" would replace', async () => {
    const user = userEvent.setup()
    render(<TelemetryBackendWizard {...baseProps} currentEndpoint="otel-gw.example.com:4317" currentProtocol="grpc" />)
    await user.click(screen.getByTestId('backend-wizard-kind-loki'))
    await user.click(screen.getByTestId('backend-wizard-continue'))
    expect(screen.getByText(/A destination is already set \(otel-gw\.example\.com:4317\)/)).toBeInTheDocument()
    // Loki is OTLP/HTTP; the current destination is gRPC - worth a protocol note too.
    expect(screen.getByText(/expects OTLP\/HTTP/)).toBeInTheDocument()
  })

  test('Review flags a trace-only extra processor against a metrics-only backend', async () => {
    const user = userEvent.setup()
    const tailSampling = { ...newProcessorEntry('tailSampling'), name: 'keep_errors' }
    render(<TelemetryBackendWizard {...baseProps} extraProcessors={[tailSampling]} />)
    await user.click(screen.getByTestId('backend-wizard-kind-prometheus'))
    await user.click(screen.getByTestId('backend-wizard-continue'))
    expect(screen.getByText(/"keep_errors" processor only applies to traces/)).toBeInTheDocument()
  })

  test('a duplicate of the same kind blocks the install command and offers no save', async () => {
    const user = userEvent.setup()
    const existing: QuickStartBackend = { id: 'qsb-1', kind: 'jaeger', modality: 'traces', namespace: 'obs-old', retention: '72h', label: 'Jaeger (traces)' }
    render(<TelemetryBackendWizard {...baseProps} existingBackends={[existing]} />)
    await user.click(screen.getByTestId('backend-wizard-kind-jaeger'))
    await user.click(screen.getByTestId('backend-wizard-continue'))
    expect(screen.getByTestId('backend-wizard-duplicate')).toHaveTextContent('obs-old')
    expect(screen.queryByTestId('backend-wizard-save')).not.toBeInTheDocument()
  })

  test('saving a built-in kind hands onSave the expected record', async () => {
    const user = userEvent.setup()
    const onSave = vi.fn()
    render(<TelemetryBackendWizard {...baseProps} onSave={onSave} />)
    await user.click(screen.getByTestId('backend-wizard-kind-prometheus'))
    await user.click(screen.getByTestId('backend-wizard-continue'))
    await user.click(screen.getByTestId('backend-wizard-save'))
    expect(onSave).toHaveBeenCalledWith({ id: '', kind: 'prometheus', modality: 'metrics', namespace: 'observability', retention: '15d', label: 'Prometheus (metrics)' })
  })

  test('the custom kind collects a name/modality/namespace/URL instead of a retention default, and needs no install command', async () => {
    const user = userEvent.setup()
    const onSave = vi.fn()
    render(<TelemetryBackendWizard {...baseProps} allowedKinds={['jaeger', 'prometheus', 'loki', 'custom']} onSave={onSave} />)
    await user.click(screen.getByTestId('backend-wizard-kind-custom'))
    expect(screen.getByTestId('backend-wizard-continue')).toBeDisabled()
    await user.type(screen.getByTestId('backend-wizard-custom-label'), 'Elastic APM')
    await user.type(screen.getByTestId('backend-wizard-custom-url'), 'https://apm.example.com')
    expect(screen.getByTestId('backend-wizard-continue')).not.toBeDisabled()
    await user.click(screen.getByTestId('backend-wizard-continue'))
    expect(screen.getByText(/Not from this app's own catalog/)).toBeInTheDocument()
    await user.click(screen.getByTestId('backend-wizard-save'))
    expect(onSave).toHaveBeenCalledWith({ id: '', kind: 'custom', modality: 'traces', namespace: 'observability', retention: 'n/a', toolUrl: 'https://apm.example.com', label: 'Elastic APM' })
  })
})
