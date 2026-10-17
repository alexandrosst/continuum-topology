import { useEffect, useState } from 'react'
import { api } from './api'
import type { ReleaseTarget } from './consent'
import { telemetrySpec } from './telemetrySpec'
import { useConn } from '@/store/server'

// The command that changes an agent that is already installed (a wider access ceiling, other telemetry, or both) is written by the
// server, which knows the chart it serves and renders every command from one description (backend/internal/install). This page says
// what it wants in that description's words and shows the answer; it no longer builds a command itself.

/** What the server writes the command from. The chart is not in it: the server names its own. */
export interface AgentUpgradeRequest {
  /** Where the agent's Helm release lives, as the agent reported it. The server falls back to the chart's defaults for what is not a plain name. */
  namespace?: string
  release?: string
  /** The access ceiling to set. Absent leaves it as it is. */
  tier?: number
  /** What to collect and where to send it (see telemetrySpec). Absent leaves the install's telemetry as it is. */
  telemetry?: ReturnType<typeof telemetrySpec>
}

/** A Secret the command's credential flags point at, and the command that creates it. */
export interface CredentialSecretDoc {
  name: string
  variable: string
  lanes: string[]
  command: string
}

/** What POST /commands/agent-upgrade answers. */
export interface AgentUpgradeAnswer {
  /** The whole block to paste: the Secrets' commands first, then the upgrade. Empty when there are problems. */
  command: string
  upgrade: string
  secretCommand: string
  secrets: CredentialSecretDoc[]
  /** What makes the telemetry unfit to write a command from. */
  problems: string[]
}

/** The request for a tier-only change or a telemetry change, from what the page holds. */
export function upgradeRequest(o: { target?: ReleaseTarget; tier?: number; telemetry?: Parameters<typeof telemetrySpec>[0]; measurementsOn?: boolean }): AgentUpgradeRequest {
  return {
    ...(o.target?.namespace ? { namespace: o.target.namespace } : {}),
    ...(o.target?.release ? { release: o.target.release } : {}),
    ...(o.tier === undefined ? {} : { tier: o.tier }),
    ...(o.telemetry ? { telemetry: telemetrySpec(o.telemetry, o.measurementsOn) } : {}),
  }
}

/** Where the question stands. `waiting` covers both the pause after an edit and the server's thinking: either way there is no answer yet for what is on screen. */
export type UpgradeCommand =
  | { status: 'idle' }
  | { status: 'waiting' }
  | { status: 'ready'; answer: AgentUpgradeAnswer }
  | { status: 'failed'; message: string }

/** How long an edit rests before the server is asked. A person typing an endpoint changes the draft on every key. */
export const UPGRADE_DEBOUNCE_MS = 250

/**
 * The server's command for a request, kept current as it changes. Only the answer to what is on screen is ever returned: one that was
 * asked of an earlier draft, even if it arrives last, is dropped, so a command is never shown beside settings it was not written from.
 * No request (null) asks nothing.
 */
export function useAgentUpgradeCommand(request: AgentUpgradeRequest | null): UpgradeCommand {
  const conn = useConn()
  const org = conn.org ?? ''
  const key = request ? JSON.stringify(request) : ''
  const [done, setDone] = useState<{ key: string; result: UpgradeCommand } | null>(null)
  // The organisation is part of what an answer is for: switching it must not show the last one's command until the next answer.
  const asked = org + '|' + key
  useEffect(() => {
    if (!key) return
    let current = true
    const timer = setTimeout(() => {
      api
        .agentUpgradeCommand(conn, JSON.parse(key) as AgentUpgradeRequest)
        .then((answer) => current && setDone({ key: asked, result: { status: 'ready', answer } }))
        .catch((e: unknown) => current && setDone({ key: asked, result: { status: 'failed', message: e instanceof Error ? e.message : 'The server could not write the command.' } }))
    }, UPGRADE_DEBOUNCE_MS)
    return () => {
      current = false
      clearTimeout(timer)
    }
    // conn's identifying fields, not the object: it is rebuilt when either changes and is the same otherwise.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key, asked, conn.url, org])
  if (!key) return { status: 'idle' }
  return done && done.key === asked ? done.result : { status: 'waiting' }
}
