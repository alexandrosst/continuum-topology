import clsx from 'clsx'
import { Check, CheckCircle2, ChevronRight, Loader2, Pin, X } from 'lucide-react'
import { useEffect, useMemo, useState, type ReactNode } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { Button, CopyButton, ErrorBanner, Field, InfoTip, Input, Modal } from '@/components/ui/primitives'
import TierLevels from '@/components/TierLevels'
import { api, ApiError, type CreatedToken } from '@/lib/api'
import { discoveryStatus, extrasOf } from '@/lib/consent'
import { previewImage } from '@/lib/image'
import { emptyScope, scopeActive, scopeProblems, splitNames, withFlowObserver, withMeasurements, withNodeProbe, withScope } from '@/lib/install'
import type { AccessTier } from '@/lib/types'
import { useServer } from '@/store/server'
import { useRawTopology } from '@/store/topology'
import ApprovalCard from './ApprovalCard'

/** Where Settings → Installation lives. Inside the wizard's token step it opens in a new tab, so the command (shown once) is not lost. */
function SettingsLink({ onNavigate, newTab }: { onNavigate?: () => void; newTab?: boolean }) {
  return newTab ? (
    <a className="text-accent hover:underline" href="/settings#installation" target="_blank" rel="noopener noreferrer">Settings → Installation</a>
  ) : (
    <Link className="text-accent hover:underline" to="/settings#installation" onClick={onNavigate}>Settings → Installation</Link>
  )
}

/** The honest notice for a server with no image registry set anywhere: the chart's own names are used, and nobody publishes them for you. */
function NoRegistryNotice({ onNavigate, newTab }: { onNavigate?: () => void; newTab?: boolean }) {
  return (
    <p className="rounded-md border border-amber-400/30 bg-amber-400/10 px-3 py-2 text-xs text-amber-200" role="note" data-testid="no-registry-notice">
      No image registry configured: the command below uses the chart’s built-in image names, which you must have published yourself. Set your registry in <SettingsLink onNavigate={onNavigate} newTab={newTab} />.
    </p>
  )
}

/** One optional-module checkbox: a bold one-line label, a one-line summary, and the fuller technical
 * justification tucked behind the info tooltip instead of always taking three or four lines of the form. */
function OptionToggle({ testId, checked, disabled, onChange, title, summary, why }: { testId: string; checked: boolean; disabled?: boolean; onChange: (v: boolean) => void; title: ReactNode; summary: string; why: string }) {
  return (
    <label className={`flex cursor-pointer items-start gap-3 rounded-lg border px-4 py-3 ${checked ? 'border-accent/60 bg-accent-soft' : 'border-nb-850 bg-nb-925 hover:bg-nb-930'}`}>
      <input type="checkbox" className="mt-1 accent-[var(--color-accent)]" checked={checked} disabled={disabled} onChange={(e) => onChange(e.target.checked)} data-testid={testId} />
      <span>
        <span className="block text-sm font-medium text-white">{title}</span>
        <span className="block text-sm text-nb-500">
          {summary}
          <InfoTip>{why}</InfoTip>
        </span>
      </span>
    </label>
  )
}

type Phase = 'form' | 'waiting' | 'approve' | 'discovering' | 'done' | 'stopped'
const STEPS: { key: Phase; label: string }[] = [
  { key: 'waiting', label: 'Agent connects' },
  { key: 'approve', label: 'You approve it' },
  { key: 'discovering', label: 'Discovering' },
  { key: 'done', label: 'Connected' },
]

/**
 * What's already automatic here needs saying out loud: the wizard notices the agent connecting and being
 * approved by itself (it is simply polling), so the only manual step left is typing the approval code. This
 * turns that into a line of ticks that fill in on their own, so it reads as "in progress", not "stuck".
 *
 * `stoppedAt` says which step it never got past when `phase` is `'stopped'`: that step gets a red mark instead
 * of being folded into "done" (a row of green checkmarks next to "this failed" would tell the opposite story
 * of the text underneath it), and nothing after it is implied to have happened either.
 */
