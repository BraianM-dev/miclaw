import { useEffect, useState, useCallback } from 'react'
import { useNavigate } from 'react-router-dom'
import { Layout } from '../components/Layout'
import { api } from '../api/client'
import { useEvents } from '../hooks/useEvents'
import type { DashboardStats, Agent, Alert, SSEEvent } from '../types'
import {
  Monitor, CheckCircle, WifiOff, Ticket,
  AlertTriangle, Clock, RefreshCw, ChevronRight,
} from 'lucide-react'

function ago(ts: string) {
  const s = Math.round((Date.now() - new Date(ts).getTime()) / 1000)
  if (s < 60)   return `${s}s`
  if (s < 3600) return `${Math.round(s / 60)}m`
  return `${Math.round(s / 3600)}h`
}

function AgentCard({ agent }: { agent: Agent }) {
  const nav = useNavigate()
  const isOk = agent.status === 'ok'
  const borderColor = isOk ? 'var(--success)' : agent.status === 'offline' ? 'var(--border)' : 'var(--warning)'

  return (
    <div
      className="card card-sm"
      style={{ cursor: 'pointer', borderLeft: `3px solid ${borderColor}` }}
      onClick={() => nav(`/agents/${encodeURIComponent(agent.id)}`)}
    >
      <div className="flex items-center gap-8 mb-6">
        <span className={`dot dot-${agent.status}`} />
        <span style={{ fontWeight: 600, fontSize: 13, flex: 1 }} className="truncate">{agent.name}</span>
        <ChevronRight size={13} color="var(--muted)" />
      </div>
      <div className="text-small text-muted truncate">{agent.location || agent.ip}</div>
      <div className="flex items-center gap-6 mt-6">
        <Clock size={11} color="var(--muted)" />
        <span className="text-xs text-muted">hace {ago(agent.last_seen)}</span>
        <span className={`badge badge-${agent.status}`} style={{ marginLeft: 'auto', fontSize: 10 }}>
          {agent.status === 'ok' ? 'online' : agent.status}
        </span>
      </div>
    </div>
  )
}

