import { render, screen } from '@testing-library/react'
import { describe, expect, test } from 'vitest'
import { PhoneList } from '@/components/topology/TelemetryParts'
import { layoutLanes } from '@/lib/telemetryLanes'
import type { PlatformEntity, PlatformModel } from '@/lib/platformLayer'

const part = (id: string, kind: PlatformEntity['kind'], over: Partial<PlatformEntity> = {}): PlatformEntity => ({ id, kind, name: kind, detail: '', status: 'healthy', sentence: '', sendsTo: [], ...over })
const hop = (from: string, to: string, status: PlatformEntity['status'], age?: string) => ({ id: `${from}>${to}`, from, to, status, age })

describe('PhoneList', () => {
  test('draws the line between a cluster\'s agent and its local operator, and says how long ago data crossed it where something is wrong', () => {
    const model: PlatformModel = {
      entities: [
        part('agent:a', 'agent', { clusterId: 'a', clusterName: 'apple', agentId: 'a' }),
        part('local:a', 'local', { clusterId: 'a', clusterName: 'apple', agentId: 'a' }),
        part('agent:b', 'agent', { clusterId: 'b', clusterName: 'banana', agentId: 'b', status: 'down' }),
        part('local:b', 'local', { clusterId: 'b', clusterName: 'banana', agentId: 'b' }),
      ],
      edges: [hop('agent:a', 'local:a', 'healthy', '20 s'), hop('agent:b', 'local:b', 'down', '2 h')],
    }
    const layout = layoutLanes(model, {})
    render(<PhoneList layout={layout} facts={new Map()} onSelect={() => {}} />)
    const links = screen.getAllByTestId('list-hop')
    expect(links.map((l) => l.getAttribute('data-status'))).toEqual(['healthy', 'down'])
    expect(links[0]).toHaveTextContent('')
    expect(links[1]).toHaveTextContent('2 h')
  })
})
