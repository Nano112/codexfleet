import { expect, test } from 'claude-code/testing'

import { activity, bandRows, elapsed, eventText, finished, fleetName, paneColumns, rowTail, splitArgs, statusLine, statusMap, tokens, visibleTasks } from '../hooks/fleet'
import type { FleetEntry, FleetSnapshot, FleetTask, Snapshot } from '../types'

const detail = {
  recent: [
    { kind: 'agent', text: 'Parser rewritten. Running tests next.', line: 'Parser rewritten. Running tests next.' },
    { kind: 'cmd', text: '$ cargo test  [exit 101]', line: 'cargo test', exit: 101, failed: true },
    { kind: 'mcp', text: 'analyzer.open  [completed]', line: 'analyzer.open' },
  ],
  commits: ['abc1234 parser: handle nested blocks (5 minutes ago)'],
  dirty_files: [' M src/parser.rs'],
  final_message: '',
}
const refs: FleetTask = {
  task: 'refs', status: 'running', alive: true, pid: 4242, branch: 'codex/refs', runtime_sec: 3700, idle_sec: 60, commits: 2, dirty: 1, untracked: 0,
  last_event: 'cmd: cargo test', activity: 'Parser rewritten', has_result: false, runs: 1,
  tokens: { input_tokens: 2_500_000, cached_input_tokens: 2_000_000, output_tokens: 45_300, reasoning_output_tokens: 100 },
  stream: { events: 9, turns_completed: 0, commands: 4, commands_failed: 1, tool_calls: 3, mcp_failed: 1, agent_messages: 1, helpers_spawned: 2, helpers_active: 1, in_flight: 1 },
  heavy: [{ pid: 77, name: 'java', rss_gb: 4.2, task: 'refs', fleet: '/r/codexfleet.toml' }],
  detail,
}
const cells: FleetTask = {
  task: 'cells', status: 'done', alive: false, runtime_sec: 600, idle_sec: 0, commits: 3, dirty: 0, untracked: 0, last_event: 'turn.completed',
  activity: 'Cells decoded', has_result: true, runs: 1, detail: { ...detail, final_message: 'All cells decoded.\nTests pass.' },
}
const snap: FleetSnapshot = {
  config: '/r/codexfleet.toml',
  max_concurrent: 2,
  running: 1,
  queued: 1,
  available_ram_gb: 12.5,
  time: '2026-10-23T10:00:00Z',
  tasks: [
    refs,
    cells,
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
const snapshot: Snapshot = {
  time: '2026-10-23T10:00:00Z',
  machine: { available_ram_gb: 3.1, total_ram_gb: 64, top: [{ pid: 9, name: 'qemu', rss_gb: 24.0 }, { pid: 77, name: 'java', rss_gb: 4.2, task: 'refs' }] },
  fleets,
}

test('splitArgs honours quotes', () => {
  expect(splitArgs(`resume refs "fix the \\"test\\" now" --x`)).toEqual(['resume', 'refs', 'fix the "test" now', '--x'])
  expect(splitArgs(`a '' b`)).toEqual(['a', '', 'b'])
})

test('status line, change detection and grouping helpers', () => {
  expect(statusLine(fleets)).toBe('codex 2 running · 1 queued · 1 done · 1 helper')
  expect(statusLine([])).toBe(undefined)
  const before = statusMap(fleets)
  before['/r/codexfleet.toml|cells'] = 'running'
  expect(finished(before, fleets).map(t => t.task)).toEqual(['cells'])
  expect(fleetName('/home/u/elsewhere/codexfleet.toml', '/home/u')).toBe('~/elsewhere')
  expect(visibleTasks(fleets[1]!).map(t => t.task)).toEqual(['nested'])
  expect(visibleTasks(fleets[0]!).length).toBe(3)
})

test('native-style rows: glyph, name, activity, aligned tail', () => {
  expect(elapsed(3700)).toBe('1h 1m 40s')
  expect(elapsed(423)).toBe('7m 3s')
  expect(tokens(453_900)).toBe('453.9k')
  expect(tokens(2_500_000)).toBe('2.5M')
  expect(rowTail(refs)).toBe('1h 1m 40s · ↓ 45.3k tokens · 2 commits')
  expect(activity({ ...cells, status: 'failed', error: 'boom' })).toBe('error: boom')
  expect(activity(snap.tasks[2]!)).toBe('waiting for a slot')
  const rows = bandRows(fleets, 90)
  expect(rows.map(r => r.glyph)).toEqual(['●', '○', '○', '●'])
  expect(rows[0]!.name.trim()).toBe('codex refs')
  // Every row is the same width, and fits.
  const widths = rows.map(r => `${r.glyph} ${r.name}  ${r.activity}  ${r.tail}`.length)
  expect(new Set(widths).size).toBe(1)
  expect(widths[0]! <= 90).toBe(true)
  expect(eventText(detail.recent[1]!)).toBe('[101] $ cargo test')
  expect(paneColumns(40)).toEqual([])
  expect(paneColumns(120).map(c => c.title)).toEqual(['RUN', 'CMT', 'TOKENS', 'IDLE', 'DIRTY', 'MCP'])
})

type Body = Extract<Parameters<typeof test>[1], (...a: never[]) => unknown>
type On = Parameters<Body>[1]
type RunArg = Parameters<Parameters<Body>[0]['command']['run']>[0]

function answer(on: On, calls: string[][], placed = true) {
  on('process.run', ($, e) => {
    calls.push([...e.argv])
    const isSnap = e.argv[1] === 'snapshot'
    return { value: { exitCode: 0, stdout: isSnap ? JSON.stringify(snapshot) : 'TASK STATE\nrefs running', stderr: '', isStdoutTruncated: false, isStderrTruncated: false } }
  })
  on('ui.status', () => ({ value: undefined }))
  on('ui.toast', () => ({ value: undefined }))
  on('ui.open', () => ({ value: placed ? { isPlaced: true } : { isPlaced: false, reason: 'unasked panes wait under 144 columns (now 100)' } }))
  on('ui.panes', () => ({ value: placed ? [{ id: 'codexfleet', title: 'Codex fleet', isShown: true, isFocused: false, isPlaced: true }] : [] }))
}

test('/fleet status runs the binary; the pane lists tasks, memory and the selected detail', async ($, on) => {
  const calls: string[][] = []
  answer(on, calls)
  const out = await $.command.run({ command: 'fleet', args: 'status' } as RunArg)
  expect(out.text).toContain('refs running')
  expect(calls.some(a => a[0] === 'codexfleet' && a[1] === 'status')).toBe(true)

  const opened = await $.command.run({ command: 'fleet', args: '' } as RunArg)
  expect(opened.text).toBe('Codex fleet pane opened.')
  expect(calls.some(a => a[1] === 'snapshot')).toBe(true)
  for (const surface of ['terminal', 'desktop'] as const) {
    const ui = await $.ui.mount({ plugin: 'codexfleet', surface, component: 'Pane', requestId: 'codexfleet', props: { bodyColumns: 120 } as never })
    for (const text of [/refs/, /cells/, /flips/, /nested/, /elsewhere/, /RAM 3\.1 GB free/, /qemu 24\.0G \(external\)/, /java 4\.2G \(refs\)/,
      /Parser rewritten/, /\[101\] \$ cargo test/, /abc1234/, /src\/parser\.rs/, /pid 4242/, /helpers 1\/2/]) {
      expect(await ui.find({ text })).toBeDefined()
    }
    expect(await ui.find({ text: /\bidle\b/ })).toBeUndefined()
    // Selecting another task shows its final message.
    await ui.press({ key: '/r/codexfleet.toml|cells' })
    expect(await ui.find({ text: /All cells decoded/ })).toBeDefined()
    await ui.press({ key: '/r/codexfleet.toml|refs' })
    await ui.unmount()
  }
})

test('/fleet show TASK selects it, and says when the pane is not drawn', async ($, on) => {
  const calls: string[][] = []
  answer(on, calls, false)
  const out = await $.command.run({ command: 'fleet', args: 'show cells' } as RunArg)
  expect(out.text).toContain('not drawn: unasked panes wait')
  const missing = await $.command.run({ command: 'fleet', args: 'show nope' } as RunArg)
  expect(missing.text).toContain('No task nope')
  const ui = await $.ui.mount({ plugin: 'codexfleet', surface: 'terminal', component: 'Pane', requestId: 'codexfleet', props: { bodyColumns: 60 } as never })
  expect(await ui.find({ text: /All cells decoded/ })).toBeDefined()
  await ui.unmount()
})

test('the band lists every agent like the native agent list while the pane is not shown', async ($, on) => {
  const calls: string[][] = []
  answer(on, calls, false)
  await $.command.run({ command: 'fleet', args: '' } as RunArg)
  const ui = await $.ui.mount({ plugin: 'codexfleet', surface: 'terminal', component: 'AbovePrompt', props: { bodyColumns: 100, maxRows: 10, hasSurvey: false } as never })
  for (const text of [/● codex refs/, /○ codex cells/, /codex nested/, /1h 1m 40s · ↓ 45\.3k tokens · 2 commits/, /Parser rewritten/]) {
    expect(await ui.find({ text })).toBeDefined()
  }
  await ui.unmount()
})

test('the hint line carries the summary', async ($, on) => {
  const calls: string[][] = []
  answer(on, calls)
  let tail: string | undefined
  // Beneath the plugin: the engine's own hint line, which draws the tail.
  on('ui.render', { component: 'PromptHint' }, ($, e) => {
    tail = e.props.tail
    const { Text } = $.ui.resolve(e)
    return <Text>{`${e.props.hint}${e.props.tail ?? ''}`}</Text>
  })
  await $.command.run({ command: 'fleet', args: '' } as RunArg)
  const ui = await $.ui.mount({ plugin: 'codexfleet', surface: 'terminal', component: 'PromptHint', props: { hint: '? for shortcuts', isDraft: false, isWorking: false } as never })
  expect(await ui.find({ text: /codex 2 running · 1 queued · 1 done · 1 helper/ })).toBeDefined()
  expect(tail).toBe('  codex 2 running · 1 queued · 1 done · 1 helper')
  await ui.unmount()
})
