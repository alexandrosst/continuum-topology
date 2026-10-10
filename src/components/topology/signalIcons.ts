import { Activity, FileText, Waypoints, type LucideIcon } from 'lucide-react'
import type { Modality } from '@/lib/install'

/** The glyph of each signal type, the same wherever a part says what it collects: a box, the key. */
export const SIGNAL_ICON: Record<Modality, LucideIcon> = { metrics: Activity, logs: FileText, traces: Waypoints }
