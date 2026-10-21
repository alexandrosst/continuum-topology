/** The page's own words for a shape the server writes: the generated type (src/lib/generated/wire.ts), with the fields the page knows more
 *  about than the server says. The server writes a word such as a role or a state as text; the page knows the words it handles, and keeps
 *  them as a union here. Everything not named in N stays exactly as the server writes it. */
export type Narrowed<T, N> = Omit<T, keyof N> & N

/** What a call may send. The server reads a body field by field and takes one that is missing as empty, so each may be left out; what it
 *  refuses is a field it does not know, and a field of the wrong kind, which is what this catches. */
export type Body<T> = Partial<T>
