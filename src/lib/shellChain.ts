/**
 * Joins shell commands into one block to paste, each running only if the one before it worked (a failed Secret stops the
 * upgrade that needs it).
 *
 * A command that ends in a here-document (`kubectl apply -f - <<'X' ... X`) cannot simply be followed by ` && \`: the closing
 * word must be alone on its line, or the shell never sees the document end and waits at its `>` prompt for more. Such a command is
 * wrapped in braces instead, and the `&&` follows the closing brace. Only the commands before the last need it.
 */
export function chainCommands(commands: string[]): string {
  const last = commands.length - 1
  return commands.map((c, i) => (i < last && HEREDOC.test(c) ? `{ ${c}\n}` : c)).join(' && \\\n')
}

const HEREDOC = /<<-?\s*['"]?[A-Za-z_]\w*['"]?/
