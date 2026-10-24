import { CopyButton } from '@/components/ui/primitives'
import type { ControllerDoc } from '@/lib/generated/wire'

/**
 * The same install as the helm command, asked for as an IkhnosAgent resource by the optional controller: three things to run, in the order to
 * run them. The server writes them in the call that issues the token, from the same choices as the helm command, so the two views cannot say
 * different things. The token is in the second command only; the resource names the Secret that holds it, so the resource can be kept.
 */
export default function ControllerSteps({ c }: { c: ControllerDoc }) {
  if (!c.available) {
    return (
      <div className="rounded-md border border-nb-850 bg-nb-930 px-3 py-2.5 text-xs text-nb-400" data-testid="controller-unavailable">
        <div className="mb-1 font-medium text-nb-200">The controller is not offered here</div>
        <p>{c.reason}</p>
        <p className="mt-1.5">The helm command in the other tab installs the same agent.</p>
      </div>
    )
  }
  const steps = [
    {
      id: 'install',
      title: '1. Install the controller',
      note: (
        <>
          Skip this if it is already running in <code className="font-mono">{c.namespace}</code>. It installs the <code className="font-mono">IkhnosAgent</code> resource definition with it, and is given permission for access tier {c.maxTier} and no more.
        </>
      ),
      text: c.install,
    },
    {
      id: 'secret',
      title: '2. Give it the one-time token',
      note: <>The token is in this command only. The resource below names this Secret, and you can delete it once the agent has enrolled.</>,
      text: c.secret,
    },
    {
      id: 'manifest',
      title: '3. Ask for the agent',
      note: (
        <>
          The resource says what to install and holds no token, so it can be kept in your repository. Deleting it uninstalls the agent.
        </>
      ),
      text: c.manifestCommand,
    },
  ]
  return (
    <div className="space-y-4" data-testid="controller-steps">
      <p className="text-sm text-nb-300">
        Run these where you use <code className="font-mono text-xs">kubectl</code> and <code className="font-mono text-xs">helm</code> for the cluster, in this order
      </p>
      {steps.map((s) => (
        <div key={s.id}>
          <div className="mb-1 flex items-center justify-between text-sm text-nb-300">
            <span>{s.title}</span>
            <CopyButton text={s.text} label="Copy command" />
          </div>
          <pre className="whitespace-pre-wrap break-all rounded-md border border-nb-850 bg-nb-950 px-3 py-3 font-mono text-xs leading-relaxed text-nb-300" data-testid={`controller-${s.id}`}>{s.text}</pre>
          <p className="mt-1.5 text-xs text-nb-500">{s.note}</p>
        </div>
      ))}
      <p className="text-xs text-nb-500">
        A controller that is already installed with a lower ceiling than tier {c.maxTier} refuses this resource and says so in its status; raise it with{' '}
        <code className="font-mono">helm upgrade ikhnos-controller &lt;chart&gt; --reset-then-reuse-values --set maxAccessTier={c.maxTier}</code>.
      </p>
    </div>
  )
}
