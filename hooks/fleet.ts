// Pure helpers for the codexfleet plugin (no engine calls), so they can be tested.
import type { FleetEntry, FleetSnapshot, FleetTask } from '../types'

/** Splits a command line like a shell would for simple quoting ("..." and '...'). */
export function splitArgs(s: string): string[] {
  const out: string[] = []
  let cur = ''
  let quote: string | null = null
  let has = false
  for (let i = 0; i < s.length; i++) {
    const c = s[i]
    if (quote) {
      if (c === quote) quote = null
      else if (c === '\\' && quote === '"' && i + 1 < s.length) cur += s[++i]
      else cur += c
    } else if (c === '"' || c === "'") {
      quote = c
      has = true
    } else if (c === ' ' || c === '\t' || c === '\n') {
      if (has || cur) out.push(cur)
      cur = ''
      has = false
    } else {
      cur += c
      has = true
    }
  }
  if (has || cur) out.push(cur)
  return out
}

export function duration(sec: number): string {
  if (!sec || sec < 0) return '-'
  const m = Math.floor(sec / 60)
  if (m >= 60) return `${Math.floor(m / 60)}h${String(m % 60).padStart(2, '0')}m`
  return `${m}m`
}

/** The status-line text over every shown fleet, or undefined when there is nothing to show. */
export function statusLine(fleets: FleetEntry[]): string | undefined {
  const counts: Record<string, number> = {}
  let running = 0
  let queued = 0
  let shown = 0
  for (const e of fleets) {
    if (!e.status) continue
    running += e.status.running
    queued += e.status.queued
    for (const t of e.status.tasks) {
      counts[t.status] = (counts[t.status] ?? 0) + 1
      if (t.status !== 'pending' && t.status !== 'cleaned') shown++
    }
  }
  if (shown === 0) return undefined
  const parts = [`${running} running`]
  if (queued) parts.push(`${queued} queued`)
  for (const k of ['done', 'failed', 'stopped', 'lost']) if (counts[k]) parts.push(`${counts[k]} ${k}`)
  return `codex fleet: ${parts.join(' · ')}`
}

/** Status per "fleet path|task", for change detection. */
export function statusMap(fleets: FleetEntry[]): Record<string, string> {
  const m: Record<string, string> = {}
  for (const e of fleets) for (const t of e.status?.tasks ?? []) m[`${e.path}|${t.task}`] = t.status
  return m
}

/** Tasks whose status changed to a terminal state since the previous snapshot. */
export function finished(prev: Record<string, string>, fleets: FleetEntry[]): FleetTask[] {
  const terminal = new Set(['done', 'failed', 'stopped', 'lost'])
  const out: FleetTask[] = []
  for (const e of fleets) {
    for (const t of e.status?.tasks ?? []) {
      const before = prev[`${e.path}|${t.task}`]
      if (terminal.has(t.status) && before !== undefined && before !== t.status) out.push(t)
    }
  }
  return out
}

/** A fleet's display name: its directory, with the home prefix shortened. */
export function fleetName(path: string, home?: string): string {
  let p = path.replace(/\/codexfleet\.toml$/, '')
  if (home && p.startsWith(home + '/')) p = '~' + p.slice(home.length)
  return p
}

/** Tasks worth a row: everything in a current fleet; only active ones elsewhere. */
export function visibleTasks(e: FleetEntry): FleetTask[] {
  const tasks = e.status?.tasks ?? []
  return e.current ? tasks : tasks.filter(t => t.status !== 'pending' && t.status !== 'cleaned')
}

export type { FleetSnapshot }

export function statusColor(s: string): 'success' | 'error' | 'warning' | 'suggestion' | 'inactive' {
  switch (s) {
    case 'running':
    case 'starting':
      return 'suggestion'
    case 'done':
      return 'success'
    case 'failed':
    case 'lost':
      return 'error'
    case 'queued':
    case 'stopped':
      return 'warning'
    default:
      return 'inactive'
  }
}

export function clip(s: string, n: number): string {
  if (n <= 1) return ''
  return s.length > n ? s.slice(0, n - 1) + '…' : s
}

export const SUBCOMMANDS = ['status', 'launch', 'stop', 'resume', 'note', 'adopt', 'fleets', 'logs', 'result', 'commits', 'diff', 'clean', 'doctor', 'render']
