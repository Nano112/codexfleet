// Pure helpers for the codexfleet plugin (no engine calls), so they can be tested.
import type { FleetEntry, FleetEvent, FleetSnapshot, FleetTask, Machine, Proc } from '../types'

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
  let helpers = 0
  for (const e of fleets) {
    if (!e.status) continue
    running += e.status.running
    queued += e.status.queued
    for (const t of e.status.tasks) {
      counts[t.status] = (counts[t.status] ?? 0) + 1
      if (t.status !== 'pending' && t.status !== 'cleaned') shown++
      if (t.alive) helpers += t.stream?.helpers_active ?? 0
    }
  }
  if (shown === 0) return undefined
  const parts = [`${running} running`]
  if (queued) parts.push(`${queued} queued`)
  for (const k of ['paused', 'done', 'failed', 'stopped', 'lost']) if (counts[k]) parts.push(`${counts[k]} ${k}`)
  if (helpers) parts.push(`${helpers} helper${helpers === 1 ? '' : 's'}`)
  return `codex ${parts.join(' · ')}`
}

/** Runtime the way Claude Code's agent list writes it: 2h 20m 30s, 7m 3s. */
export function elapsed(sec: number): string {
  if (!sec || sec < 0) return '-'
  const h = Math.floor(sec / 3600)
  const m = Math.floor((sec % 3600) / 60)
  const s = Math.floor(sec % 60)
  if (h > 0) return `${h}h ${m}m ${s}s`
  if (m > 0) return `${m}m ${s}s`
  return `${s}s`
}

/** 950, 12.3k, 453.9k, 1.2M. */
export function tokens(n: number): string {
  if (n < 1000) return String(n)
  if (n < 1_000_000) return `${(n / 1000).toFixed(1)}k`
  return `${(n / 1_000_000).toFixed(1)}M`
}

/** The few words a row says the task is doing. */
export function activity(t: FleetTask): string {
  if (t.error && !t.alive && t.status !== 'done') return `error: ${t.error}`
  if (t.activity) return t.activity
  if (t.status === 'queued') return 'waiting for a slot'
  if (t.status === 'pending') return 'not started'
  return t.last_event ?? ''
}

/** ● while it runs, ○ otherwise (as the native agent list). */
export function glyph(t: FleetTask): string {
  return t.alive && t.status !== 'paused' ? '●' : '○'
}

/** "37m 10s · ↓ 95.0k tokens · 6 commits" (the parts that apply). */
export function rowTail(t: FleetTask): string {
  const parts: string[] = []
  if (t.runtime_sec > 0) parts.push(elapsed(t.runtime_sec))
  if (t.tokens) parts.push(`↓ ${tokens(t.tokens.output_tokens)} tokens`)
  if (t.commits > 0) parts.push(`${t.commits} commit${t.commits === 1 ? '' : 's'}`)
  return parts.join(' · ')
}

export type BandRow = { key: string; glyph: string; name: string; activity: string; tail: string; status: string; alive: boolean }

/** Rows of the native-style list, padded so the columns align in width cells. */
export function bandRows(fleets: FleetEntry[], width: number): BandRow[] {
  const tasks = fleets.flatMap(f => visibleTasks(f).map(t => ({ f, t })))
  const nameW = Math.min(22, Math.max(...tasks.map(x => `codex ${x.t.task}`.length), 0))
  const tails = tasks.map(x => rowTail(x.t))
  const tailW = Math.max(0, ...tails.map(s => s.length))
  const actW = Math.max(0, width - (2 + nameW + 2 + 2 + tailW))
  return tasks.map((x, i) => ({
    key: taskKey(x.f, x.t),
    glyph: glyph(x.t),
    name: clip(`codex ${x.t.task}`, nameW).padEnd(nameW),
    activity: actW >= 8 ? clip(activity(x.t), actW).padEnd(actW) : '',
    tail: (tails[i] ?? '').padStart(tailW),
    status: x.t.status,
    alive: x.t.alive,
  }))
}

export function taskKey(f: FleetEntry, t: FleetTask): string {
  return `${f.path}|${t.task}`
}

/** Finds a task by key, or by bare name (first match). */
export function findTask(fleets: FleetEntry[], sel: string | null): { f: FleetEntry; t: FleetTask } | undefined {
  if (!sel) return undefined
  for (const f of fleets) for (const t of visibleTasks(f)) if (taskKey(f, t) === sel || t.task === sel) return { f, t }
  return undefined
}

/** "java 6.1G (records)" */
export function procLabel(p: Proc): string {
  return `${p.name} ${p.rss_gb.toFixed(1)}G (${p.task ?? 'external'})`
}

export function machineLine(m: Machine | null): string {
  if (!m) return ''
  const top = m.top.map(procLabel).join(', ')
  return `RAM ${m.available_ram_gb.toFixed(1)} GB free${m.total_ram_gb ? ` of ${m.total_ram_gb.toFixed(0)}` : ''}${top ? ` · top: ${top}` : ''}`
}

/** One event as a single line: "[1] $ cargo test", "analyzer.open", the message start. */
export function eventText(e: FleetEvent): string {
  const exit = e.exit !== undefined ? `[${e.exit}] ` : ''
  const body = (e.line || e.text || '').replace(/\s+/g, ' ').trim()
  return exit + (e.kind === 'cmd' ? `$ ${body}` : body)
}

export function heavyLabel(t: FleetTask): string {
  const hs = t.heavy ?? []
  if (hs.length === 0) return ''
  const gb = hs.reduce((a, p) => a + p.rss_gb, 0)
  return `${hs.length} heavy ${gb.toFixed(1)}G`
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
    case 'paused':
      return 'warning'
    default:
      return 'inactive'
  }
}

export function clip(s: string, n: number): string {
  if (n <= 1) return ''
  return s.length > n ? s.slice(0, n - 1) + '…' : s
}

export const SUBCOMMANDS = ['status', 'launch', 'stop', 'resume', 'note', 'pause', 'unpause', 'adopt', 'fleets', 'snapshot', 'logs', 'result', 'commits', 'diff', 'clean', 'doctor', 'render']

export type PaneColumn = { title: string; width: number; value: (t: FleetTask) => string }

/** The pane table's number columns that fit in width (the activity keeps 20+). */
export function paneColumns(width: number): PaneColumn[] {
  const all: (PaneColumn & { min: number })[] = [
    { title: 'RUN', width: 9, min: 0, value: t => elapsed(t.runtime_sec) },
    { title: 'CMT', width: 3, min: 70, value: t => (t.commits >= 0 ? String(t.commits) : '-') },
    { title: 'TOKENS', width: 7, min: 80, value: t => (t.tokens ? tokens(t.tokens.output_tokens) : '-') },
    { title: 'IDLE', width: 5, min: 90, value: t => (t.alive ? (elapsed(t.idle_sec).split(' ')[0] ?? '-') : '-') },
    { title: 'DIRTY', width: 6, min: 100, value: t => (t.dirty < 0 ? '-' : t.untracked > 0 ? `${t.dirty}+${t.untracked}?` : String(t.dirty)) },
    { title: 'MCP', width: 6, min: 110, value: t => {
      const s = t.stream
      if (!s || s.tool_calls === 0) return '-'
      return s.mcp_failed > 0 ? `${s.tool_calls}/${s.mcp_failed}✗` : String(s.tool_calls)
    } },
  ]
  if (width < 50) return []
  return all.filter(c => width >= c.min).map(({ title, width: w, value }) => ({ title, width: w, value }))
}
