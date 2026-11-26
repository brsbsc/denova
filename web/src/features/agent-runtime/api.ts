import { jsonHeaders, requestJSON } from '@/lib/api-client'
import type {
  AgentEngineID,
  AuthSessionSnapshot,
  ClaudeAuthMethod,
  EngineDescriptor,
  EngineModels,
  ManagedCLIStatus,
  ManagedCLIStatusResponse,
} from './types'

export const fetchAgentEngines = () => requestJSON<{ items: EngineDescriptor[] }>('/api/agent-runtimes')
export const fetchEngineModels = (id: AgentEngineID) => requestJSON<EngineModels>(`/api/agent-runtimes/${id}/models`)

/** Opens a CLI process, so only an explicit user action may call this. */
export const checkAgentEngine = (id: AgentEngineID) => requestJSON<EngineDescriptor>(`/api/agent-runtimes/${id}/check`, {
  method: 'POST', headers: jsonHeaders, body: '{}',
})

export const checkEngineCLIUpdate = (id: AgentEngineID) => requestJSON<ManagedCLIStatus>(`/api/agent-runtimes/${id}/cli/check-update`, {
  method: 'POST', headers: jsonHeaders, body: '{}',
})

export const installEngineCLI = (id: AgentEngineID) => requestJSON<ManagedCLIStatus>(`/api/agent-runtimes/${id}/cli/install`, {
  method: 'POST', headers: jsonHeaders, body: '{}',
})

export const fetchEngineCLIStatus = (id: AgentEngineID) => requestJSON<ManagedCLIStatusResponse>(`/api/agent-runtimes/${id}/cli/status`)

export const startEngineAuth = (id: AgentEngineID, options?: { method?: ClaudeAuthMethod }) => requestJSON<AuthSessionSnapshot>(`/api/agent-runtimes/${id}/auth/start`, {
  method: 'POST',
  headers: jsonHeaders,
  body: JSON.stringify(options?.method ? { method: options.method } : {}),
})

export const fetchEngineAuthSession = (id: AgentEngineID, sessionID: string) => requestJSON<AuthSessionSnapshot>(
  `/api/agent-runtimes/${id}/auth/${encodeURIComponent(sessionID)}`,
)

export const submitEngineAuth = (id: AgentEngineID, sessionID: string, input: string) => requestJSON<AuthSessionSnapshot>(
  `/api/agent-runtimes/${id}/auth/${encodeURIComponent(sessionID)}/submit`,
  { method: 'POST', headers: jsonHeaders, body: JSON.stringify({ input }) },
)

export const cancelEngineAuth = (id: AgentEngineID, sessionID: string) => requestJSON<AuthSessionSnapshot>(
  `/api/agent-runtimes/${id}/auth/${encodeURIComponent(sessionID)}/cancel`,
  { method: 'POST', headers: jsonHeaders, body: '{}' },
)

export const logoutEngine = (id: AgentEngineID) => requestJSON<EngineDescriptor>(`/api/agent-runtimes/${id}/auth/logout`, {
  method: 'POST', headers: jsonHeaders, body: '{}',
})
