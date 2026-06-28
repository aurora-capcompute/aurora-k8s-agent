// Mirrors the agent's JSON shapes (internal/webapi + the aurora.Runtime types).

export interface ManifestInfo {
  name: string;
  brain: string;
  system_prompt?: string;
  capabilities: string[];
  digest: string;
  manifest: unknown;
}

export interface ThreadSummary {
  id: string;
  title: string;
  created_at: string;
  updated_at: string;
  run_count: number;
  active_run_id?: string;
  manifest: unknown;
}

export type RunStatus =
  | "queued"
  | "running"
  | "stopping"
  | "yielded"
  | "waiting_task"
  | "interrupted"
  | "completed"
  | "stopped"
  | "failed";

export interface RunSnapshot {
  id: string;
  thread_id: string;
  message: string;
  status: RunStatus;
  answer?: string;
  error?: string;
}

export interface JournalOutcome {
  status: string;
  result?: unknown;
  message?: string;
}

export interface JournalCall {
  name: string;
  args?: unknown;
}

export interface JournalEntry {
  index: number;
  call: JournalCall;
  outcome: JournalOutcome;
}

export interface RevisionView {
  revision: number;
  forked: boolean;
  fork_parent?: number;
  fork_offset: number;
  entries: JournalEntry[];
}

export interface ThreadGraphRun {
  run_id: string;
  message: string;
  parent_run_id?: string;
  status: RunStatus;
  answer?: string;
  error?: string;
  attempt: number;
  current_revision: number;
  child_run_ids?: string[];
  revisions: RevisionView[];
}

export interface ThreadGraph {
  thread_id: string;
  title: string;
  runs: ThreadGraphRun[];
}

export interface RunGraphNode {
  run_id: string;
  thread_id: string;
  parent_id?: string;
  status: RunStatus;
  attempt: number;
  revision: number;
  answer?: string;
  error?: string;
  children?: RunGraphNode[];
}

export interface AgentEvent {
  type: string;
  data: unknown;
}

export interface ProgressEvent {
  run_id: string;
  message: string;
}

export type TaskState =
  | "pending"
  | "approved"
  | "denied"
  | "completed"
  | "failed"
  | "cancelled"
  | "expired"
  | "executed";

export type Decision =
  | "approved"
  | "denied"
  | "completed"
  | "failed"
  | "cancelled";

export interface Resolution {
  decision: Decision;
  data?: unknown;
  actor?: string;
  reason?: string;
}

export interface TaskSnapshot {
  id: string;
  run_id: string;
  revision: number;
  journal_position: number;
  call: JournalCall;
  summary: string;
  state: TaskState;
  resolution?: Resolution;
  created_at: string;
  expires_at?: string;
  resolved_at?: string;
  webhook_token: string;
}
