export type Role = 'host' | 'client'

export type Capability =
  | 'cloudflare'
  | 'webrtc'
  | 'screencapture'
  | 'native_control'

export type Harness = 'native' | 'maestri' | 'codex'
export type Status = 'active' | 'paused' | 'offline' | 'releasing'

export interface DeviceInfo {
  device_id: string
  name: string
  role: Role
  public_key: string
  fingerprint: string
  capabilities: Capability[]
  paired_at: string
}

export interface SessionDescriptor {
  id: string
  harness: Harness
  nativeSessionId: string
  maestriTerminalId?: string
  codexThreadId?: string
  cwd: string
  pid?: number
  windowId?: string
  frontmost: boolean
  status: Status
  capabilities: Capability[]
  title?: string
  lastSeenAt?: string
  maestri_agent_name?: string
  maestri_socket?: string
  maestri_cli?: string
  // legado Marco 1
  session_id: string
  host_id: string
  devices: DeviceInfo[]
  created_at: string
  expires_at: string
}

export interface SessionMessageResult {
  status: string
  mode: 'maestri_ask' | 'local_inject' | 'codex_turn' | string
  reply?: string
  turn_id?: string
  error?: string
}

export interface Attachment {
  id: string
  name: string
  mime: string
  size: number
  url: string
  path?: string
  caption?: string
}

export interface AgentStatus {
  listening: boolean
  address: string
  version: string
  paired?: boolean
}

export interface AuthenticatedStatus extends AgentStatus {
  session_id: string
  session_path: string
  devices: DeviceInfo[]
  sessions: SessionDescriptor[]
}

export interface ApprovalRequest {
  id: string
  command: string
  cwd: string
  justification: string
  permissions: string[]
  approved?: boolean
}

export interface CodexApproval {
  id: string
  thread_id: string
  turn_id: string
  item_id: string
  approval_id?: string
  command?: string
  cwd?: string
  reason?: string
  started_at_ms: number
  created_at: string
}

export interface CodexEvent {
  id: string
  thread_id: string
  turn_id?: string
  kind: 'status' | 'timeline' | 'error' | 'approval'
  method?: string
  status?: string
  text?: string
  created_at: string
}

export type ApprovalAction = 'accept' | 'deny'

export interface SignedEnvelope {
  payload: string
  signature: string
  signer_key: string
}

export interface PairResponse {
  session_id: string
  device_id: string
  host_name: string
  server_key: string
  server_ecdh: string
  lease_token: string
  lease_expiry: string
}
