import assert from 'node:assert/strict'
import { test } from 'node:test'
import { cleanCode, codeComplete, formatCode, fromPaste, hasStrayCharacters } from '../src/lib/approvalCode'
import { skewLabel, skewWarning } from '../src/lib/clock'

test('an approval code is cleaned the way the server reads it', () => {
  assert.equal(cleanCode('k7qm-4txd'), 'K7QM4TXD')
  assert.equal(cleanCode(' K7QM 4TXD\n'), 'K7QM4TXD')
  assert.equal(cleanCode('O0O0-1I1L'), '00001111')
  assert.equal(cleanCode('K7QM4TXDEXTRA'), 'K7QM4TXD')
  assert.equal(cleanCode('K7QU'), 'K7Q') // U is not in the alphabet
  assert.equal(cleanCode(''), '')
})

test('the code is laid out as it is typed', () => {
  assert.equal(formatCode('k7'), 'K7')
  assert.equal(formatCode('k7qm'), 'K7QM')
  assert.equal(formatCode('k7qm4'), 'K7QM-4')
  assert.equal(formatCode('K7QM-4TXD'), 'K7QM-4TXD')
  assert.equal(formatCode('k7qm4txdzz'), 'K7QM-4TXD')
  assert.equal(codeComplete('K7QM-4TX'), false)
  assert.equal(codeComplete('k7qm 4txd'), true)
})

test('text that cannot be part of a code is noticed', () => {
  assert.equal(hasStrayCharacters('K7QM-4TXD'), false)
  assert.equal(hasStrayCharacters('K7QM 4TXD'), false)
  assert.equal(hasStrayCharacters('enrollment pending: K7QM-4TXD'), true)
  assert.equal(hasStrayCharacters('K7QM-4TXU'), true)
})

test('clock skew reads as a short phrase and only warns beyond two minutes', () => {
  assert.equal(skewWarning(undefined), undefined)
  assert.equal(skewWarning(0), undefined)
  assert.equal(skewWarning(119_000), undefined)
  assert.equal(skewWarning(-119_000), undefined)
  assert.equal(skewLabel(180_000), 'clock 3 min off')
  assert.equal(skewLabel(-181_000), 'clock 3 min off')
  assert.equal(skewLabel(7_200_000), 'clock 2 h off')
  assert.equal(skewLabel(3 * 86_400_000), 'clock 3 d off')
  assert.match(skewWarning(180_000)!, /ahead of the server's/)
  assert.match(skewWarning(-600_000)!, /behind the server's/)
  assert.match(skewWarning(600_000)!, /NTP|time synchronisation/)
})

test('pasting a whole log line finds the code in it', () => {
  assert.equal(fromPaste('time=2026 level=INFO msg="enrollment pending: approval code K7QM-4TXD - enter it in Continuum to approve this cluster" why=x'), 'K7QM-4TXD')
  assert.equal(fromPaste('k7qm-4txd'), 'K7QM-4TXD')
  assert.equal(fromPaste('  K7QM 4TXD '), 'K7QM-4TXD')
  assert.equal(fromPaste('K7QM4TXD'), 'K7QM-4TXD')
  assert.equal(fromPaste(''), '')
})
