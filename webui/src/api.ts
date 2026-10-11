export type JobState = 'queued' | 'running' | 'succeeded' | 'failed' | 'canceled'

export interface Job {
  gid: string
  info_hash: string
  name: string
  progress: number
  magnet_uri: string
  state: JobState
  error: string
  created_at: string
  started_at?: string
  finished_at?: string
}

export interface FileRecord {
  relative_path: string
  name: string
  sha1: string
  size_bytes: number
  remote_path: string
}

export interface JobDetail {
  job: Job
  result?: {
    name: string
    info_hash: string
    result_id: string
    total_bytes: number
    scanned_at: string
    files: FileRecord[] | null
  }
}

export interface RuntimeStatus {
  p115_enabled: boolean
  mode: 'full' | 'local'
  message: string
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const response = await fetch(path, {
    ...init,
    headers: { Accept: 'application/json', ...init?.headers },
  })
  if (!response.ok) {
    const body = await response.json().catch(() => null)
    throw new Error(body?.error || `请求失败 (${response.status})`)
  }
  if (response.status === 204) return undefined as T
  return response.json() as Promise<T>
}

export type Settings = Record<string, Record<string, string | number | boolean | string[] | null>>
export interface ConfigResponse { path: string; settings: Settings; setup_required: boolean; setup_mode?: boolean; credentials_configured: Record<string, boolean> }

export interface ServerPathNode {
  key: string
  title: string
  path: string
  is_directory: boolean
  isLeaf: boolean
  disabled?: boolean
}

export const api = {
  paths: (kind: 'database' | 'directory', path = '') => request<{ nodes: ServerPathNode[] }>(
    `/api/v1/filesystem?${new URLSearchParams({ kind, path })}`,
  ),
  config: () => request<ConfigResponse>('/api/v1/config'),
  saveConfig: (settings: Settings, credentials: Record<string, string> = {}) => request<{ path: string; restart_required: boolean; restarting: boolean; restart_url?: string }>('/api/v1/config', {
    method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ settings, credentials }),
  }),
  jobs: () => request<{ jobs: Job[] }>('/api/v1/jobs'),
  job: (gid: string) => request<JobDetail>(`/api/v1/jobs/${encodeURIComponent(gid)}`),
  cancel: (gid: string) => request<void>(
    `/api/v1/jobs/${encodeURIComponent(gid)}/cancel`,
    { method: 'POST' },
  ),
  delete: (gid: string) => request<void>(
    `/api/v1/jobs/${encodeURIComponent(gid)}`,
    { method: 'DELETE' },
  ),
  rebuildSTRMs: (gid: string) => request<{ rebuilt: number }>(
    `/api/v1/jobs/${encodeURIComponent(gid)}/rebuild-strm`,
    { method: 'POST' },
  ),
  status: () => request<RuntimeStatus>('/api/v1/status'),
}