function Stepper({ phase, stoppedAt }: { phase: Phase; stoppedAt: number }) {
  if (phase === 'form') return null
  const activeIndex = phase === 'stopped' ? stoppedAt : STEPS.findIndex((s) => s.key === phase)
  return (
    <div className="mb-4 flex items-center" data-testid="wizard-steps">
      {STEPS.map((s, i) => {
        const failed = phase === 'stopped' && i === activeIndex
        const done = !failed && i < activeIndex
        const current = !failed && i === activeIndex
        return (
          <div key={s.key} className={clsx('flex items-center', i < STEPS.length - 1 && 'flex-1')}>
            <span className="relative flex size-5 shrink-0 items-center justify-center">
              {current && <span className="absolute inline-flex size-full animate-ping rounded-full bg-accent/40" />}
              <span
                className={clsx(
                  'relative flex size-5 items-center justify-center rounded-full border text-[10px] font-medium',
                  failed
                    ? 'border-red-400/50 bg-red-400/15 text-red-300'
                    : done
                      ? 'border-emerald-400/50 bg-emerald-400/15 text-emerald-300'
                      : current
                        ? 'border-accent bg-accent-soft text-accent'
                        : 'border-nb-800 text-nb-600',
                )}
              >
                {failed ? <X size={11} /> : done ? <Check size={11} /> : i + 1}
              </span>
            </span>
            <span className={clsx('ml-1.5 whitespace-nowrap text-[11px]', failed ? 'text-red-300' : done ? 'text-nb-400' : current ? 'text-nb-200' : 'text-nb-600')}>{s.label}</span>
            {i < STEPS.length - 1 && <span className={clsx('mx-2 h-px flex-1', done ? 'bg-emerald-400/30' : 'bg-nb-850')} />}
          </div>
        )
      })}
    </div>
  )
}

