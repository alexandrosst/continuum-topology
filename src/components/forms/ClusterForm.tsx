import { useState } from 'react'
import { ComboField, EvidenceChip, Field, Input, LabelsEditor, Modal, Select } from '@/components/ui/primitives'
import { hasOverrides } from '@/lib/effective'
import { CNI_OPTIONS, DISTRIBUTION_OPTIONS, PROVIDER_OPTIONS } from '@/lib/present'
import { useWeakValue } from '@/store/rowEvidence'
import { uid, useTopology } from '@/store/topology'
import { useServer } from '@/store/server'
import { DEFAULT_ORG, TIERS, type Cluster } from '@/lib/types'
import { StatusSelect, TrustSelect, FormFooter, DiscoveredNote } from './shared'

/* ---------- Cluster ---------- */
export function ClusterForm({ initial, onClose }: { initial: Cluster | null; onClose: () => void }) {
  const save = useTopology((s) => s.saveCluster)
  const resetOverrides = useTopology((s) => s.resetOverrides)
  const sites = useTopology((s) => s.sites)
  const by = useServer((s) => s.user?.username)
  const [f, setF] = useState<Cluster>(
    initial ?? {
      id: uid('cl'),
      orgId: DEFAULT_ORG,
      name: '',
      tier: 'cloud',
      distribution: 'kubeadm',
      version: '',
      provider: '',
      region: '',
      status: 'unknown',
      labels: {},
      source: 'manual',
    },
  )
  const [labels, setLabels] = useState(f.labels)
  const set = <K extends keyof Cluster>(k: K, v: Cluster[K]) => setF((p) => ({ ...p, [k]: v }))
  // Same "is this field a guess, or not known at all" signal the Clusters table already shows per row (see
  // NodesPage/ClustersPage's own `chip` helper) - reused here so editing a discovered value shows exactly the
  // confidence a person would already have seen before opening this form, instead of a plain, unqualified input.
  const weak = useWeakValue('cluster')
  const confirmField = useTopology((s) => s.confirmField)
  // A "guess" already has a real value showing below, so confirming it is one click - freeze it as a human
  // override with no value change (see `confirmOverride`). The exact affordance the Inspector's "Guessed or not
  // known" list already offers (see WeakValues), reused here so an edit form gives the same choice on the spot.
  // An "unknown" has no value to freeze - it is resolved the ordinary way, by typing one into the field below.
  const chip = (attr: string, field?: string) => {
    const w = weak(f as never, attr, field)
    if (!w) return undefined
    return (
      <span className="inline-flex items-center gap-1.5">
        <EvidenceChip level={w.level} why={w.why} />
        {w.level === 'guess' && initial && by && (
          <button
            onClick={() => confirmField('cluster', initial.id, field ?? attr, by)}
            className="text-[11px] text-accent hover:underline"
            title="Keep this value as it is; it will not be overwritten by rediscovery."
          >
            confirm
          </button>
        )}
      </span>
    )
  }

  return (
    <Modal
      open
      onClose={onClose}
      title={initial ? 'Edit cluster' : 'Add cluster'}
      description="A Kubernetes cluster and where it sits on the cloud–edge continuum."
      footer={
        <FormFooter
          isNew={!initial}
          disabled={!f.name.trim()}
          onCancel={onClose}
          onSave={() => {
            save({ ...f, name: f.name.trim(), labels }, by)
            onClose()
          }}
          onReset={
            initial && hasOverrides(initial)
              ? () => {
                  resetOverrides('cluster', initial.id)
                  onClose()
                }
              : undefined
          }
        />
      }
    >
      <DiscoveredNote entity={initial} />
      <div className="grid grid-cols-2 gap-4">
        <Field label="Name" className="col-span-2">
          <Input autoFocus value={f.name} onChange={(e) => set('name', e.target.value)} placeholder="e.g. edge-patras" />
        </Field>
        <Field label="Tier" hint="Drives vertical placement in the topology." adornment={chip('tier')}>
          <Select value={f.tier} onChange={(e) => set('tier', e.target.value as Cluster['tier'])}>
            {TIERS.map((t) => (
              <option key={t.value} value={t.value}>
                {t.label}
              </option>
            ))}
          </Select>
        </Field>
        <Field label="Status">
          <StatusSelect value={f.status} onChange={(v) => set('status', v)} />
        </Field>
        <Field label="Distribution" adornment={chip('distribution')}>
          <ComboField value={f.distribution} onChange={(v) => set('distribution', v)} options={DISTRIBUTION_OPTIONS} placeholder="EKS, k3s, kubeadm…" />
        </Field>
        <Field label="Version" adornment={chip('version')}>
          <Input value={f.version} onChange={(e) => set('version', e.target.value)} placeholder="v1.30.2" />
        </Field>
        <Field label="Provider" adornment={chip('provider')}>
          <ComboField value={f.provider} onChange={(v) => set('provider', v)} options={PROVIDER_OPTIONS} placeholder="AWS, On-prem…" />
        </Field>
        <Field label="Region label" hint="A cloud region code or a city name. If the cluster is not on a site yet, it is used to suggest where it is." adornment={chip('region')}>
          <Input value={f.region} onChange={(e) => set('region', e.target.value)} placeholder="eu-central-1" />
        </Field>
        <Field label="Site" hint="Where it sits on the map. Manage sites under Sites." className="col-span-2">
          <Select value={f.siteId ?? ''} onChange={(e) => set('siteId', e.target.value || undefined)}>
            <option value="">No site</option>
            {sites.map((x) => (
              <option key={x.id} value={x.id}>
                {x.name}
              </option>
            ))}
          </Select>
        </Field>
        <Field label="CNI">
          <ComboField value={f.cni ?? ''} onChange={(v) => set('cni', v || undefined)} options={CNI_OPTIONS} placeholder="calico, flannel, cilium…" />
        </Field>
        <Field label="Trust zone" hint="Policy input for placement.">
          <TrustSelect value={f.trustZone} onChange={(v) => set('trustZone', v)} />
        </Field>
        <Field label="Data residency" hint="Jurisdiction data must stay in, e.g. EU." className="col-span-2">
          <Input value={f.dataResidency ?? ''} onChange={(e) => set('dataResidency', e.target.value || undefined)} placeholder="EU" />
        </Field>
        <Field label="Labels" className="col-span-2">
          <LabelsEditor value={labels} onChange={setLabels} keyPlaceholder="env" valuePlaceholder="prod" />
        </Field>
      </div>
    </Modal>
  )
}
