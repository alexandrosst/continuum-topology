import assert from 'node:assert/strict'
import { test } from 'node:test'
import { toCsv } from '../src/lib/csv'

test('csv: plain fields need no quoting', () => {
  assert.equal(toCsv(['a', 'b'], [['1', '2']]), 'a,b\r\n1,2\r\n')
})

test('csv: commas, quotes and newlines are RFC 4180 escaped', () => {
  const csv = toCsv(['Who', 'Detail'], [['ann, smith', 'said "hi"\nbye']])
  assert.equal(csv, 'Who,Detail\r\n"ann, smith","said ""hi""\nbye"\r\n')
})

test('csv: null/undefined cells become empty fields, not the literal word', () => {
  assert.equal(toCsv(['a', 'b'], [[null, undefined]]), 'a,b\r\n,\r\n')
})

test('csv: numbers are stringified plainly', () => {
  assert.equal(toCsv(['n'], [[42]]), 'n\r\n42\r\n')
})

test('csv: an empty row set is just the header', () => {
  assert.equal(toCsv(['a', 'b'], []), 'a,b\r\n')
})

test('csv: a text cell opening with a formula character gets a leading apostrophe', () => {
  for (const lead of ['=', '+', '-', '@', '\t']) {
    assert.equal(toCsv(['d'], [[`${lead}SUM(A1)`]]), `d\r\n'${lead}SUM(A1)\r\n`, `lead ${JSON.stringify(lead)}`)
  }
})

test('csv: a leading CR is prefixed and then quoted, since a bare CR would split the record', () => {
  assert.equal(toCsv(['d'], [['\r=1']]), "d\r\n\"'\r=1\"\r\n")
})

test('csv: the prefix is applied before quoting, so =A,B stays one quoted field', () => {
  assert.equal(toCsv(['d'], [['=A,B']]), `d\r\n"'=A,B"\r\n`)
})

test('csv: numbers are never prefixed, even negative ones', () => {
  assert.equal(toCsv(['n'], [[-5]]), 'n\r\n-5\r\n')
})

test('csv: a formula character that is not the first one is left alone', () => {
  assert.equal(toCsv(['d'], [['a=b', 'x-y', 'me@x']]), 'd\r\na=b,x-y,me@x\r\n')
})
