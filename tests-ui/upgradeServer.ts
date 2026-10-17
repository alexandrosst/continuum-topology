import type { AgentUpgradeAnswer, AgentUpgradeRequest } from '@/lib/agentUpgrade'

// A stand-in for POST /commands/agent-upgrade in the component tests. What the server writes from a request is pinned by the Go tests
// against tests/install_commands.golden.json; what these tests hold is what the page ASKS for, and that it shows what comes back.

/** Every request the page made, oldest first. */
export const upgradeCalls: AgentUpgradeRequest[] = []

/** The command the stand-in answers: it says which request it is for, so a test can tell an answer to the draft on screen from another. */
export const answerTo = (req: AgentUpgradeRequest) => `stub-command ${JSON.stringify(req)}`

export async function answer(_conn: unknown, req: AgentUpgradeRequest): Promise<AgentUpgradeAnswer> {
  upgradeCalls.push(req)
  return { command: answerTo(req), upgrade: answerTo(req), secretCommand: '', secrets: [], problems: [] }
}

export const lastUpgrade = (): AgentUpgradeRequest => {
  const last = upgradeCalls.at(-1)
  if (!last) throw new Error('the page has not asked for a command')
  return last
}

export const forgetUpgrades = () => {
  upgradeCalls.length = 0
}
