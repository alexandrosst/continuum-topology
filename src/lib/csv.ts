/**
 * Turning a table already loaded in the browser into a CSV file the person can open in a spreadsheet.
 * Client-side only: nothing is sent back to the server, it just serialises what is already on screen.
 */

/** RFC 4180 field escaping: wrap in quotes (doubling any inner quote) whenever that's required to stay a single field. */
function csvField(value: string | number | null | undefined): string {
  const s = value === null || value === undefined ? '' : String(value)
  return /[",\r\n]/.test(s) ? `"${s.replace(/"/g, '""')}"` : s
}

/** Header row + one row per record, CRLF-terminated as RFC 4180 expects. */
export function toCsv(headers: string[], rows: (string | number | null | undefined)[][]): string {
  const lines = [headers.map(csvField).join(','), ...rows.map((row) => row.map(csvField).join(','))]
  return lines.join('\r\n') + '\r\n'
}

/** Triggers a browser download of `csv` as `filename`, without a round trip to any server. */
export function downloadCsv(filename: string, csv: string): void {
  // A leading BOM keeps Excel from mis-guessing the encoding on non-ASCII content.
  const blob = new Blob(['﻿', csv], { type: 'text/csv;charset=utf-8;' })
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = filename
  a.click()
  URL.revokeObjectURL(url)
}
