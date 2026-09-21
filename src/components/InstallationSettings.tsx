import clsx from 'clsx'
import { CircleAlert, Pin, Save, TriangleAlert } from 'lucide-react'
import { useId, useState } from 'react'
import { Button, Input } from '@/components/ui/primitives'
import { atLeast, type Conn } from '@/lib/api'
import { imageOk, imageProblems, previewImage } from '@/lib/image'
import { useServer } from '@/store/server'
import { useSettings } from '@/store/settings'

/** One labelled input. The label, the help and the problem are tied to the input, so a screen reader hears all three. */
function TextField({ label, help, problem, value, onChange, disabled, placeholder, testId }: { label: string; help: React.ReactNode; problem: string; value: string; onChange: (v: string) => void; disabled: boolean; placeholder: string; testId: string }) {
  const id = useId()
  return (
    <div>
      <label htmlFor={id} className="mb-1.5 block text-sm font-medium text-nb-300">{label}</label>
      <Input
        id={id}
        value={value}
        disabled={disabled}
        placeholder={placeholder}
        spellCheck={false}
        autoCapitalize="none"
        autoCorrect="off"
        autoComplete="off"
        aria-invalid={!!problem}
        aria-describedby={`${id}-help ${id}-problem`}
        onChange={(e) => onChange(e.target.value)}
        className={clsx('font-mono text-[13px]', problem && 'border-red-400/60')}
        data-testid={testId}
      />
      <p id={`${id}-problem`} role={problem ? 'alert' : undefined} className={clsx('mt-1 text-xs text-red-300', !problem && 'hidden')} data-testid={`${testId}-problem`}>{problem}</p>
      <p id={`${id}-help`} className="mt-1 text-xs text-nb-500">{help}</p>
    </div>
  )
}

const Code = ({ children }: { children: React.ReactNode }) => <code className="font-mono text-nb-300">{children}</code>

/**
 * Where install commands pull the agent image and chart from. Nothing is preset: an administrator points this
 * organisation's commands at a registry they published to (see scripts/publish.sh), optionally pinned by digest.
 */
