import { atom, read, update } from 'claude-code'
import type { EngineInterface, Register } from 'claude-code'

import type { FleetEntry } from '../types'
import { SUBCOMMANDS, clip, duration, finished, fleetName, splitArgs, statusColor, statusLine, statusMap, visibleTasks } from './fleet'

const PANE = 'codexfleet'
const fleets = atom({ plugin: 'codexfleet', key: 'fleets' } as const, [])
const failure = atom({ plugin: 'codexfleet', key: 'error' } as const, null)

const HELP = `/fleet                 open the Codex fleet pane
/fleet hide            close it
/fleet fleets          every fleet: here (cwd, $CODEXFLEET_FILE) and registered
/fleet status          status table of this directory's fleet
/fleet launch TASK...|--all [--fill]
/fleet stop TASK
/fleet note TASK "message"     message a running agent
/fleet resume TASK "message"   continue an exited agent
/fleet logs TASK [--tail N]
/fleet result TASK | commits TASK | diff TASK [--stat]
/fleet clean TASK [--force]
/fleet doctor
Commands act on this directory's fleet; add -f FILE for another one.
(watch blocks, so run it from Bash in the background: codexfleet watch --fill)`

type Cfg = { bin: string; fileArgs: string[] }

// Last seen status per fleet|task, to toast when one finishes (reset on reload).
let previous: Record<string, string> | null = null
let home: string | undefined

async function refresh($: EngineInterface, cfg: Cfg): Promise<FleetEntry[]> {
  let list: FleetEntry[] = []
  let err: string | null = null
  try {
    // Discovery: -f (plugin option), the session's cwd and parents,
    // $CODEXFLEET_FILE, then the user registry (live fleets only).
    const r = await $.process.run([cfg.bin, 'fleets', '--json', '--live', ...cfg.fileArgs], { timeoutMs: 20000 })
    if (r.exitCode === 0) {
      list = (JSON.parse(r.stdout) as { fleets: FleetEntry[] | null }).fleets ?? []
    } else {
      err = r.stderr.trim() || `codexfleet exited ${r.exitCode}`
    }
  } catch (e) {
    err = `cannot run ${cfg.bin}: ${String(e)}`
  }
  await update($, fleets, () => list)
  await update($, failure, () => err)
  $.ui.status(statusLine(list))
  if (previous) {
    for (const t of finished(previous, list)) {
      $.ui.toast(`Codex ${t.task}: ${t.status}${t.commits > 0 ? `, ${t.commits} commit(s)` : ''}`)
    }
  }
  previous = statusMap(list)
  return list
}

export const register: Register = (on, options) => {
  const bin = String(options.binary || 'codexfleet')
  const fileArgs = options.fleetFile ? ['-f', String(options.fleetFile)] : []
  const pollMs = Math.max(3, Number(options.pollSeconds) || 15) * 1000
  const cfg: Cfg = { bin, fileArgs }

  on('session.start', async ($, e, next) => {
    await $.command.register({
      name: 'fleet',
      description: 'Codex agent fleet: pane, status, launch, stop, note, resume, logs, result',
      argumentHint: '[fleets|status|launch|stop|note|resume|logs|result|diff|clean|doctor|hide] ...',
      immediate: true,
    })
    home = await $.env.get('HOME')
    const list = await refresh($, cfg)
    $.clock.every(pollMs, () => void refresh($, cfg))
    if (list.some(f => (f.status?.running ?? 0) > 0)) void $.ui.open({ id: PANE, title: 'Codex fleet' })
    return next(e)
  })

  on('command.run', { command: 'fleet' }, async ($, e) => {
    const argv = splitArgs(e.args)
    const sub = argv[0] ?? ''
    if (sub === '' || sub === 'pane' || sub === 'show') {
      await refresh($, cfg)
      await $.ui.open({ id: PANE, title: 'Codex fleet' })
      return { text: 'Codex fleet pane opened.' }
    }
    if (sub === 'hide') {
      await $.ui.close({ id: PANE })
      return { text: 'Codex fleet pane closed.' }
    }
    if (sub === 'help' || !SUBCOMMANDS.includes(sub)) {
      return { text: HELP }
    }
    const hasFile = argv.includes('-f') || argv.some(a => a.startsWith('-f='))
    const args = hasFile ? argv : [...argv, ...fileArgs]
    try {
      const r = await $.process.run([bin, ...args], { timeoutMs: 120000 })
      void refresh($, cfg)
      const out = [r.stdout.trimEnd(), r.stderr.trimEnd()].filter(Boolean).join('\n')
      return { text: '```\n' + (out || `(exit ${r.exitCode})`) + '\n```' }
    } catch (err) {
      return { text: `codexfleet failed: ${String(err)}` }
    }
  })

  on('ui.render', { component: 'Pane', requestId: PANE }, async ($, e) => {
    const { Box, Text } = $.ui.resolve(e)
    const list = await read($, fleets)
    const err = await read($, failure)
    const width = Math.max(20, e.props.bodyColumns ?? 80)
    if (err) {
      return <Text color="error">{clip(err, width * 3)}</Text>
    }
    if (list.length === 0) {
      return <Text dimColor>No Codex fleet here and none running. Run codexfleet init, or launch/adopt a fleet anywhere.</Text>
    }
    const all = list.flatMap(f => visibleTasks(f))
    const nameW = Math.min(14, Math.max(4, ...all.map(t => t.task.length)))
    const lastW = Math.max(0, width - (nameW + 1 + 8 + 1 + 6 + 1 + 5 + 1 + 4 + 1))
    return (
      <Box flexDirection="column">
        {list.map(f => (
          <Box flexDirection="column">
            <Text bold>
              {clip(fleetName(f.path, home), Math.max(10, width - 30))}
              <Text dimColor>
                {f.status ? `  ${f.status.running}/${f.status.max_concurrent} running · ${f.status.queued} queued · ${f.status.available_ram_gb.toFixed(1)} GB free` : ''}
              </Text>
            </Text>
            {f.error && <Text color="error">{clip(f.error, width)}</Text>}
            {visibleTasks(f).map(t => (
              <Text>
                <Text bold>{clip(t.task, nameW).padEnd(nameW)} </Text>
                <Text color={statusColor(t.status)}>{t.status.padEnd(8)} </Text>
                <Text dimColor>
                  {duration(t.runtime_sec).padStart(6)} {(t.alive ? duration(t.idle_sec) : '-').padStart(5)}{' '}
                  {(t.commits >= 0 ? `${t.commits}c` : '-').padStart(4)}{' '}
                </Text>
                {clip(t.error && !t.alive && t.status !== 'done' ? `error: ${t.error}` : t.last_event ?? '', lastW)}
              </Text>
            ))}
          </Box>
        ))}
      </Box>
    )
  })
}
