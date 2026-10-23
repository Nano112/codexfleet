import { atom, read, update } from 'claude-code'
import type { EngineInterface, Register } from 'claude-code'

import type { FleetEntry, FleetTask, Machine, Snapshot } from '../types'
import {
  SUBCOMMANDS, activity, paneColumns, bandRows, clip, elapsed, eventText, findTask, finished, fleetName, heavyLabel,
  procLabel, splitArgs, statusColor, statusLine, statusMap, taskKey, tokens, visibleTasks,
} from './fleet'

const PANE = 'codexfleet'
const fleets = atom({ plugin: 'codexfleet', key: 'fleets' } as const, [])
const machine = atom({ plugin: 'codexfleet', key: 'machine' } as const, null)
const failure = atom({ plugin: 'codexfleet', key: 'error' } as const, null)
const selected = atom({ plugin: 'codexfleet', key: 'selected' } as const, null)

const HELP = `/fleet                 open the Codex fleet pane (task list, details, memory)
/fleet show TASK       open the pane on one task's detail
/fleet hide            close it
/fleet fleets          every fleet: here (cwd, $CODEXFLEET_FILE) and registered
/fleet status          status table of this directory's fleet
/fleet launch TASK...|--all [--fill]
/fleet stop TASK       kill its process tree (frees memory; resume later)
/fleet pause TASK      SIGSTOP its tree (stops growth, frees nothing) · unpause TASK
/fleet note TASK "message"     message a running agent
/fleet resume TASK "message"   continue an exited agent
/fleet logs TASK [--tail N]
/fleet result TASK | commits TASK | diff TASK [--stat]
/fleet clean TASK [--force]
/fleet doctor
Commands act on this directory's fleet; add -f FILE for another one.
For a full-screen dashboard in another terminal: codexfleet top
(watch blocks, so run it from Bash in the background: codexfleet watch --fill)`

type Cfg = { bin: string; fileArgs: string[] }

// Last seen status per fleet|task, to toast when one finishes (reset on reload).
let previous: Record<string, string> | null = null
let home: string | undefined

async function refresh($: EngineInterface, cfg: Cfg): Promise<FleetEntry[]> {
  let list: FleetEntry[] = []
  let mach: Machine | null = null
  let err: string | null = null
  try {
    // One snapshot: fleets here and every live registered one (or -f only),
    // with recent events, tokens, MCP counts and the memory report.
    const r = await $.process.run([cfg.bin, 'snapshot', '--events', '8', ...cfg.fileArgs], { timeoutMs: 20000 })
    if (r.exitCode === 0) {
      const snap = JSON.parse(r.stdout) as Snapshot
      list = snap.fleets ?? []
      mach = snap.machine ?? null
    } else {
      err = r.stderr.trim() || `codexfleet exited ${r.exitCode}`
    }
  } catch (e) {
    err = `cannot run ${cfg.bin}: ${String(e)}`
  }
  await update($, fleets, () => list)
  await update($, machine, () => mach)
  await update($, failure, () => err)
  // The summary rides at the end of the prompt's hint line (PromptHint
  // below): a plugin status line is drawn as a warning notice.
  $.ui.status(undefined)
  if (previous) {
    for (const t of finished(previous, list)) {
      $.ui.toast(`Codex ${t.task}: ${t.status}${t.commits > 0 ? `, ${t.commits} commit(s)` : ''}`)
    }
  }
  previous = statusMap(list)
  return list
}

/** Opens the pane and says what the engine did with it. */
async function openPane($: EngineInterface): Promise<string> {
  const opened = await $.ui.open({ id: PANE, title: 'Codex fleet' })
  if (!opened.isPlaced) return `Codex fleet pane is open but not drawn: ${opened.reason}`
  const pane = (await $.ui.panes()).find(p => p.id === PANE)
  if (pane && !pane.isShown) return 'Codex fleet pane opened behind another pane: ctrl+x tab switches to it.'
  return 'Codex fleet pane opened.'
}

