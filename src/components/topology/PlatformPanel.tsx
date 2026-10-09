import { ArrowRight } from 'lucide-react'
import { Link } from 'react-router-dom'
import { PLATFORM_ICON, StatusGlyph } from '@/components/topology/PlatformNode'
import { LinkRow, Section } from '@/components/topology/InspectorParts'
import { DetailRow, ICON_SM, Pill } from '@/components/ui/primitives'
import { ago } from '@/lib/observed'
import { formatMemory } from '@/lib/present'
import { MODALITY_WORD, PLATFORM_STATUS_WORD, type PlatformEntity, type PlatformKind, type PlatformModel } from '@/lib/platformLayer'
import { useDiscoveryAgents } from '@/store/topology'
import type { Selection } from '@/components/topology/Inspector'

const KIND_WORD: Record<PlatformKind, string> = { agent: 'Discovery agent', local: 'Local operator', regional: 'Regional operator', central: 'Central operator', fusion: 'FUSION' }

/** Under the title: what kind of part it is, and its state as glyph and word. */
export function PlatformSubtitle({ part }: { part: PlatformEntity }) {
  const Icon = PLATFORM_ICON[part.kind]
  return (
    <span className="flex flex-wrap items-center gap-2">
      <Pill><Icon size={ICON_SM} aria-hidden /> {KIND_WORD[part.kind]}</Pill>
      <span className="inline-flex items-center gap-1.5 text-sm text-nb-300">
        <StatusGlyph status={part.status} />
        {PLATFORM_STATUS_WORD[part.status]}
      </span>
    </span>
  )
}

/**
 * The Inspector for a part of the platform layer: the same sections as any other entity - what it is, what state it is in and what
 * to do about it, what it sends to - and a link to the Pipeline, where it is set up and changed.
 */
export default function PlatformPanel({ part, platform, onSelect }: { part: PlatformEntity; platform: PlatformModel; onSelect: (s: Selection) => void }) {
  // The agent's own footprint, when it reported one: the history lives on System health, never repeated here.
  const self = useDiscoveryAgents().find((a) => a.clusterId === part.clusterId)?.self
  const onCanvas = new Set(platform.entities.map((e) => e.id))
  return (
    <>
      <Section title="State">
        <p className={part.status === 'attention' ? 'text-sm text-warn' : part.status === 'down' ? 'text-sm text-bad' : 'text-sm text-nb-300'}>{part.sentence}</p>
        {part.todo && <p className="mt-1.5 text-sm text-nb-400">{part.todo}</p>}
      </Section>
      <Section title="Details">
        <DetailRow label="Kind">{KIND_WORD[part.kind]}</DetailRow>
        {part.clusterName && <DetailRow label="Cluster">{part.clusterName}</DetailRow>}
        {part.version && <DetailRow label="Version">{part.version}</DetailRow>}
        {part.collecting && part.collecting.length > 0 && <DetailRow label="Collects">{part.collecting.map((m) => MODALITY_WORD[m]).join(', ')}</DetailRow>}
        {(part.lastDataAt || part.kind === 'local') && <DetailRow label="Last data">{part.lastDataAt ? ago(part.lastDataAt) : 'No data yet'}</DetailRow>}
        {part.lastHeartbeatAt && <DetailRow label="Last heartbeat">{ago(part.lastHeartbeatAt)}</DetailRow>}
        {self && (
          <>
            <DetailRow label="Memory (RSS)">{formatMemory(self.rssBytes / 1024 ** 3)}</DetailRow>
            <DetailRow label="Goroutines">{self.goroutines}</DetailRow>
          </>
        )}
      </Section>
      {part.kind !== 'fusion' && part.kind !== 'agent' && (
        <Section title="Sends to">
          {part.sendsTo.map((t) => (
            <LinkRow key={t.id} label={t.name} onClick={onCanvas.has(t.id) ? () => onSelect({ kind: 'platform', id: t.id }) : undefined} />
          ))}
          {part.elsewhere && <p className="px-2 py-1.5 text-sm text-nb-300">{part.elsewhere}<span className="text-nb-500"> (outside the platform)</span></p>}
          {part.sendsTo.length === 0 && !part.elsewhere && <p className="text-sm text-nb-500">Nothing yet.</p>}
        </Section>
      )}
      <div className="border-t border-nb-850 px-5 py-4">
        <Link to={part.kind === 'agent' ? '/discovery' : '/pipeline'} className="inline-flex items-center gap-1.5 text-sm text-accent hover:underline">
          {part.kind === 'agent' ? 'Open in Discovery' : 'Open in Pipeline'} <ArrowRight size={ICON_SM} aria-hidden />
        </Link>
        {self && (
          <div className="pt-2">
            <Link to="/system-health" className="inline-flex items-center gap-1.5 text-sm text-accent hover:underline">
              View full history <ArrowRight size={ICON_SM} aria-hidden />
            </Link>
          </div>
        )}
      </div>
    </>
  )
}
