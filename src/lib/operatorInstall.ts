import { shArg, shQuote } from './install'
import { buildExtraProcessors, processorKey, processorProblems, processorTarget, type ProcessorEntry } from './processorCatalog'

/**
 * What is wrong with the extra-processor draft for a regional operator, in words a person can act on -
 * empty when it is fine. Just a thin, named re-export of processorCatalog's own check: this file's job is
 * pairing that check with buildOperatorInstallCommand below, not reimplementing it.
 */
export const operatorProcessorProblems = (entries: ProcessorEntry[]): string[] => processorProblems(entries)

/**
 * Layers the regional-operator chart's own processor configuration on top of the `install` string the
 * server already printed (see admin_operators.go's operatorInstallCommand) - the same relationship
 * install.ts's withTelemetry() has to a server-printed enrollment command. This is deliberately client-side
 * only: unlike scope, destination and the receiver token, processor tuning is chart-values-only and is
 * never stored on the Operator record itself (see the plan's Part G note on why), so it can't be part of
 * what the server prints. Values path is `processors.*` (not `telemetry.processors.*` - this chart has no
 * `telemetry` root, see continuum-regional-operator/values.yaml), otherwise identical in shape to the
 * extraProcessors handling in install.ts's withTelemetry.
 * Returns `install` unchanged when there is nothing to add, or the draft doesn't pass
 * `operatorProcessorProblems` - the same "don't print something broken" rule withTelemetry follows.
 */
export function buildOperatorInstallCommand(install: string, extraProcessors: ProcessorEntry[]): string {
  if (!install || extraProcessors.length === 0 || operatorProcessorProblems(extraProcessors).length > 0) return install
  let cmd = install.trimEnd()
  cmd += ` \\\n  --set-json processors.extraProcessors=${shQuote(JSON.stringify(buildExtraProcessors(extraProcessors)))}`
  const byTarget = { extraProcessorNames: [] as string[], extraTracesProcessorNames: [] as string[] }
  for (const e of extraProcessors) byTarget[processorTarget(e)].push(processorKey(e))
  for (const [target, keys] of Object.entries(byTarget)) {
    keys.forEach((key, i) => {
      cmd += ` \\\n  --set-string processors.${target}[${i}]=${shArg(key)}`
    })
  }
  return cmd
}
