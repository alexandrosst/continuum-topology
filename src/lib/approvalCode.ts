/**
 * The approval code an agent prints in its own log: eight characters from Crockford's base32 (digits and letters
 * without I, L, O, U), shown as XXXX-XXXX. These helpers mirror what the server accepts (internal/approval):
 * any case, spaces and dashes ignored, O read as 0 and I or L as 1.
 */
const ALPHABET = '0123456789ABCDEFGHJKMNPQRSTVWXYZ'
export const CODE_LENGTH = 8

/** What a person typed or pasted, reduced to the characters a code can have (at most eight). */
export function cleanCode(raw: string): string {
  let out = ''
  for (const ch of raw.toUpperCase()) {
    const c = ch === 'O' ? '0' : ch === 'I' || ch === 'L' ? '1' : ch
    if (ALPHABET.includes(c)) out += c
    if (out.length === CODE_LENGTH) break
  }
  return out
}

/** The same, laid out for the input: a dash after the fourth character. */
export function formatCode(raw: string): string {
  const c = cleanCode(raw)
  return c.length > 4 ? `${c.slice(0, 4)}-${c.slice(4)}` : c
}

export const codeComplete = (raw: string): boolean => cleanCode(raw).length === CODE_LENGTH

/** Whether the pasted text holds characters that cannot belong to a code (so the person is told, not silently trimmed). */
export function hasStrayCharacters(raw: string): boolean {
  return [...raw.toUpperCase()].some((ch) => !/[\s\-_]/.test(ch) && !ALPHABET.includes(ch === 'O' ? '0' : ch === 'I' || ch === 'L' ? '1' : ch))
}

/** Where the code is printed, and the command that shows it. */
export const CODE_LOG_COMMAND = 'kubectl -n continuum-system logs deploy/continuum-agent'

/**
 * What to take from pasted text. A person may copy the whole log line ("enrollment pending: approval code
 * K7QM-4TXD - enter it in…"), so a XXXX-XXXX group in it wins; otherwise the text is cleaned as typed.
 */
export function fromPaste(text: string): string {
  const m = /(?<![0-9A-Za-z])([0-9A-Za-z]{4})-([0-9A-Za-z]{4})(?![0-9A-Za-z])/.exec(text)
  return formatCode(m ? `${m[1]}${m[2]}` : text)
}
