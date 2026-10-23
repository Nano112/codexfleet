import { expect, test } from 'claude-code/testing'

import { finished, fleetName, splitArgs, statusLine, statusMap, visibleTasks } from '../hooks/fleet'
import type { FleetEntry, FleetSnapshot } from '../types'

const snap: FleetSnapshot = {
  config: '/r/codexfleet.toml',
  max_concurrent: 2,
  running: 1,
  queued: 1,
  available_ram_gb: 12.5,
  time: '2026-10-23T10:00:00Z',
  tasks: [
    { task: 'refs', status: 'running', alive: true, runtime_sec: 3700, idle_sec: 60, commits: 2, dirty: 1, untracked: 0, last_event: 'cmd: cargo test', has_result: false, runs: 1 },
    { task: 'cells', status: 'done', alive: false, runtime_sec: 600, idle_sec: 0, commits: 3, dirty: 0, untracked: 0, last_event: 'turn.completed', has_result: true, runs: 1 },
    { task: 'flips', status: 'queued', alive: false, runtime_sec: 0, idle_sec: 0, commits: -1, dirty: -1, untracked: -1, has_result: false, runs: 0 },
  ],
}
const other: FleetSnapshot = {
  ...snap,
  config: '/home/u/elsewhere/codexfleet.toml',
  running: 1,
  queued: 0,
  tasks: [
    { task: 'nested', status: 'running', alive: true, runtime_sec: 120, idle_sec: 5, commits: 0, dirty: 0, untracked: 0, last_event: 'mcp: analyzer.open', has_result: false, runs: 1 },
    { task: 'idle', status: 'pending', alive: false, runtime_sec: 0, idle_sec: 0, commits: -1, dirty: -1, untracked: -1, has_result: false, runs: 0 },
  ],
}
const fleets: FleetEntry[] = [
  { path: '/r/codexfleet.toml', current: true, live: true, status: snap },
  { path: '/home/u/elsewhere/codexfleet.toml', current: false, live: true, status: other },
]

test('splitArgs honours quotes', () => {
  expect(splitArgs(`resume refs "fix the \\"test\\" now" --x`)).toEqual(['resume', 'refs', 'fix the "test" now', '--x'])
  expect(splitArgs(`a '' b`)).toEqual(['a', '', 'b'])
})

test('status line, change detection and grouping helpers', () => {
  expect(statusLine(fleets)).toBe('codex fleet: 2 running · 1 queued · 1 done')
  expect(statusLine([])).toBe(undefined)
  const before = statusMap(fleets)
  before['/r/codexfleet.toml|cells'] = 'running'
  expect(finished(before, fleets).map(t => t.task)).toEqual(['cells'])
  expect(fleetName('/home/u/elsewhere/codexfleet.toml', '/home/u')).toBe('~/elsewhere')
  expect(visibleTasks(fleets[1]!).map(t => t.task)).toEqual(['nested'])
  expect(visibleTasks(fleets[0]!).length).toBe(3)
})

test('/fleet status runs the binary and the pane groups every fleet', async ($, on) => {
  const calls: string[][] = []
  on('process.run', ($, e) => {
    calls.push([...e.argv])
    const isFleets = e.argv[1] === 'fleets'
    return { value: { exitCode: 0, stdout: isFleets ? JSON.stringify({ registry: '/x', fleets }) : 'TASK STATE\nrefs running', stderr: '', isStdoutTruncated: false, isStderrTruncated: false } }
  })
  on('ui.status', () => ({ value: undefined }))
  on('ui.toast', () => ({ value: undefined }))
  on('ui.open', () => ({ value: { isPlaced: true } }))
  const out = await $.command.run({ command: 'fleet', args: 'status' } as Parameters<typeof $.command.run>[0])
  expect(out.text).toContain('refs running')
  expect(calls.some(a => a[0] === 'codexfleet' && a[1] === 'status')).toBe(true)

  await $.command.run({ command: 'fleet', args: '' } as Parameters<typeof $.command.run>[0])
  expect(calls.some(a => a[1] === 'fleets' && a.includes('--live'))).toBe(true)
  for (const surface of ['terminal', 'desktop'] as const) {
    const ui = await $.ui.mount({ plugin: 'codexfleet', surface, component: 'Pane', requestId: 'codexfleet', props: { bodyColumns: 110 } as never })
    for (const name of ['refs', 'cells', 'flips', 'nested', 'elsewhere']) {
      expect(await ui.find({ text: new RegExp(name) })).toBeDefined()
    }
    expect(await ui.find({ text: /\bidle\b/ })).toBeUndefined()
    await ui.unmount()
  }
})
