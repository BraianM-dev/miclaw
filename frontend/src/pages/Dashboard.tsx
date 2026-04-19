import { useEffect, useState, useCallback } from 'react'
import { useNavigate } from 'react-router-dom'
import { Layout } from '../components/Layout'
import { api } from '../api/client'
import { useEvents } from '../hooks/useEvents'
import type { DashboardStats, Agent, Alert, SSEEvent } from '../types'

function StatusDot({ status }: { status: string }) {
  return <span className={`dot dot-${status}`} />
}

function AgentCard({ agent }: { agent: Agent }) {
  const nav = useNavigate()
  const age = Math.round((Date.now() - new Date(agent.last_seen).getTime()) / 1000)
  const ageStr = age < 60 ? `${age}s` : age < 3600 ? `${Math.round(age/60)}m` : `${Math.round(age/3600)}h`

  return (
    <div className="card" style={{ cursor: 'pointer' }} onClick={() => nav(`/agents/${encodeURIComponent(agent.id)}`)}>
      <div className="flex items-center gap-8 mb-8">
        <StatusDot status={agent.status} />
        <span style={{ fontWeight: 600, fontSize: 13 }}>{agent.name}</span>
        <span className={`badge badge-${agent.status}`} style={{ marginLeft: 'auto' }}>{agent.status}</span>
      </div>
      <div className="text-small text-muted">{agent.location || agent.ip}</div>
      <div className="text-small text-muted" style={{ marginTop: 4 }}>visto hace {ageStr}</div>
    </div>
  )
}

export function Dashboard() {
  const [stats, setStats]   = useState<DashboardStats | null>(null)
  const [agents, setAgents] = useState<Agent[]>([])
  const [alerts, setAlerts] = useState<Alert[]>([])
  const [loading, setLoading] = useState(true)

  const load = useCallback(async () => {
    try {
      const [s, a, al] = await Promise.all([api.stats(), api.agents(), api.alerts('critical', 5)])
      setStats(s); setAgents(a); setAlerts(al)
    } finally { setLoading(false) }
  }, [])

  useEffect(() => { load() }, [load])

  useEvents(useCallback((ev: SSEEvent) => {
    if (ev.type === 'agent_update' || ev.type === 'heartbeat') load()
    if (ev.type === 'alert') load()
  }, [load]))

  if (loading) return (
    <Layout title="Dashboard">
      <div className="empty-state"><div className="loading spin" /><br />Cargando...</div>
    </Layout>
  )

  const online  = agents.filter(a => a.status === 'ok').length
  const offline = agents.filter(a => a.status === 'offline').length

  return (
    <Layout title="Dashboard">
      {/* Stats */}
      <div className="stats-grid">
        <div className="stat-card">
          <div className="stat-label">Agentes en línea</div>
          <div className={`stat-value ${online > 0 ? 'success' : 'danger'}`}>{online}</div>
        </div>
        <div className="stat-card">
          <div className="stat-label">Agentes offline</div>
          <div className={`stat-value ${offline > 0 ? 'danger' : ''}`}>{offline}</div>
        </div>
        <div className="stat-card">
          <div className="stat-label">Tickets abiertos</div>
          <div className={`stat-value ${(stats?.open_tickets ?? 0) > 0 ? 'warning' : ''}`}>
            {stats?.open_tickets ?? 0}
          </div>
        </div>
        <div className="stat-card">
          <div className="stat-label">Alertas críticas</div>
          <div className={`stat-value ${(stats?.critical_alerts ?? 0) > 0 ? 'danger' : ''}`}>
            {stats?.critical_alerts ?? 0}
          </div>
        </div>
        <div className="stat-card">
          <div className="stat-label">Total agentes</div>
          <div className="stat-value">{agents.length}</div>
        </div>
        <div className="stat-card">
          <div className="stat-label">Alertas abiertas</div>
          <div className={`stat-value ${(stats?.open_alerts ?? 0) > 0 ? 'warning' : ''}`}>
            {stats?.open_alerts ?? 0}
          </div>
        </div>
      </div>

      {/* Alertas críticas recientes */}
      {alerts.length > 0 && (
        <div className="mb-24">
          <div className="card-title mb-12">🚨 Alertas críticas recientes</div>
          {alerts.map(a => (
            <div key={a.id} className="alert-bar danger" style={{ marginBottom: 8 }}>
              <strong>{a.agent_id}</strong> — {a.message}
              <span className="text-muted text-small" style={{ marginLeft: 'auto' }}>
                {new Date(a.ts).toLocaleTimeString('es-UY')}
              </span>
            </div>
          ))}
        </div>
      )}

      {/* Grilla de agentes */}
      <div className="card-title mb-12">Estado de agentes</div>
      {agents.length === 0 ? (
        <div className="empty-state">
          <div className="empty-icon">🖥️</div>
          <div>No hay agentes registrados</div>
          <div className="text-small text-muted mt-8">Los agentes Frank se registran automáticamente al iniciar.</div>
        </div>
      ) : (
        <div className="grid-3" style={{ gridTemplateColumns: 'repeat(auto-fill, minmax(220px, 1fr))' }}>
          {agents.map(a => <AgentCard key={a.id} agent={a} />)}
        </div>
      )}
    </Layout>
  )
}
