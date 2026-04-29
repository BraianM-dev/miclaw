import type {
  Agent,
  AIQueryResponse,
  Alert,
  Command,
  DashboardStats,
  GatewaySettings,
  Heartbeat,
  NetworkLocation,
  SSEEvent,
  Ticket,
  TicketMessage,
} from '../types'

const BASE = (import.meta.env.VITE_API_URL as string | undefined) ?? '/api'
const DEFAULT_KEY = (import.meta.env.VITE_API_KEY as string | undefined) ?? 'changeme'

function getKey(): string {
  return localStorage.getItem('miclaw_api_key') || DEFAULT_KEY
}

async function req<T>(method: string, path: string, body?: unknown): Promise<T> {
  const res = await fetch(`${BASE}${path}`, {
    method,
    headers: {
      'Content-Type': 'application/json',
      'X-API-Key': getKey(),
    },
    body: body !== undefined ? JSON.stringify(body) : undefined,
  })
  if (!res.ok) {
    const err = await res.json().catch(() => ({ error: res.statusText }))
    throw new Error((err as { error: string }).error ?? `HTTP ${res.status}`)
  }
  return res.json() as Promise<T>
}

const get   = <T>(path: string)               => req<T>('GET', path)
const post  = <T>(path: string, body: unknown) => req<T>('POST', path, body)
const patch = <T>(path: string, body: unknown) => req<T>('PATCH', path, body)
const put   = <T>(path: string, body: unknown) => req<T>('PUT', path, body)
const del   = <T>(path: string)               => req<T>('DELETE', path)

export const api = {
  // Health
  health: () =>
    get<{ status: string; ollama: boolean; clients: number; version: string }>('/health'),

  // Dashboard
  stats: () => get<DashboardStats>('/dashboard/stats'),

  // Agents
  agents: () => get<Agent[]>('/agents'),
  agent: (id: string) =>
    get<{ agent: Agent; heartbeats: Heartbeat[] }>(`/agents/${encodeURIComponent(id)}`),
  deleteAgent: (id: string) => del<{ status: string }>(`/agents/${encodeURIComponent(id)}`),

  // Commands
  sendCommand: (
    agentId: string,
    command: string,
    params?: unknown,
    requester?: string,
  ) =>
    post<{ id: string; status: string }>('/commands', {
      agent_id:  agentId,
      command,
      params:    params ?? {},
      requester: requester ?? 'dashboard',
    }),
  commands: (agentId?: string, limit = 50) =>
    get<Command[]>(
      `/commands${agentId
        ? `?agent_id=${encodeURIComponent(agentId)}&limit=${limit}`
        : `?limit=${limit}`}`,
    ),
  command: (id: string) => get<Command>(`/commands/${id}`),

  // Alerts
  alerts: (level?: string, limit = 100) =>
    get<Alert[]>(`/alerts?limit=${limit}${level ? `&level=${level}` : ''}`),
  createAlert: (a: Partial<Alert>) => post<{ id: number }>('/alerts', a),
  ackAlert: (id: number, status: string) =>
    patch<{ status: string }>(`/alerts/${id}`, { status }),

  // Tickets
  tickets: (status?: string, limit = 50) =>
    get<Ticket[]>(`/tickets?limit=${limit}${status ? `&status=${status}` : ''}`),
  ticket: (id: number) =>
    get<{ ticket: Ticket; messages: TicketMessage[] }>(`/tickets/${id}`),
  createTicket: (t: Partial<Ticket>) =>
    post<{ id: number; category: string; priority: string; response: string }>('/tickets', t),
  updateTicket: (id: number, status: string) =>
    patch<{ status: string }>(`/tickets/${id}`, { status }),
  addMessage: (ticketId: number, author: string, content: string) =>
    post<{ id: number }>(`/tickets/${ticketId}/messages`, { author, content }),
  messages: (ticketId: number) =>
    get<TicketMessage[]>(`/tickets/${ticketId}/messages`),

  // AI — returns structured safe-action response
  aiQuery: (prompt: string, context?: string) =>
    post<AIQueryResponse>('/ai/query', { prompt, context }),

  // Network
  locations: () => get<NetworkLocation[]>('/network/locations'),

  // Settings
  getSettings: () => get<GatewaySettings>('/settings'),
  saveSettings: (s: GatewaySettings) => put<{ status: string }>('/settings', s),

  // Rules
  rules: () => get<unknown[]>('/rules'),

  // Knowledge
  knowledge: (category?: string) =>
    get<unknown[]>(`/knowledge${category ? `?category=${category}` : ''}`),

  // Queue
  queueStats: () =>
    get<{ pending: number; processing: number; done: number; failed: number }>('/queue/stats'),
}

// ── Server-Sent Events ────────────────────────────────────────────────────────
// EventSource does not support custom headers — we pass the API key as ?key=
export function connectEvents(
  onEvent: (event: SSEEvent) => void,
  onDisconnect?: () => void,
): () => void {
  const key = getKey()
  const url = `${BASE}/events?key=${encodeURIComponent(key)}`
  let es: EventSource | null = null
  let closed = false

  function connect() {
    if (closed) return
    es = new EventSource(url)

    es.onmessage = (e: MessageEvent) => {
      try {
        onEvent(JSON.parse(e.data as string) as SSEEvent)
      } catch { /* ignore parse errors */ }
    }

    es.onerror = () => {
      es?.close()
      onDisconnect?.()
      if (!closed) setTimeout(connect, 3000)
    }
  }

  connect()

  return () => {
    closed = true
    es?.close()
  }
}