export function Dashboard() {
  const [stats,   setStats]   = useState<DashboardStats | null>(null)
  const [agents,  setAgents]  = useState<Agent[]>([])
  const [alerts,  setAlerts]  = useState<Alert[]>([])
  const [loading, setLoading] = useState(true)

  const load = useCallback(async () => {
    try {
      const [s, a, al] = await Promise.all([
        api.stats(),
        api.agents(),
        api.alerts(undefined, 5),
      ])
      setStats(s); setAgents(a); setAlerts(al)
    } finally { setLoading(false) }
  }, [])

  useEffect(() => { load() }, [load])

  useEvents(useCallback((ev: SSEEvent) => {
    if (['agent_update', 'heartbeat', 'alert', 'ticket_update'].includes(ev.type)) load()
  }, [load]))

  const online  = agents.filter(a => a.status === 'ok').length
  const offline = agents.filter(a => a.status === 'offline').length
  const critical = alerts.filter(a => a.level === 'critical' && a.status === 'open')

  if (loading) return (
    <Layout title="Dashboard">
      <div className="loading-center"><span className="spinner spinner-lg" /> Cargando...</div>
    </Layout>
  )

  return (
    <Layout title="Dashboard">
      {/* Alertas críticas activas */}
      {critical.length > 0 && (
        <div className="alert-banner danger mb-16">
          <AlertTriangle size={16} style={{ flexShrink: 0 }} />
          <div>
            <strong>{critical.length} alerta{critical.length > 1 ? 's' : ''} crítica{critical.length > 1 ? 's' : ''} activa{critical.length > 1 ? 's' : ''}</strong>
            {' — '}{critical.map(a => a.message).slice(0, 2).join(' · ')}
            {critical.length > 2 && ` · +${critical.length - 2} más`}
          </div>
        </div>
      )}

      {/* KPIs */}
      <div className="stats-grid mb-24">
        <div className="stat-card">
          <div className="stat-label" style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
            <CheckCircle size={12} /> Agentes online
          </div>
          <div className={`stat-value ${online > 0 ? 'success' : 'danger'}`}>{online}</div>
          <div className="stat-trend">de {agents.length} total</div>
        </div>
        <div className="stat-card">
          <div className="stat-label" style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
            <WifiOff size={12} /> Agentes offline
          </div>
          <div className={`stat-value ${offline > 0 ? 'danger' : ''}`}>{offline}</div>
          <div className="stat-trend">{offline === 0 ? 'Todo operativo' : 'Requieren atención'}</div>
        </div>
        <div className="stat-card">
          <div className="stat-label" style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
            <Ticket size={12} /> Tickets abiertos
          </div>
          <div className={`stat-value ${(stats?.open_tickets ?? 0) > 0 ? 'warning' : ''}`}>
            {stats?.open_tickets ?? 0}
          </div>
          <div className="stat-trend">en cola de soporte</div>
        </div>
        <div className="stat-card">
          <div className="stat-label" style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
            <AlertTriangle size={12} /> Alertas críticas
          </div>
          <div className={`stat-value ${critical.length > 0 ? 'danger' : ''}`}>{critical.length}</div>
          <div className="stat-trend">{stats?.open_alerts ?? 0} alertas abiertas total</div>
        </div>
        <div className="stat-card">
          <div className="stat-label" style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
            <Monitor size={12} /> Total agentes
          </div>
          <div className="stat-value blue">{agents.length}</div>
          <div className="stat-trend">registrados en la red</div>
        </div>
        <div className="stat-card" style={{ cursor: 'pointer' }} onClick={load}>
          <div className="stat-label" style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
            <RefreshCw size={12} /> Actualizar
          </div>
          <div className="stat-value" style={{ fontSize: 18, color: 'var(--muted)', marginTop: 4 }}>↻</div>
          <div className="stat-trend">click para refrescar</div>
        </div>
      </div>

      {/* Agentes */}
      <div className="flex items-center justify-between mb-12">
        <div className="card-title">Estado de agentes</div>
        {agents.length > 0 && (
          <div className="flex gap-8">
            <span className="badge badge-ok"><CheckCircle size={10} /> {online} online</span>
            {offline > 0 && <span className="badge badge-offline"><WifiOff size={10} /> {offline} offline</span>}
          </div>
        )}
      </div>

      {agents.length === 0 ? (
        <div className="card">
          <div className="empty-state">
            <div className="empty-state-icon"><Monitor size={40} color="var(--muted)" /></div>
            <div className="empty-state-title">Sin agentes registrados</div>
            <div className="empty-state-sub">
              Los agentes Frank se registran automáticamente al iniciar.<br />
              Verificá que el agente esté corriendo y apunte a este gateway.
            </div>
          </div>
        </div>
      ) : (
        <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(220px, 1fr))', gap: 12 }}>
          {agents.map(a => <AgentCard key={a.id} agent={a} />)}
        </div>
      )}

      {/* Últimas alertas */}
      {alerts.length > 0 && (
        <>
          <div className="card-title mt-24 mb-12">Últimas alertas</div>
          <div className="card" style={{ padding: 0 }}>
            <div className="table-wrap">
              <table>
                <thead>
                  <tr>
                    <th>Nivel</th>
                    <th>Agente</th>
                    <th>Mensaje</th>
                    <th>Estado</th>
                    <th>Hora</th>
                  </tr>
                </thead>
                <tbody>
                  {alerts.map(a => (
                    <tr key={a.id}>
                      <td><span className={`badge badge-${a.level}`}>{a.level}</span></td>
                      <td className="font-mono" style={{ fontSize: 12 }}>{a.agent_id || '—'}</td>
                      <td style={{ maxWidth: 300 }} className="truncate">{a.message}</td>
                      <td><span className={`badge badge-${a.status}`}>{a.status}</span></td>
                      <td className="text-muted text-small">{new Date(a.ts).toLocaleTimeString('es-AR')}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </div>
        </>
      )}
    </Layout>
  )
}
