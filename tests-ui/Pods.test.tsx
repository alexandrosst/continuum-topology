import { fireEvent, render, screen, within } from '@testing-library/react'
import { ReactFlowProvider } from '@xyflow/react'
import { describe, expect, test, vi } from 'vitest'
import { PodPopover, PodRail } from '@/components/topology/Pods'
import { buildPodsView, podLabel, podState, RAIL_LIMIT } from '@/lib/pods'
import type { MachineNode, Pod, Service } from '@/lib/types'

const NOW = Date.parse('2026-01-10T12:00:00Z')
const ago = (min: number) => new Date(NOW - min * 60_000).toISOString()
const nodes = new Map([
  ['n1', { id: 'n1', name: 'worker-1' }],
  ['n2', { id: 'n2', name: 'worker-2' }],
] as unknown as [string, MachineNode][])
const services = new Map([['svc-db', { id: 'svc-db', name: 'database' }]] as unknown as [string, Service][])
const pod = (name: string, over: Partial<Pod> = {}): Pod => ({ name, nodeId: 'n1', phase: 'Running', ready: true, createdAt: ago(60 * 24 * 3), ...over })
const view = (pods: Pod[]) => buildPodsView(pods, 'api', nodes, services, NOW)!

describe('podState and podLabel', () => {
  test('a crash loop outranks ready; not ready without a node is unscheduled, with one it is a warning', () => {
    expect(podState(pod('a'))).toBe('ready')
    expect(podState(pod('a', { restarts: 2 }))).toBe('ready')
    expect(podState(pod('a', { restarts: 3 }))).toBe('bad')
    expect(podState(pod('a', { restarts: 120, ready: true }))).toBe('bad')
    expect(podState(pod('a', { ready: false }))).toBe('warn')
    expect(podState(pod('a', { ready: false, nodeId: undefined, phase: 'Pending' }))).toBe('unscheduled')
  })

  test('the service prefix is dropped when something readable is left, else the whole name stays', () => {
    expect(podLabel('api-6d9f7b8c4-5c1x', 'api')).toBe('6d9f7b8c4-5c1x')
    expect(podLabel('postgres-0', 'postgres')).toBe('postgres-0')
    expect(podLabel('other-name-xyz', 'api')).toBe('other-name-xyz')
  })
})

describe('buildPodsView', () => {
  test('nothing without pods', () => {
    expect(buildPodsView(undefined, 'api', nodes, services)).toBeUndefined()
    expect(buildPodsView([], 'api', nodes, services)).toBeUndefined()
  })

  test('counts: ready out of total, not-ready and crash-looping apart, real nodes only', () => {
    const v = view([pod('api-a'), pod('api-b', { nodeId: 'n2' }), pod('api-c', { ready: false }), pod('api-d', { restarts: 7 }), pod('api-e', { ready: false, nodeId: undefined, phase: 'Pending' })])
    expect(v).toMatchObject({ total: 5, ready: 3, warn: 2, bad: 1, nodes: 2 })
  })

  test('the rail caps at RAIL_LIMIT, keeps every pod that needs attention, and stays in node-then-name order', () => {
    const healthy = Array.from({ length: 30 }, (_, i) => pod(`api-h${String(i).padStart(2, '0')}`))
    const v = view([...healthy, pod('api-zz-sick', { ready: false }), pod('api-zz-crash', { restarts: 9 }), pod('api-zz-new', { createdAt: ago(1) })])
    expect(v.rail).toHaveLength(RAIL_LIMIT)
    expect(v.overflow).toBe(33 - RAIL_LIMIT)
    const ids = v.rail.map((r) => r.id)
    expect(ids).toEqual(expect.arrayContaining(['api-zz-sick', 'api-zz-crash', 'api-zz-new']))
    expect(ids).toEqual([...ids].sort((a, b) => a.localeCompare(b)))
    expect(v.rail.find((r) => r.id === 'api-zz-new')?.recent).toBe(true)
  })

  test('a pod changing colour does not move its cell', () => {
    const base = [pod('api-a'), pod('api-b'), pod('api-c')]
    const order = (v: ReturnType<typeof view>) => v.rail.map((r) => r.id)
    expect(order(view(base.map((p) => (p.name === 'api-b' ? { ...p, ready: false } : p))))).toEqual(order(view(base)))
  })

  test('groups by node, worst first then healthy by name; "not scheduled" is its own group; traffic peers get names', () => {
    const v = view([
      pod('api-1', { traffic: [{ peer: 'svc-db', peerKind: 'service', direction: 'out', port: 5432, protocol: 'tcp', connections: 2 }, { peer: '1.2.3.4', peerKind: 'external', direction: 'out', port: 443, protocol: 'tcp', connections: 1 }] }),
      pod('api-2', { restarts: 4 }),
      pod('api-3', { ready: false }),
      pod('api-4', { nodeId: 'n2' }),
      pod('api-5', { nodeId: undefined, ready: false, phase: 'Pending' }),
    ])
    expect(v.groups.map((g) => g.nodeName)).toEqual(['not scheduled', 'worker-1', 'worker-2'])
    expect(v.groups[1].pods.map((p) => p.id)).toEqual(['api-2', 'api-3', 'api-1'])
    expect(v.groups[1].pods.find((p) => p.id === 'api-1')?.traffic?.map((t) => t.peer)).toEqual(['database', '1.2.3.4'])
    expect(v.groups[1].pods[0]).toMatchObject({ state: 'bad', restarts: 4, label: 'api-2' })
  })

  test('the hover text and the row carry name, phase, age and restarts', () => {
    const p = view([pod('api-6d9f-x1', { ready: false, phase: 'Pending', restarts: 1, createdAt: ago(90) })]).rail[0]
    expect(p.title).toBe('api-6d9f-x1 · Pending, not ready · 1 hour old · 1 restart')
    expect(p).toMatchObject({ label: '6d9f-x1', phase: 'Pending', age: '1h' })
  })
})

