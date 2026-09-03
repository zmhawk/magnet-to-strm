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
  auth_available: boolean
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

export const api = {
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
  refreshAuthToken: (refreshToken: string) => request<{ message: string }>(
    '/api/v1/auth/refresh',
    {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ refresh_token: refreshToken }),
    },
  ),
  status: () => request<RuntimeStatus>('/api/v1/status'),
}