export const register: Register = (on, options) => {
  const bin = String(options.binary || 'codexfleet')
  const fileArgs = options.fleetFile ? ['-f', String(options.fleetFile)] : []
  const pollMs = Math.max(3, Number(options.pollSeconds) || 15) * 1000
  const cfg: Cfg = { bin, fileArgs }

  on('session.start', async ($, e, next) => {
    await $.command.register({
      name: 'fleet',
      description: 'Codex agent fleet: pane, status, launch, stop, pause, note, resume, logs, result',
      argumentHint: '[show TASK|hide|status|launch|stop|pause|note|resume|logs|result|diff|clean|doctor] ...',
      immediate: true,
    })
    home = await $.env.get('HOME')
    await refresh($, cfg)
    $.clock.every(pollMs, () => void refresh($, cfg))
    // The in-session view is the band above the prompt (it draws at any
    // width); the pane is opened by /fleet.
    return next(e)
  })

  on('command.run', { command: 'fleet' }, async ($, e) => {
    const argv = splitArgs(e.args)
    const sub = argv[0] ?? ''
    if (sub === '' || sub === 'pane' || sub === 'show' || sub === 'open') {
      const list = await refresh($, cfg)
      const want = argv[1]
      if (want) {
        const hit = findTask(list, want)
        if (!hit) return { text: `No task ${want} in the shown fleets.` }
        await update($, selected, () => taskKey(hit.f, hit.t))
      }
      return { text: await openPane($) }
    }
    if (sub === 'hide' || sub === 'close') {
      await $.ui.close({ id: PANE })
      return { text: 'Codex fleet pane closed.' }
    }
    if (sub === 'help' || !SUBCOMMANDS.includes(sub)) {
      return { text: HELP }
    }
    const hasFile = argv.includes('-f') || argv.some(a => a.startsWith('-f='))
    let extra = fileArgs
    if (!hasFile && extra.length === 0 && sub !== 'fleets') {
      // No fleet in the session's directory: use the shown fleet that has the
      // named task, else the only one shown.
      const list = await read($, fleets)
      if (!list.some(f => f.current)) {
        const task = argv[1]
        const owner = list.filter(f => task && f.status?.tasks.some(t => t.task === task))
        const pick = owner.length === 1 ? owner[0] : list.length === 1 ? list[0] : undefined
        if (pick) extra = ['-f', pick.path]
      }
    }
    const args = hasFile ? argv : [...argv, ...extra]
    try {
      const r = await $.process.run([bin, ...args], { timeoutMs: 120000 })
      void refresh($, cfg)
      const out = [r.stdout.trimEnd(), r.stderr.trimEnd()].filter(Boolean).join('\n')
      return { text: '```\n' + (out || `(exit ${r.exitCode})`) + '\n```' }
    } catch (err) {
      return { text: `codexfleet failed: ${String(err)}` }
    }
  })

  // "codex 4 running · 2 done · 1 helper", dim at the end of the hint line.
  on('ui.render', { component: 'PromptHint' }, async ($, e, next) => {
    const err = await read($, failure)
    const line = err ? 'codex: snapshot failed (/fleet)' : statusLine(await read($, fleets))
    if (!line) return next(e)
    return next({ ...e, props: { ...e.props, tail: `${e.props.tail ?? ''}  ${line}` } })
  })

  // The default in-session view: one row per Codex agent, styled like Claude
  // Code's own background-agent list. Drawn whenever the pane is not on screen.
  on('ui.render', { component: 'AbovePrompt' }, async ($, e, next) => {
    if (e.props.hasSurvey) return next(e)
    const list = await read($, fleets)
    const width = Math.max(30, e.props.bodyColumns ?? 80)
    const rows = bandRows(list, width)
    if (rows.length === 0) return next(e)
    const pane = (await $.ui.panes()).find(p => p.id === PANE)
    if (pane?.isPlaced && pane.isShown) return next(e)
    const { Box, Text } = $.ui.resolve(e)
    const maxRows = Math.max(1, (e.props.maxRows ?? 12) - 1)
    const shown = rows.slice(0, maxRows)
    return (
      <Box flexDirection="column">
        {shown.map(r => (
          <Text key={r.key} wrap="truncate">
            <Text color={statusColor(r.status)}>{r.glyph} </Text>
            <Text dimColor={!r.alive}>{r.name}</Text>
            <Text dimColor>{`  ${r.activity}`}</Text>
            <Text dimColor>{`  ${r.tail}`}</Text>
          </Text>
        ))}
        {rows.length > shown.length && <Text dimColor>{`  … ${rows.length - shown.length} more (/fleet)`}</Text>}
      </Box>
    )
  })

  on('ui.render', { component: 'Pane', requestId: PANE }, async ($, e) => {
    const { Box, Text, Button } = $.ui.resolve(e)
    const list = await read($, fleets)
    const mach = await read($, machine)
    const err = await read($, failure)
    const sel = await read($, selected)
    const width = Math.max(30, e.props.bodyColumns ?? 80)
    if (err) {
      return <Text color="error">{clip(err, width * 3)}</Text>
    }
    if (list.length === 0) {
      return <Text dimColor>No Codex fleet here and none running. Run codexfleet init, or launch/adopt a fleet anywhere.</Text>
    }
    const all = list.flatMap(f => visibleTasks(f))
    const nameW = Math.min(14, Math.max(4, ...all.map(t => t.task.length)))
    const current = findTask(list, sel) ?? (() => {
      for (const f of list) for (const t of visibleTasks(f)) if (t.alive) return { f, t }
      const f = list.find(x => visibleTasks(x).length > 0)
      const t = f ? visibleTasks(f)[0] : undefined
      return f && t ? { f, t } : undefined
    })()
    const curKey = current ? taskKey(current.f, current.t) : null
    const select = (key: string) => () => void update($, selected, () => key)
    const cols = paneColumns(width)
    const head = `  ${'TASK'.padEnd(nameW)} ${'STATE'.padEnd(8)} ${cols.map(c => c.title.padStart(c.width)).join(' ')} ACTIVITY`
    const numbers = (t: FleetTask) => cols.map(c => c.value(t).padStart(c.width)).join(' ')
    const actW = Math.max(0, width - head.length + 'ACTIVITY'.length - 1)
    const d = current?.t.detail
    return (
      <Box flexDirection="column">
        {mach && (
          <Text wrap="truncate">
            <Text color={mach.total_ram_gb > 0 && mach.available_ram_gb / mach.total_ram_gb < 0.1 ? 'error' : mach.total_ram_gb > 0 && mach.available_ram_gb / mach.total_ram_gb < 0.2 ? 'warning' : 'success'}>
              {`RAM ${mach.available_ram_gb.toFixed(1)} GB free`}
            </Text>
            <Text dimColor>{mach.total_ram_gb ? ` of ${mach.total_ram_gb.toFixed(0)}` : ''}</Text>
            {mach.top.length > 0 && <Text dimColor>{` · top: ${mach.top.map(procLabel).join(', ')}`}</Text>}
          </Text>
        )}
        {list.map(f => (
          <Box flexDirection="column" key={f.path}>
            <Text bold wrap="truncate">
              {clip(fleetName(f.path, home), Math.max(10, width - 40))}
              <Text dimColor>
                {f.status ? `  ${f.status.running}/${f.status.max_concurrent} running · ${f.status.queued} queued${f.status.paused ? ` · ${f.status.paused} paused` : ''}` : ''}
              </Text>
            </Text>
            {f.error && <Text color="error">{clip(f.error, width)}</Text>}
            {visibleTasks(f).length > 0 && (
              <Text dimColor wrap="truncate">{clip(head, width)}</Text>
            )}
            {visibleTasks(f).map(t => {
              const key = taskKey(f, t)
              const isSel = key === curKey
              return (
                <Button key={key} plain dimColor={!isSel} onPress={select(key)}>
                  <Text bold={isSel}>{`${isSel ? '▸' : ' '} ${clip(t.task, nameW).padEnd(nameW)} `}</Text>
                  <Text color={statusColor(t.status)}>{`${t.status.padEnd(8)} `}</Text>
                  <Text dimColor>{cols.length > 0 ? `${numbers(t)} ` : ''}</Text>
                  <Text>{clip(activity(t), actW)}</Text>
                </Button>
              )
            })}
          </Box>
        ))}
        {current && (
          <Box flexDirection="column" marginTop={1}>
            <Text wrap="truncate">
              <Text bold>{`── ${current.t.task} `}</Text>
              <Text color={statusColor(current.t.status)}>{current.t.status}</Text>
              <Text dimColor>
                {[
                  current.t.alive && current.t.pid ? `pid ${current.t.pid}` : '',
                  current.t.branch ?? '',
                  current.t.tokens ? `tokens in ${tokens(current.t.tokens.input_tokens)} out ${tokens(current.t.tokens.output_tokens)}` : '',
                  current.t.stream ? `cmds ${current.t.stream.commands}${current.t.stream.commands_failed ? ` (${current.t.stream.commands_failed} failed)` : ''}` : '',
                  current.t.stream ? `mcp ${current.t.stream.tool_calls}${current.t.stream.mcp_failed ? ` (${current.t.stream.mcp_failed} failed)` : ''}` : '',
                  current.t.stream?.helpers_spawned ? `helpers ${current.t.stream.helpers_active}/${current.t.stream.helpers_spawned}` : '',
                  heavyLabel(current.t),
                ].filter(Boolean).map(s => ` · ${s}`).join('')}
              </Text>
            </Text>
            {(d?.recent ?? []).map((ev, i) => (
              <Text key={`ev${i}`} wrap="truncate">
                <Text color={ev.failed ? 'error' : ev.kind === 'agent' ? undefined : 'inactive'}>{`  ${ev.kind.padEnd(7)} `}</Text>
                <Text dimColor={ev.kind !== 'agent' && !ev.failed} color={ev.failed ? 'error' : undefined}>{clip(eventText(ev), width - 12)}</Text>
              </Text>
            ))}
            {d && d.commits.length > 0 && <Text bold>{`  commits (${current.t.commits})`}</Text>}
            {(d?.commits ?? []).slice(0, 5).map((c, i) => (
              <Text key={`c${i}`} dimColor wrap="truncate">{`    ${clip(c, width - 6)}`}</Text>
            ))}
            {d && d.dirty_files.length > 0 && <Text bold>{`  dirty (${Math.max(0, current.t.dirty)} tracked, ${Math.max(0, current.t.untracked)} untracked)`}</Text>}
            {(d?.dirty_files ?? []).slice(0, 4).map((c, i) => (
              <Text key={`d${i}`} dimColor wrap="truncate">{`    ${clip(c, width - 6)}`}</Text>
            ))}
            {d && !current.t.alive && d.final_message !== '' && <Text bold>  final message</Text>}
            {d && !current.t.alive && d.final_message !== '' &&
              d.final_message.split('\n').filter(l => l.trim() !== '').slice(0, 8).map((l, i) => (
                <Text key={`m${i}`} wrap="truncate">{`    ${clip(l, width - 6)}`}</Text>
              ))}
            <Text dimColor wrap="truncate">{`  /fleet logs ${current.t.task} · /fleet result ${current.t.task} · /fleet note ${current.t.task} "…" · codexfleet top`}</Text>
          </Box>
        )}
      </Box>
    )
  })
}


