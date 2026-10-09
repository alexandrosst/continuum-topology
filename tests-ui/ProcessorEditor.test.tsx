import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState } from 'react'
import { describe, expect, test } from 'vitest'
import ProcessOptions from '@/components/telemetry/ProcessOptions'
import { emptyTelemetry, type TelemetryInput } from '@/lib/install'

// The extra processors a person can add behind "Extra processors" in the setup's tags/masking/sampling section: filters, transforms and a
// raw override, each checked as it is typed. The editor's state is plain draft state, so it is exercised through its only home.

function Wrapper() {
  const [value, setValue] = useState<TelemetryInput>({ ...emptyTelemetry, resourceUsage: true })
  return <ProcessOptions value={value} onChange={setValue} testIdPrefix="t" />
}

const open = async (user: ReturnType<typeof userEvent.setup>) => {
  await user.click(screen.getByText(/^Extra processors/))
}

describe('extra processors', () => {
  test('start empty, and adding one opens it pre-expanded for editing', async () => {
    const user = userEvent.setup()
    render(<Wrapper />)
    await open(user)
    expect(screen.getByText('No extra processors.')).toBeInTheDocument()
    await user.click(screen.getByTestId('t-processor-add-filter'))
    expect(screen.queryByText('No extra processors.')).not.toBeInTheDocument()
    const rows = screen.getAllByTestId('t-processor-row')
    expect(rows).toHaveLength(1)
    expect(within(rows[0]).getByPlaceholderText('drop_debug_logs')).toBeVisible()
  })

  test('an unnamed processor with no conditions is flagged; naming it and adding a condition clears the problem', async () => {
    const user = userEvent.setup()
    render(<Wrapper />)
    await open(user)
    await user.click(screen.getByTestId('t-processor-add-filter'))
    expect(screen.getByTestId('t-processor-problems')).toHaveTextContent('Every processor needs a name.')
    await user.type(screen.getByPlaceholderText('drop_debug_logs'), 'drop_debug')
    expect(screen.getByTestId('t-processor-problems')).toHaveTextContent('has no conditions')
    await user.click(screen.getByRole('button', { name: 'Condition' }))
    await user.type(screen.getByPlaceholderText('attribute'), 'level')
    await user.type(screen.getByPlaceholderText('value'), 'debug')
    expect(screen.queryByTestId('t-processor-problems')).not.toBeInTheDocument()
  })

  test('a filter condition says whether its attribute is the record\'s or the resource\'s, and an empty attribute name is flagged', async () => {
    const user = userEvent.setup()
    render(<Wrapper />)
    await open(user)
    await user.click(screen.getByTestId('t-processor-add-filter'))
    await user.type(screen.getByPlaceholderText('drop_debug_logs'), 'drop_ns')
    await user.click(screen.getByRole('button', { name: 'Condition' }))
    expect(screen.getByTestId('t-processor-problems')).toHaveTextContent('no attribute name')
    const level = screen.getByLabelText('Where the attribute is')
    expect(level).toHaveTextContent('Data point')
    await user.click(level)
    await user.click(screen.getByRole('option', { name: 'Resource' }))
    expect(level).toHaveTextContent('Resource')
    await user.type(screen.getByPlaceholderText('attribute'), 'k8s.namespace.name')
    await user.type(screen.getByPlaceholderText('value'), 'kube-system')
    expect(screen.queryByTestId('t-processor-problems')).not.toBeInTheDocument()
  })

  test('a raw JSON override hides the typed form and marks the row "raw"', async () => {
    const user = userEvent.setup()
    render(<Wrapper />)
    await open(user)
    await user.click(screen.getByTestId('t-processor-add-filter'))
    expect(screen.getByText('Signal')).toBeInTheDocument()
    await user.click(screen.getByText('Raw JSON body instead'))
    await user.type(screen.getByLabelText('Raw processor JSON body'), '{{"error_mode":"ignore"}}')
    expect(screen.queryByText('Signal')).not.toBeInTheDocument()
    expect(screen.getByText('raw')).toBeInTheDocument()
  })

  test('the up/down buttons reorder processors, and the first row\'s up button is disabled', async () => {
    const user = userEvent.setup()
    render(<Wrapper />)
    await open(user)
    await user.click(screen.getByTestId('t-processor-add-filter'))
    await user.type(screen.getByPlaceholderText('drop_debug_logs'), 'first')
    await user.click(screen.getByTestId('t-processor-add-transform'))
    await user.type(screen.getAllByPlaceholderText('drop_debug_logs')[1], 'second')
    let rows = screen.getAllByTestId('t-processor-row')
    expect(within(rows[0]).getByText('first')).toBeInTheDocument()
    expect(within(rows[1]).getByText('second')).toBeInTheDocument()
    expect(screen.getByLabelText('Move first up')).toBeDisabled()
    await user.click(screen.getByLabelText('Move second up'))
    rows = screen.getAllByTestId('t-processor-row')
    expect(within(rows[0]).getByText('second')).toBeInTheDocument()
    expect(within(rows[1]).getByText('first')).toBeInTheDocument()
  })

  test('removing a processor drops its row', async () => {
    const user = userEvent.setup()
    render(<Wrapper />)
    await open(user)
    await user.click(screen.getByTestId('t-processor-add-filter'))
    expect(screen.getAllByTestId('t-processor-row')).toHaveLength(1)
    await user.click(screen.getByLabelText('Remove Filter'))
    expect(screen.queryAllByTestId('t-processor-row')).toHaveLength(0)
    expect(screen.getByText('No extra processors.')).toBeInTheDocument()
  })
})
