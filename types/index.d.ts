export type FleetTask = {
  task: string
  status: string
  alive: boolean
  runtime_sec: number
  idle_sec: number
  commits: number
  dirty: number
  untracked: number
  last_event?: string
  error?: string
  has_result: boolean
  runs: number
}

export type FleetSnapshot = {
  config: string
  max_concurrent: number
  running: number
  queued: number
  available_ram_gb: number
  time: string
  tasks: FleetTask[]
}

/** One fleet from `codexfleet fleets --json`. */
export type FleetEntry = {
  path: string
  current: boolean
  live: boolean
  error?: string
  status?: FleetSnapshot
}

declare module 'claude-code' {
  interface PluginState {
    codexfleet: { fleets: FleetEntry[]; error: string | null }
  }
}
