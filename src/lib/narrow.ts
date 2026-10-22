/** A shape the server writes (src/lib/generated/wire.ts) with the fields the page itself adds or keeps differently, such as the revision a
 *  settings document carries beside what the server sends. Everything not named in N stays exactly as the server writes it. A word the
 *  server lists (a role, a state) is already a union in the generated type, and is not narrowed here. */
export type Narrowed<T, N> = Omit<T, keyof N> & N

/** What a call may send. The server reads a body field by field and takes one that is missing as empty, so each may be left out; what it
 *  refuses is a field it does not know, and a field of the wrong kind, which is what this catches. */
export type Body<T> = Partial<T>

/** Whether a value read from the wire is one of the words a union holds. The set is written as a record over the union, so a word the
 *  server adds is a compile error here and not a state the page reads as unknown. */
export function isWord<T extends string>(set: Record<T, unknown>, v: unknown): v is T {
  return typeof v === 'string' && Object.prototype.hasOwnProperty.call(set, v)
}