export default function InstallationSettings({ conn }: { conn: Conn }) {
  const admin = useServer((s) => atLeast(s.role, 'admin'))
  const reloadInfo = useServer((s) => s.reloadInfo)
  const chartVersion = useServer((s) => s.info?.install?.chartVersion ?? '')
  const { settings, save, error, loaded } = useSettings()
  const [draft, setDraft] = useState({ registry: '', tag: '', digest: '' })
  const [saved, setSaved] = useState(false)
  const [busy, setBusy] = useState(false)
  // Follow the server's values when they change (loaded, saved, another tab), without an effect.
  const [seen, setSeen] = useState(settings)
  if (seen !== settings) {
    setSeen(settings)
    setDraft({ registry: settings.imageRegistry, tag: settings.imageTag, digest: settings.imageDigest })
  }

  const problems = imageProblems(draft)
  const ok = imageOk(problems)
  const dirty = draft.registry.trim() !== settings.imageRegistry || draft.tag.trim() !== settings.imageTag || draft.digest.trim() !== settings.imageDigest
  const preview = previewImage(draft, settings.imageDefaults)
  const set = (k: 'registry' | 'tag' | 'digest') => (v: string) => {
    setSaved(false)
    setDraft((d) => ({ ...d, [k]: v }))
  }
  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!ok || !dirty || busy) return
    setBusy(true)
    const done = await save(conn, { ...settings, imageRegistry: draft.registry.trim().replace(/\/+$/, ''), imageTag: draft.tag.trim(), imageDigest: draft.digest.trim() })
    if (done) await reloadInfo() // the install wizard reads what the server now resolves
    setSaved(done)
    setBusy(false)
  }

  return (
    <section id="installation" aria-labelledby="installation-title" className="mb-8 scroll-mt-6" data-testid="installation-settings">
      <h2 id="installation-title" className="mb-1 text-sm font-medium text-white">Installation</h2>
      <p className="mb-3 max-w-2xl text-sm text-nb-500">
        Where the install command for this organisation’s clusters gets the agent image and chart. Whoever controls this registry controls what runs in your clusters, so nothing is preset: point it at a registry you publish to.
      </p>
      <form onSubmit={submit} className="rounded-xl border border-nb-850 bg-nb-925 p-5" noValidate>
        <div className="grid gap-4 md:grid-cols-2">
          <TextField
            label="Image registry"
            value={draft.registry}
            onChange={set('registry')}
            problem={problems.registry}
            disabled={!admin}
            placeholder="registry.example.com/team"
            testId="image-registry"
            help={<>Host and optional path, lowercase, no <Code>https://</Code>. A bare name such as <Code>myteam</Code> is a Docker Hub namespace. The image is <Code>&lt;registry&gt;/continuum</Code>; the chart is fetched from the same place.</>}
          />
          <TextField
            label="Image tag"
            value={draft.tag}
            onChange={set('tag')}
            problem={problems.tag}
            disabled={!admin}
            placeholder="chart’s own version"
            testId="image-tag"
            help="Optional. Empty uses the chart’s own appVersion. A tag can be moved to a different image later."
          />
          <div className="md:col-span-2">
            <TextField
              label="Image digest"
              value={draft.digest}
              onChange={set('digest')}
              problem={problems.digest}
              disabled={!admin}
              placeholder="sha256:…"
              testId="image-digest"
              help={<>Optional, and the safest choice: <Code>sha256:</Code> and 64 hex digits pin the image so it can never change under you (the tag is then ignored). <Code>scripts/publish.sh</Code> prints the digest of what it pushed; or run <Code>docker buildx imagetools inspect &lt;registry&gt;/continuum:&lt;tag&gt;</Code>.</>}
            />
          </div>
        </div>

        <div className="mt-5 rounded-lg border border-nb-850 bg-nb-930 px-4 py-3 text-xs" data-testid="image-preview" aria-live="polite">
          <div className="mb-1.5 font-medium text-nb-300">What install commands will use</div>
          {!ok ? (
            <p className="text-nb-500" data-testid="preview-invalid">Fix the highlighted fields to see the image and chart the install commands will use.</p>
          ) : preview.configured ? (
            <dl className="grid grid-cols-[auto_1fr] gap-x-3 gap-y-1">
              <dt className="text-nb-500">Image</dt>
              <dd className="break-all font-mono text-nb-300" data-testid="preview-image">
                {preview.digest ? (
                  <>
                    {preview.pinnedReference}
                    {preview.tag && <span className="text-nb-500"> (tag {preview.tag} is ignored)</span>}
                  </>
                ) : (
                  <>
                    {preview.reference}
                    {!preview.tag && <span className="font-sans text-nb-500"> (tag: the chart’s own version)</span>}
                  </>
                )}
              </dd>
              <dt className="text-nb-500">Chart</dt>
              <dd className="break-all font-mono text-nb-300" data-testid="preview-chart">{preview.chartRef}{chartVersion && <span className="font-sans text-nb-500"> version {chartVersion}</span>}</dd>
            </dl>
          ) : (
            <p className="text-nb-400" data-testid="preview-none">
              No registry configured: commands use the chart’s built-in image name (<Code>{preview.repository}</Code>) and the chart file this server serves. You must have published that image yourself.
            </p>
          )}
          {ok && preview.fromServer && <p className="mt-1.5 text-nb-500">Not set here: these are the server’s defaults (its <Code>--image-registry</Code> settings). Saving a registry above replaces them for this organisation.</p>}
          {ok && preview.configured && (
            <p className={clsx('mt-2 flex items-start gap-1.5', preview.digest ? 'text-emerald-300' : 'text-nb-500')} data-testid="preview-pin">
              {preview.digest ? <Pin size={13} className="mt-px shrink-0" aria-hidden /> : <TriangleAlert size={13} className="mt-px shrink-0" aria-hidden />}
              {preview.digest ? 'Pinned by digest: every install pulls exactly this image.' : 'Not pinned: a tag is mutable, so pin a digest for reproducible installs.'}
            </p>
          )}
        </div>

        {error && (
          <p className="mt-3 text-sm text-red-300" role="alert" data-testid="image-error">
            <CircleAlert size={13} className="mr-1 inline" aria-hidden />
            {error}
          </p>
        )}
        <div className="mt-4 flex flex-wrap items-center gap-3">
          {admin ? (
            <>
              <Button type="submit" variant="primary" disabled={!dirty || !ok || !loaded || busy} data-testid="save-image">
                <Save size={15} /> {busy ? 'Saving…' : 'Save'}
              </Button>
              {saved && !dirty && <span className="text-sm text-emerald-300" role="status" data-testid="image-saved">Saved. New install commands use these values.</span>}
            </>
          ) : (
            <p className="text-xs text-nb-500">Only administrators can change these.</p>
          )}
        </div>
      </form>
    </section>
  )
}
