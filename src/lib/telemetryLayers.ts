import { AppWindow, Server, type LucideIcon } from 'lucide-react'

/** The two signal layers TELEMETRY_SIGNALS' own `layer` field carries (see consent.ts/install.ts) - kept
 *  here, not inside GuidedWizard.tsx, so both it and TelemetryReviewPipeline.tsx (the guided wizard's
 *  Review step, drawn as a pipeline) import the exact same labels/icons from a plain data file instead of
 *  one component reaching into another's locals. */
export type Layer = 'infrastructure' | 'application'

export const LAYER_META: Record<Layer, { label: string; hint: string; icon: LucideIcon }> = {
  infrastructure: { label: 'Infrastructure', hint: 'The clusters, nodes and Kubernetes objects this agent runs on - not your applications themselves.', icon: Server },
  application: { label: 'Application', hint: 'What your own workloads emit - metrics they push, logs, and traces.', icon: AppWindow },
}

export const LAYER_CARDS: Layer[] = ['infrastructure', 'application']
