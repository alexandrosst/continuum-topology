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
