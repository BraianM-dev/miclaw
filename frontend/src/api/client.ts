// API client — thin fetch wrapper con auth header y base URL configurable.

const BASE = (import.meta.env.VITE_API_URL as string | undefined) ?? '/api'
export const API_KEY = (import.meta.env.VITE_API_KEY as string | undefined) ?? 'changeme'

async function req<T>(method: string, path: string, body?: unknown): Promise<T> {
  const res = await fetch(`${BASE}${path}`, {
    method,
    headers: {
      'Content-Type': 'application/json',
      'X-API-Key': API_KEY,
    },
    body: body !== undefined ? JSON.stringify(body) : undefined,
  })
  if (!res.ok) {
    const err = await res.json().catch(() => ({ error: res.statusText }))
    throw new Error((err as { error: string }).error ?? res.statusText)
  }
  return res.json() as Promise<T>
}

const get   = <T>(path: string)               => req<T>('GET', path)
const post  = <T>(path: string, body: unknown) => req<T>('POST', path, body)
const patch = <T>(path: string, body: unknown) => req<T>('PATCH', path, body)

import type { Agent, Heartbeat, DashboardStats, Alert, Ticket, TicketMessage, Command } from '../types'

export const api = {
  // Dashboard
  stats: () => get<DashboardStats>('/dashboard/stats'),

  // Agents
  agents: () => get<Agent[]>('/agents'),
  agent:  (id: string) => get<{ agent: Agent; heartbeats: Heartbeat[] }>(`/agents/${encodeURIComponent(id)}`),

  // Commands
  sendCommand: (agentId: string, command: string, params: Record<string, string> = {}) =>
    post<{ id: string; status: string }>('/commands', {
      agent_id:  agentId,
      command,
      params:    JSON.stringify(params),
      requester: 'ui',
    }),
  commands: (agentId?: string, limit = 50) =>
    get<Command[]>(`/commands?${agentId ? `agent_id=${encodeURIComponent(agentId)}&` : ''}limit=${limit}`),

  // Alerts
  alerts: (level?: string, limit = 100) =>
    get<Alert[]>(`/alerts?limit=${limit}${level ? `&level=${level}` : ''}`),
  ackAlert: (id: number, status = 'ack') =>
    patch<{ status: string }>(`/alerts/${id}`, { status }),

  // Tickets
  tickets: (limit = 50) => get<Ticket[]>(`/tickets?limit=${limit}`),
  ticket:  (id: number) => get<{ ticket: Ticket; messages: TicketMessage[] }>(`/tickets/${id}`),
  createTicket: (data: Partial<Ticket> & { message: string }) =>
    post<{ id: number }>('/tickets', data),
  updateTicket: (id: number, status: string) =>
    patch<{ status: string }>(`/tickets/${id}`, { status }),
  addMessage: (ticketId: number, author: string, content: string) =>
    post<{ id: number }>(`/tickets/${ticketId}/messages`, { author, content }),

  // AI
  aiQuery: (prompt: string, context?: string) =>
    post<{ response: string; source: string; model?: string }>('/ai/query', { prompt, context }),
}

// ── Server-Sent Events ────────────────────────────────────────────────────
// EventSource no soporta custom headers — pasamos la API key como query param.
// El backend lo acepta via ?key= (ver authMiddleware).
export function connectEvents(onEvent: (event: unknown) => void): () => void {
  const url = `${BASE}/events?key=${encodeURIComponent(API_KEY)}`
  const es = new EventSource(url)

  es.onmessage = (e: MessageEvent) => {
    try { onEvent(JSON.parse(e.data as string)) } catch { /* ignore */ }
  }
  es.onerror = () => {
    // El navegador reconecta automáticamente.
  }

  return () => es.close()
}
