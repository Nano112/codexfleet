/** Token usage, as Codex reports it. */
export type Tokens = {
  input_tokens: number
  cached_input_tokens: number
  output_tokens: number
  reasoning_output_tokens: number
}

/** Counters over a task's --json event stream. */
export type Stream = {
  session_id?: string
  events: number
  turns_completed: number
  commands: number
  commands_failed: number
  tool_calls: number
  mcp_failed: number
  mcp_by_server?: Record<string, number>
  agent_messages: number
  helpers_spawned: number
  helpers_active: number
  in_flight: number
  activity?: string
  last_event?: string
}

/** One readable event (`codexfleet snapshot`'s recent events). */
export type FleetEvent = {
  kind: string
  text: string
  line: string
  failed?: boolean
  exit?: number
}

/** A process in the memory report. */
export type Proc = {
  pid: number
  name: string
  rss_gb: number
  /** Owning task; absent = external. */
  task?: string
  fleet?: string
}

export type TaskDetail = {
  recent: FleetEvent[]
  commits: string[]
  dirty_files: string[]
  final_message: string
}

export type FleetTask = {
  task: string
  status: string
  alive: boolean
  pid?: number
  branch?: string
  session_id?: string
  runtime_sec: number
  idle_sec: number
  commits: number
  dirty: number
  untracked: number
  last_event?: string
  activity?: string
  error?: string
  has_result: boolean
  runs: number
  tokens?: Tokens
  stream?: Stream
  heavy?: Proc[]
  detail?: TaskDetail
}

export type FleetSnapshot = {
  config: string
  max_concurrent: number
  running: number
  queued: number
  paused?: number
  available_ram_gb: number
  time: string
  tasks: FleetTask[]
}

/** One fleet from `codexfleet fleets --json` / `snapshot`. */
export type FleetEntry = {
  path: string
  current: boolean
  live: boolean
  error?: string
  status?: FleetSnapshot
}

export type Machine = {
  available_ram_gb: number
  total_ram_gb: number
  top: Proc[]
  error?: string
}

/** `codexfleet snapshot` */
export type Snapshot = {
  time: string
  machine: Machine
  fleets: FleetEntry[]
}

declare module 'claude-code' {
  interface PluginState {
    codexfleet: { fleets: FleetEntry[]; machine: Machine | null; error: string | null; selected: string | null }
  }
}