/** Create a token, show the install command, then follow the agent through approval to first discovery. */
export default function ConnectClusterWizard({ open, onClose }: { open: boolean; onClose: () => void }) {
  const conn = useServer((s) => s.conn)
  const baseUrl = useServer((s) => s.url)
  const refresh = useServer((s) => s.refresh)
  const state = useServer((s) => s.state)
  const info = useServer((s) => s.info)
  const reloadInfo = useServer((s) => s.reloadInfo)
  // Settings → Installation may have changed since the page loaded; the command must reflect it.
  useEffect(() => {
    if (open) void reloadInfo()
  }, [open, reloadInfo])
  const chartRef = info?.install?.chartRef ?? ''
  // The downloadable file matters only when the command names it.
  const chartFile = chartRef ? undefined : info?.install?.chartFile
  const imagesConfigured = !!info?.install?.imagesConfigured
  // The one image this command makes the cluster pull, as the server resolved it (the organisation's setting, else the server's flags).
  const img = previewImage({ registry: info?.install?.imageRegistry ?? '', tag: info?.install?.imageTag ?? '', digest: info?.install?.imageDigest ?? '' })
  const raw = useRawTopology()
  const navigate = useNavigate()
  const [name, setName] = useState('')
  const [tier, setTier] = useState<AccessTier>(2)
  const [probe, setProbe] = useState(false)
  const [flows, setFlows] = useState(false)
  const [measure, setMeasure] = useState(false)
  const [scopeOn, setScopeOn] = useState(false)
  const [inc, setInc] = useState('')
  const [exc, setExc] = useState('')
  const [sel, setSel] = useState('')
  const [created, setCreated] = useState<CreatedToken | null>(null)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  // A default name so Create works the moment the wizard opens; still yours to change, and renameable later either way.
  // Deliberately keyed on `open` alone: it should fill in once per open, not re-fill while the count changes under it.
  useEffect(() => {
    if (open) setName((n) => n || `cluster-${raw.clusters.length + 1}`)
  }, [open])

  const scope = useMemo(() => (scopeOn && tier >= 2 ? { ...emptyScope, namespaces: splitNames(inc), exclude: splitNames(exc), selector: sel } : emptyScope), [scopeOn, tier, inc, exc, sel])
  const problems = scopeProblems(scope)
  const max = Math.min(info?.implementedTier ?? 2, 2)
  const agent = useMemo(() => {
    if (!created) return undefined
    const since = new Date(created.meta.createdAt).getTime() - 1000
    return state?.agents?.find((a) => a.name === created.meta.name && new Date(a.requestedAt).getTime() >= since)
  }, [state, created])
  const mine = raw.agents.find((a) => a.id === agent?.id)
  const cluster = raw.clusters.find((c) => c.agentId === agent?.id)
  const counts = cluster
    ? { nodes: raw.nodes.filter((n) => n.clusterId === cluster.id && !n.deletedAt).length, services: raw.services.filter((s) => s.clusterId === cluster.id && !s.deletedAt).length }
    : null
  // Whether the agent has finished its first full read of the cluster (every watch synced), not just found
  // one thing: a big cluster can report its first node long before it is done looking around.
  const discovery = discoveryStatus(extrasOf(state?.agents, agent?.id ?? '').diagnostics)

  const close = () => {
    setCreated(null)
    setName('')
    setProbe(false)
    setFlows(false)
    setMeasure(false)
    setScopeOn(false)
    setInc('')
    setExc('')
    setSel('')
    setError('')
    onClose()
  }

  const create = async () => {
    const c = conn()
    if (!c) return setError('Not connected to the server.')
    setBusy(true)
    setError('')
    try {
      const t = await api.createToken(c, name.trim(), tier)
      setCreated({ ...t, install: withScope(withMeasurements(withFlowObserver(withNodeProbe(t.install, probe && tier >= 1), flows && tier >= 2), measure && tier >= 2), scope) })
      await refresh()
    } catch (e) {
      setError(e instanceof ApiError ? e.message : 'Could not create the token.')
    } finally {
      setBusy(false)
    }
  }

  const phase = !created
    ? 'form'
    : !agent
      ? 'waiting'
      : agent.status === 'pending'
        ? 'approve'
        : agent.status === 'approved'
          ? (counts && discovery.state !== 'discovering' ? 'done' : 'discovering')
          : 'stopped'
  // Which step a stopped enrollment never got past. Rejected or expired both happen while still pending
  // approval; a revoke can only happen to something that was already approved, so it reads as having failed
  // one step further along (we don't know exactly when during discovery it was cut off, so this is our best guess).
  const stoppedAt = agent?.status === 'revoked' ? 2 : 1

  return (
    <Modal
      open={open}
      onClose={close}
      width="max-w-2xl"
      title="Connect a cluster"
      description="Install a small read-only agent in the cluster. It dials out to this server, so the cluster needs no inbound port and you hand over no kubeconfig."
      footer={
        phase === 'form' ? (
          <>
            <Button onClick={close}>Cancel</Button>
            <Button variant="primary" onClick={create} disabled={busy || !name.trim() || problems.length > 0}>
              {busy ? 'Creating…' : 'Create install command'}
            </Button>
          </>
        ) : phase === 'done' ? (
          <>
            <Button onClick={close}>Close</Button>
            <Button
              variant="primary"
              onClick={() => {
                close()
                navigate('/topology')
              }}
            >
              View in topology
            </Button>
          </>
        ) : (
          <Button onClick={close}>{phase === 'stopped' ? 'Close' : 'Continue in background'}</Button>
        )
      }
    >
      {phase === 'form' && (
        <div className="space-y-5">
          <Field label="Cluster name" hint="How it appears in Continuum. You can rename it later.">
            <Input value={name} onChange={(e) => setName(e.target.value)} placeholder="edge-patras" autoFocus />
          </Field>
          <p className="text-xs text-nb-500">Installs read-only at the recommended access level. It can never read Secrets or ConfigMaps, or change anything in the cluster — change what it may see under More options.</p>
          {max >= 1 && (
            <details className="group rounded-lg border border-nb-850 bg-nb-925" data-testid="advanced-options" open={tier !== 2 || probe || flows || measure || scopeOn}>
              <summary className="flex cursor-pointer select-none items-center gap-1.5 px-4 py-3 text-sm font-medium text-nb-300 marker:content-none">
                <ChevronRight size={14} className="text-nb-500 transition-transform group-open:rotate-90" aria-hidden />
                More options <span className="font-normal text-nb-500">(access level, node probe, traffic observer, path measurements, namespace scope)</span>
              </summary>
              <div className="space-y-3 border-t border-nb-850 p-3">
                <fieldset>
                  <legend className="mb-2 text-sm font-medium text-nb-300">What may the agent read?</legend>
                  <TierLevels
                    tiers={([1, 2] as AccessTier[]).filter((t) => t <= max)}
                    value={tier}
                    onSelect={(t) => setTier(t)}
                    data-testid="wizard-tier"
                  />
                </fieldset>
                <fieldset className="space-y-3 border-t border-nb-850 pt-3">
                  <legend className="mb-0.5 text-sm font-medium text-nb-300">Extras</legend>
                  <p className="-mt-2 mb-2 text-xs text-nb-500">Independent of access level: each of these is a separate pod you're choosing to also install, not a higher tier.</p>
                  <OptionToggle
                    testId="node-probe-toggle"
                    checked={probe}
                    disabled={tier < 1}
                    onChange={setProbe}
                    title="Also tell virtual machines from bare metal"
                    summary="Reads CPU flags and firmware info on every node, read-only, reporting only inside the cluster."
                    why="Kubernetes cannot say whether a node is a VM or a physical server, so Continuum otherwise guesses and marks the guess as uncertain. This is a small pod on every node that reads CPU flags, firmware vendor/product name, ARM board model, and which network interface kinds are up - never serial numbers, MAC addresses, disks or processes."
                  />
                  {max >= 2 && (
                    <OptionToggle
                      testId="flow-observer-toggle"
                      checked={flows}
                      disabled={tier < 2}
                      onChange={setFlows}
                      title="Also show which services talk to which"
                      summary="Counts connections and bytes on every node, via eBPF or the kernel's connection table."
                      why="Kubernetes does not record who calls whom. This is a pod on every node that counts connections and the bytes they carried (TCP via eBPF where the kernel supports it, TCP and UDP via the kernel's connection table otherwise) - never packets or contents. It needs extra privileges (root with CAP_BPF and CAP_PERFMON, and the host's process view for live byte totals), so read what it is allowed to do before you enable it."
                    />
                  )}
                  {max >= 2 && (
                    <OptionToggle
                      testId="measurements-toggle"
                      checked={measure}
                      disabled={tier < 2}
                      onChange={setMeasure}
                      title="Also measure round trips to other places"
                      summary="Times connections to a few addresses the server names, for placement advice."
                      why="Times how long a TCP connection takes to open, and how often it fails, to the busiest outside addresses this cluster already connects to plus any you add on the Sites page. Nothing is sent over the connection; loopback, link-local and metadata addresses are always refused, and the agent refuses any address the server did not issue."
                    />
                  )}
                  {max >= 2 && (
                  <div className={`rounded-lg border px-4 py-3 ${scopeOn ? 'border-accent/60 bg-accent-soft' : 'border-nb-850 bg-nb-930'}`}>
                    <label className="flex cursor-pointer items-start gap-3">
                      <input type="checkbox" className="mt-1 accent-[var(--color-accent)]" checked={scopeOn} disabled={tier < 2} onChange={(e) => setScopeOn(e.target.checked)} data-testid="scope-toggle" />
                      <span>
                        <span className="block text-sm font-medium text-white">Only look at some namespaces</span>
                        <span className="block text-sm text-nb-500">
                          By default the agent reports every namespace. What is left out never leaves the cluster.
                          <InfoTip>This is a privacy boundary, not a permission: the agent's read-only access is unchanged. System namespaces are still read to recognise the cluster but never drawn. Any namespace labelled continuum.io/observe=false is left out whatever you choose here.</InfoTip>
                        </span>
                      </span>
                    </label>
                    {scopeOn && tier >= 2 && (
                      <div className="mt-3 grid gap-3 pl-7 sm:grid-cols-2">
                        <Field label="Only these namespaces" hint="Names, separated by spaces or commas. Empty: all.">
                          <Input value={inc} onChange={(e) => setInc(e.target.value)} placeholder="shop payments" data-testid="scope-include" />
                        </Field>
                        <Field label="Never these" hint="Wins over the other two.">
                          <Input value={exc} onChange={(e) => setExc(e.target.value)} placeholder="hr-data" data-testid="scope-exclude" />
                        </Field>
                        <Field label="Or namespaces with this label" hint="key=value, on a label starting continuum.io/; added to the names on the left.">
                          <Input value={sel} onChange={(e) => setSel(e.target.value)} placeholder="continuum.io/scope=yes" data-testid="scope-label" />
                        </Field>
                        <p className="self-end text-xs text-nb-500">
                          {scopeActive(scope) ? 'The install command below will carry this scope.' : 'Nothing narrowed yet: the agent would report every namespace.'}
                        </p>
                        {problems.length > 0 && <p role="alert" className="text-xs text-red-300 sm:col-span-2">{problems.join('. ')}.</p>}
                      </div>
                    )}
                  </div>
                  )}
                </fieldset>
              </div>
            </details>
          )}
          {error && <ErrorBanner>{error}</ErrorBanner>}
        </div>
      )}

      {created && phase !== 'form' && (
        <div className="space-y-5">
          <Stepper phase={phase} stoppedAt={stoppedAt} />
          {(phase === 'waiting' || phase === 'approve') && (
            <div>
              <div className="mb-1 flex items-center justify-between text-sm text-nb-300">
                <span>Run this where you use <code className="font-mono text-xs">kubectl</code> and <code className="font-mono text-xs">helm</code> for the cluster</span>
                <CopyButton text={created.install} label="Copy command" />
              </div>
              <pre className="whitespace-pre-wrap break-all rounded-md border border-nb-850 bg-nb-950 px-3 py-3 font-mono text-xs leading-relaxed text-nb-300" data-testid="install-command">{created.install}</pre>
              <p className="mt-2 text-xs text-nb-500">
                {/nodeProbe\.enabled=true/.test(created.install) && (
                  <>The node probe mounts the host's <code className="font-mono">/sys</code> read-only. If the namespace enforces the Pod Security “baseline” profile, its pods are refused until you run <code className="font-mono">kubectl label namespace continuum-system pod-security.kubernetes.io/enforce=privileged --overwrite</code>. </>
                )}
                {/flowObserver\.enabled=true/.test(created.install) && (
                  <>The traffic observer runs as root with only CAP_BPF, CAP_PERFMON and CAP_SYS_RESOURCE and an unconfined seccomp profile, in the host network. A namespace that enforces the Pod Security “baseline” profile refuses it until you run <code className="font-mono">kubectl label namespace continuum-system pod-security.kubernetes.io/enforce=privileged --overwrite</code>. </>
                )}
                The token works once and expires at {new Date(created.meta.expiresAt).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })}. It is shown only here and cannot be retrieved later.
              </p>
              {!imagesConfigured && <div className="mt-3"><NoRegistryNotice newTab /></div>}
              {(chartFile || !imagesConfigured) && (
                <div className="mt-3 rounded-md border border-nb-850 bg-nb-930 px-3 py-2.5 text-xs text-nb-400" data-testid="install-prereq">
                  <div className="mb-1 font-medium text-nb-200">Before you run it, on the machine that runs helm</div>
                  <ol className="list-decimal space-y-1.5 pl-4">
                    {chartFile && (
                      <li>
                        Get the chart: <a className="text-accent hover:underline" href={`${baseUrl}/charts/${chartFile}`} download data-testid="chart-download">download {chartFile}</a> and put it in the folder you run the command from (the command refers to <code className="font-mono">./{chartFile}</code>). From a shell that can reach this server:
                        <code className="mt-1 block break-all rounded border border-nb-850 bg-nb-950 px-2 py-1 font-mono text-[11px] text-nb-300">curl -fLO {`${baseUrl || window.location.origin}/charts/${chartFile}`}</code>
                      </li>
                    )}
                    {!imagesConfigured && (
                      <li>
                        Make the image <code className="font-mono">{img.repository}</code> available to the cluster: point this server at a registry you publish it to, under <SettingsLink newTab /> (see the deployment guide), or load a locally built image straight into the nodes (<code className="font-mono">k3s ctr images import</code> for k3s, <code className="font-mono">kind load docker-image</code> for kind).
                      </li>
                    )}
                  </ol>
                </div>
              )}
              {chartRef && (
                <details className="mt-3 rounded-md border border-nb-850 bg-nb-930 px-3 py-2.5 text-xs text-nb-400" data-testid="install-source">
                  <summary className="cursor-pointer font-medium text-nb-200">{imagesConfigured ? 'Nothing to download or build: this pulls everything from your registry' : 'Where this command pulls the chart from'}</summary>
                  <ul className="mt-2 list-disc space-y-1 pl-4">
                    <li>The chart: <code className="font-mono">{chartRef}</code>, version {info?.install?.chartVersion}.</li>
                    {imagesConfigured && (
                      <li data-testid="install-image">
                        The image: <code className="break-all font-mono">{img.reference}</code>
                        {img.digest ? (
                          <span className="ml-1.5 inline-flex items-center gap-1 rounded border border-emerald-500/30 bg-emerald-500/10 px-1.5 py-px align-middle text-[11px] text-emerald-300" data-testid="image-pinned" title="Pinned by digest: every install pulls exactly this image">
                            <Pin size={11} aria-hidden /> pinned
                          </span>
                        ) : (
                          <span className="block text-nb-500" data-testid="image-mutable">
                            {img.tag ? 'The tag is mutable' : 'The chart’s own version tag is mutable'}: pin a digest in <SettingsLink newTab /> for reproducible installs.
                          </span>
                        )}
                        <span className="block">It must be public, or the cluster must be able to authenticate to the registry.</span>
                      </li>
                    )}
                  </ul>
                  <details className="mt-1.5">
                    <summary className="cursor-pointer text-nb-300">If helm says not found or unauthorized</summary>
                    <p className="mt-1">
                      They have not been published yet, or the registry is private. Ask whoever manages this Continuum server to publish them (see the deployment guide) or grant the cluster's registry credentials access.
                      {info?.install?.chartFile && (
                        <> To skip the registry for just the chart, use the copy this server already serves: <code className="font-mono">curl -fLO {`${baseUrl || window.location.origin}/charts/${info.install.chartFile}`}</code>, then change the start of the command to <code className="font-mono">helm install continuum-agent ./{info.install.chartFile}</code> and drop <code className="font-mono">--version</code>.</>
                      )}
                    </p>
                  </details>
                </details>
              )}
            </div>
          )}

          {phase === 'waiting' && (
            <p className="flex items-center gap-2 text-sm text-nb-400">
              <Loader2 size={16} className="animate-spin text-accent" /> Waiting for the agent to start and connect. This can take a minute.
            </p>
          )}
          {phase === 'approve' && mine && <ApprovalCard agent={mine} />}
          {phase === 'discovering' && (
            <p className="flex items-center gap-2 text-sm text-nb-400">
              <Loader2 size={16} className="animate-spin text-accent" />
              {discovery.state === 'discovering' ? `Approved. Looking at the cluster… ${discovery.synced} of ${discovery.total} watches read so far.` : 'Approved. Waiting for the first discovery…'}
            </p>
          )}
          {phase === 'done' && counts && (
            <p className="flex items-center gap-2 text-sm text-emerald-300">
              <CheckCircle2 size={16} /> Connected. Found {counts.nodes} {counts.nodes === 1 ? 'node' : 'nodes'} and {counts.services} {counts.services === 1 ? 'workload' : 'workloads'}. Review the grouping suggestions in the Discovery inbox.
            </p>
          )}
          {phase === 'stopped' && <p className="text-sm text-amber-300">This enrollment was {agent?.status}{agent?.reason ? ` (${agent.reason})` : ''}. Create a new install command to try again.</p>}
        </div>
      )}
    </Modal>
  )
}