describe('PodRail and PodPopover', () => {
  const sample = view([
    pod('api-aaaa-1'),
    pod('api-aaaa-2', { restarts: 5 }),
    pod('api-aaaa-3', { ready: false }),
    ...Array.from({ length: 6 }, (_, i) => pod(`api-bbbb-${i}`)),
    pod('api-cccc-1', { nodeId: 'n2', createdAt: ago(1), traffic: [{ peer: 'svc-db', peerKind: 'service', direction: 'out', port: 5432, protocol: 'tcp', connections: 2 }] }),
  ])

  function Harness({ onClose = () => {} }: { onClose?: () => void }) {
    const anchor = document.body.appendChild(document.createElement('button'))
    return (
      <ReactFlowProvider>
        <PodPopover pods={sample} name="api" anchor={anchor} onClose={onClose} />
      </ReactFlowProvider>
    )
  }

  test('the rail is one button with the summary, an accessible name, and the expanded state', () => {
    const onToggle = vi.fn()
    render(<PodRail pods={sample} far={false} open={false} onToggle={onToggle} buttonRef={null} />)
    const btn = screen.getByRole('button', { name: '9 of 10 pods ready, 1 crash-looping. Show the pods' })
    expect(btn).toHaveAttribute('aria-expanded', 'false')
    expect(btn.textContent).toContain('9/10 ready')
    fireEvent.click(btn)
    expect(onToggle).toHaveBeenCalledOnce()
  })

  test('the popover opens as a named dialog on the body with the summary, node groups and "+N more healthy"', () => {
    render(<Harness />)
    const dialog = screen.getByRole('dialog', { name: 'Pods of api' })
    expect(dialog.parentElement).toBe(document.body)
    expect(dialog.textContent).toContain('9/10 ready · 2 nodes')
    expect(dialog.textContent).toContain('1 not ready')
    expect(dialog.textContent).toContain('1 crash-looping')
    expect(within(dialog).getAllByTestId('pod-row').length).toBeLessThan(10)
    expect(dialog.textContent).toMatch(/\+\d+ more healthy/)
    fireEvent.click(within(dialog).getByText(/more healthy/))
    expect(within(dialog).getAllByTestId('pod-row')).toHaveLength(10)
  })

  test('a pod with traffic opens its breakdown; Escape closes', () => {
    const onClose = vi.fn()
    render(<Harness onClose={onClose} />)
    const row = screen.getAllByTestId('pod-row').find((r) => r.textContent?.includes('cccc-1'))!
    fireEvent.click(row)
    expect(screen.getByTestId('pod-traffic').textContent).toContain('database')
    fireEvent.keyDown(document, { key: 'Escape' })
    expect(onClose).toHaveBeenCalledOnce()
  })

  test('a click elsewhere closes it', () => {
    const onClose = vi.fn()
    render(<Harness onClose={onClose} />)
    fireEvent.pointerDown(document.body)
    expect(onClose).toHaveBeenCalledOnce()
  })
})
