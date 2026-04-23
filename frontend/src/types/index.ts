export interface Agent {
  id: string
  name: string
  type: string
  ip: string
  port: number
  hostname: string
  location: string
  gateway: string
  status: 'ok' | 'warning' | 'offline' | 'unknown'
  version: string
  last_seen: string
  enabled: boolean
}

export interface Heartbeat {
  id: number
  agent_id: string
  ip: string
  cpu_pct: number
  mem_pct: number
  disk_pct: number
  status: string
  ts: string
}

export interface Alert {
  id: number
  agent_id: string
  level: 'info' | 'warning' | 'critical'
  source: string
  message: string
  details: string
  status: 'open' | 'ack' | 'resolved'
  ts: string
}

export interface Command {
  id: string
  agent_id: string
  command: string
  params: string
  status: 'pending' | 'sent' | 'done' | 'failed' | 'timeout'
  result: string
  requester: string
  created_at: string
  executed_at?: string
}

export interface Ticket {
  id: number
  pc_name: string
  username: string
  message: string
  category: string
  priority: 'low' | 'normal' | 'high' | 'critical'
  agent_id: string
  telemetry: string
  status: 'open' | 'in_progress' | 'resolved' | 'closed'
  created_at: string
  updated_at: string
}

export interface TicketMessage {
  id: number
  ticket_id: number
  author: string
  content: string
  ts: string
}

export interface DashboardStats {
  total_agents: number
  online_agents: number
  offline_agents: number
  open_tickets: number
  open_alerts: number
  critical_alerts: number
}

export interface GatewaySettings {
  gateway_name: string
  organization: string
  alert_cpu_threshold: number
  alert_mem_threshold: number
  alert_disk_threshold: number
  auto_close_days: number
  webhook_url: string
  ollama_enabled: boolean
  ollama_model: string
}

export interface NetworkLocation {
  name: string
  cidr: string
  gateway: string
  region: string
}

export interface SSEEvent {
  type: 'agent_update' | 'heartbeat' | 'alert' | 'command_result' | 'ticket_update' | 'connected'
  payload: unknown
  ts: string
}

export interface LocalSettings {
  apiKey: string
  language: 'es' | 'en'
  accentColor: string
  refreshInterval: number
  notificationsEnabled: boolean
  sidebarCollapsed: boolean
}
