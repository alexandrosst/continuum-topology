import { readdirSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { describe, expect, test } from 'vitest'
import { DASHBOARD_GROUPS, DASHBOARDS } from '@/features/fusion/lib/dashboards'

// The chart's dashboards are the source of truth: the list the UI offers is held to them.
const DIR = join(import.meta.dirname, '../backend/internal/chart/ikhnos-fusion/files/dashboards')
const shipped = readdirSync(DIR).filter((f) => f.endsWith('.json')).map((f) => JSON.parse(readFileSync(join(DIR, f), 'utf8')) as { uid: string; title: string; templating?: { list?: { name: string }[] } })

describe('the dashboards the UI points at', () => {
  test('are exactly the ones the chart ships, with the same titles', () => {
    expect(DASHBOARDS.map((d) => [d.uid, d.title]).sort()).toEqual(shipped.map((d) => [d.uid, d.title]).sort())
  })
  test('each is in a known group', () => {
    for (const d of DASHBOARDS) expect(DASHBOARD_GROUPS).toContain(d.group)
  })
  test('a dashboard that says its cluster picker can start on a cluster has a variable called cluster', () => {
    for (const d of DASHBOARDS.filter((x) => x.clusterPicker)) {
      expect(shipped.find((s) => s.uid === d.uid)?.templating?.list?.map((v) => v.name)).toContain('cluster')
    }
  })
})
