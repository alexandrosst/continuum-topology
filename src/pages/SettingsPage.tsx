import { Download, RotateCcw, Trash2, Upload } from 'lucide-react'
import { useEffect, useRef, useState } from 'react'
import { useLocation } from 'react-router-dom'
import { ConfirmModal } from '@/components/forms'
import InstallationSettings from '@/components/InstallationSettings'
import { Button, PageHeader } from '@/components/ui/primitives'
import { atLeast } from '@/lib/api'
import { declaredNote, rehydrate, toDeclared } from '@/lib/declared'
import { useConn, useServer } from '@/store/server'
import { exportTopology, normalize, useRawTopology, useTopology } from '@/store/topology'

function Card({ title, description, children }: { title: string; description: string; children: React.ReactNode }) {
  return (
    <div className="flex items-center justify-between gap-6 rounded-xl border border-nb-850 bg-nb-925 p-5">
      <div>
        <div className="text-sm font-medium text-white">{title}</div>
        <div className="mt-1 max-w-lg text-sm text-nb-500">{description}</div>
      </div>
      <div className="shrink-0">{children}</div>
    </div>
  )
}

export default function SettingsPage() {
  const onServer = useServer((s) => s.status === 'connected' && atLeast(s.role, 'admin'))
  const { replaceAll, reset, clear, clusters, nodes, services, devices, dependencies, applications, sites } = useTopology()
  const conn = useConn()
  const connected = useServer((s) => s.status === 'connected')
  const { hash } = useLocation()
  // /settings#installation lands on the section (the wizard links there); the page is lazy, so scroll after it renders.
  useEffect(() => {
    if (hash) document.getElementById(hash.slice(1))?.scrollIntoView({ block: 'start' })
  }, [hash, connected])
  const fileRef = useRef<HTMLInputElement>(null)
  const [msg, setMsg] = useState<{ ok: boolean; text: string } | null>(null)
  const [confirm, setConfirm] = useState<'clear' | 'reset' | null>(null)

  const download = () => {
    // A file holds what people declared. What agents observed is not exported: it is the server's to report, and a
    // copy in a file would come back later as if it were still true.
    const current = exportTopology()
    const { model, report } = toDeclared(current)
    const blob = new Blob([JSON.stringify({ ...model, schemaVersion: current.schemaVersion }, null, 2)], { type: 'application/json' })
    const a = document.createElement('a')
    a.href = URL.createObjectURL(blob)
    a.download = 'topology.json'
    a.click()
    URL.revokeObjectURL(a.href)
    const note = declaredNote(report)
    setMsg(note ? { ok: true, text: `Exported the declared workspace. ${note.replace(/^Removed/, 'Left out')}` } : null)
  }

  const onFile = async (f: File | undefined) => {
    if (!f) return
    try {
      // normalize refuses a file from a newer version with a message saying so. What it accepts is reduced to what
      // people declared: an older file may carry discovered records, which are not brought in.
      const { model: t, report } = toDeclared(normalize(JSON.parse(await f.text())))
      // Signed in, the records this browser already holds from the server stay; on their own the imported file is all there is.
      replaceAll(connected ? rehydrate(t, useRawTopology.getState()) : t)
      const note = declaredNote(report)
      setMsg({ ok: true, text: `${note ? note + ' ' : ''}Imported ${t.clusters.length} clusters, ${t.nodes.length} nodes, ${t.services.length} services, ${t.devices.length} devices, ${t.dependencies.length} dependencies, ${t.applications.length} applications, ${t.sites.length} sites.` })
    } catch (e) {
      setMsg({ ok: false, text: e instanceof Error ? e.message : 'Could not read file.' })
    }
    if (fileRef.current) fileRef.current.value = ''
  }

  return (
    <>
      <PageHeader title="Settings" description={connected ? 'Where install commands pull the agent from, and your topology as a file.' : 'Your topology is stored in this browser. Export it as JSON to back it up or share it.'} />

      {connected && <InstallationSettings conn={conn} />}

      <h2 className="mb-1 text-sm font-medium text-white">Import / Export</h2>
      <p className="mb-3 max-w-2xl text-sm text-nb-500">{onServer ? "Your topology is saved to the server and shared with everyone who signs in. Export it as JSON to keep a copy." : "Your topology is stored in this browser. Export it as JSON to back it up or share it."}</p>

      <div className="mb-6 grid grid-cols-3 gap-3 lg:grid-cols-7">
        {[['Clusters', clusters.length], ['Nodes', nodes.length], ['Services', services.length], ['Devices', devices.length], ['Dependencies', dependencies.length], ['Applications', applications.length], ['Sites', sites.length]].map(([l, v]) => (
          <div key={l} className="rounded-xl border border-nb-850 bg-nb-925 px-5 py-4">
            <div className="text-2xl font-medium text-white">{v}</div>
            <div className="text-xs text-nb-500">{l}</div>
          </div>
        ))}
      </div>

      <div className="space-y-3">
        <Card title="Export topology" description="Download clusters, nodes, services and dependencies as a JSON file.">
          <Button onClick={download}><Download size={16} /> Export JSON</Button>
        </Card>
        <Card title="Import topology" description="Replace the current topology with a JSON file. References to missing clusters, nodes or services are dropped automatically.">
          <Button onClick={() => fileRef.current?.click()}><Upload size={16} /> Import JSON</Button>
          <input ref={fileRef} type="file" accept="application/json,.json" hidden onChange={(e) => onFile(e.target.files?.[0])} />
        </Card>
        <Card title="Load sample topology" description="Restore the built-in cloud → edge → far-edge example.">
          <Button onClick={() => setConfirm('reset')}><RotateCcw size={16} /> Load sample</Button>
        </Card>
        <Card title="Clear everything" description="Remove all clusters, nodes, services and dependencies to start from a blank canvas.">
          <Button variant="danger" onClick={() => setConfirm('clear')}><Trash2 size={16} /> Clear all</Button>
        </Card>
      </div>

      {msg && (
        <p className={'mt-4 text-sm ' + (msg.ok ? 'text-emerald-400' : 'text-red-400')} role="status">{msg.text}</p>
      )}

      <p className="mt-10 max-w-2xl text-xs leading-5 text-nb-500">
        Place data: city names and coordinates from <a className="underline hover:text-nb-400" href="https://www.geonames.org/" target="_blank" rel="noreferrer">GeoNames</a> (CC BY 4.0), country outlines from Natural Earth. When the server has a GeoIP database, IP geolocation by <a className="underline hover:text-nb-400" href="https://db-ip.com" target="_blank" rel="noreferrer">DB-IP.com</a> (CC BY 4.0) or MaxMind GeoLite2.
      </p>

      {confirm && (
        <ConfirmModal
          title={confirm === 'clear' ? 'Clear the whole topology?' : 'Replace with the sample topology?'}
          message={onServer ? "This replaces the shared workspace on the server for everyone. Export first if you want a backup." : "This replaces what is currently stored in this browser. Export first if you want a backup."}
          onConfirm={confirm === 'clear' ? clear : reset}
          onClose={() => setConfirm(null)}
        />
      )}
    </>
  )
}
