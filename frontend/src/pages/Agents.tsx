import { useEffect, useState, useCallback } from 'react'
import { useNavigate } from 'react-router-dom'
import { Layout } from '../components/Layout'
import { api } from '../api/client'
import { useEvents } from '../hooks/useEvents'
import type { Agent, SSEEvent } from '../types'
import { Monitor, Search, RefreshCw, ChevronRight, CheckCircle, WifiOff, AlertTriangle } from 'lucide-react'

const STATUS_LABEL: Record<string, string> = {
  ok: 'Online', offline: 'Offline', warning: 'Advertencia', unknown: 'Desconocido',
}

function MetricBar({ value, label }: { value?: number; label: string }) {
  if (value === undefined || value === null) return <span className="text-muted text-xs">—</span>
  const color = value > 85 ? 'danger' : value > 70 ? 'warning' : 'ok'
  return (
    <div style={{ minWidth: 80 }}>
      <div className="flex items-center justify-between mb-4">
        <span className="text-xs text-muted">{label}</span>
        <span className="text-xs" style={{ color: color === 'danger' ? 'var(--danger)' : color === 'warning' ? 'var(--warning)' : 'var(--muted)' }}>
          {value.toFixed(0)}%
        </span>
      </div>
      <div className="metric-fill-wrap">
        <div className={`metric-fill ${color}`} style={{ width: `${Math.min(value, 100)}%` }} />
      </div>
    </div>
  )
}

export function Agents() {
  const [agents,  setAgents]  = useState<Agent[]>([])
  const [filter,  setFilter]  = useState('')
  const [loading, setLoading] = useState(true)
  const nav = useNavigate()

  const load = useCallback(async () => {
    try { setAgents(await api.agents()) } finally { setLoading(false) }
  }, [])

  useEffect(() => { load() }, [load])
  useEvents(useCallback((ev: SSEEvent) => {
    if (ev.type === 'agent_update' || ev.type === 'heartbeat') load()
  }, [load]))

  const filtered = agents.filter(a =>
    !filter ||
    a.name.toLowerCase().includes(filter.toLowerCase()) ||
    a.ip.includes(filter) ||
    (a.location || '').toLowerCase().includes(filter.toLowerCase()) ||
    (a.hostname || '').toLowerCase().includes(filter.toLowerCase())
  )

  const online  = agents.filter(a => a.status === 'ok').length
  const offline = agents.filter(a => a.status === 'offline').length
  const warning = agents.filter(a => a.status === 'warning').length

  function ageSince(ts: string) {
    const s = Math.round((Date.now() - new Date(ts).getTime()) / 1000)
    if (s < 60)   return { str: `${s}s`, old: false }
    if (s < 3600) return { str: `${Math.round(s / 60)}m`, old: s > 120 }
    return { str: `${Math.round(s / 3600)}h`, old: true }
  }

  return (
    <Layout title="Agentes">
      {/* Toolbar */}
      <div className="flex items-center gap-12 mb-20" style={{ flexWrap: 'wrap' }}>
        <div className="flex gap-8">
          <span className="badge badge-ok"><CheckCircle size={10} /> {online} online</span>
          {offline > 0 && <span className="badge badge-offline"><WifiOff size={10} /> {offline} offline</span>}
          {warning > 0 && <span className="badge badge-warning"><AlertTriangle size={10} /> {warning} advertencia</span>}
        </div>
        <div className="flex items-center gap-8 flex-1" style={{ maxWidth: 340 }}>
          <Search size={14} color="var(--muted)" style={{ flexShrink: 0 }} />
          <input
            className="input"
            placeholder="Filtrar por nombre, IP, ubicación..."
            value={filter}
            onChange={e => setFilter(e.target.value)}
          />
        </div>
        <button className="btn btn-ghost btn-sm" onClick={load} style={{ marginLeft: 'auto' }}>
          <RefreshCw size={13} /> Actualizar
        </button>
      </div>

      <div className="card" style={{ padding: 0 }}>
        {loading ? (
          <div className="loading-center"><span className="spinner" /></div>
        ) : filtered.length === 0 ? (
          <div className="empty-state">
            <div className="empty-state-icon"><Monitor size={40} color="var(--muted)" /></div>
            <div className="empty-state-title">
              {filter ? 'Sin resultados' : 'No hay agentes registrados'}
            </div>
            <div className="empty-state-sub">
              {filter ? 'Probá con otro filtro.' : 'Los agentes Frank se registran automáticamente al iniciar.'}
            </div>
          </div>
        ) : (
          <div className="table-wrap">
            <table>
              <thead>
                <tr>
                  <th>Estado</th>
                  <th>Nombre / Hostname</th>
                  <th>IP</th>
                  <th>Ubicación</th>
                  <th>CPU</th>
                  <th>RAM</th>
                  <th>Disco</th>
                  <th>Última señal</th>
                  <th></th>
                </tr>
              </thead>
              <tbody>
                {filtered.map(a => {
                  const { str: ageStr, old } = ageSince(a.last_seen)
                  const hb = (a as unknown as Record<string, unknown>)
                  const cpu  = typeof hb.cpu_pct  === 'number' ? hb.cpu_pct  as number : undefined
                  const mem  = typeof hb.mem_pct  === 'number' ? hb.mem_pct  as number : undefined
                  const disk = typeof hb.disk_pct === 'number' ? hb.disk_pct as number : undefined
                  return (
                    <tr key={a.id} className="clickable-row" onClick={() => nav(`/agents/${encodeURIComponent(a.id)}`)}>
                      <td>
                        <div className="flex items-center gap-8">
                          <span className={`dot dot-${a.status}`} />
                          <span className={`badge badge-${a.status}`}>{STATUS_LABEL[a.status] ?? a.status}</span>
                        </div>
                      </td>
                      <td>
                        <div style={{ fontWeight: 500, color: 'var(--text)' }}>{a.name}</div>
                        {a.hostname && a.hostname !== a.name && (
                          <div className="text-xs text-muted">{a.hostname}</div>
                        )}
                      </td>
                      <td><code style={{ fontSize: 12 }}>{a.ip}</code></td>
                      <td className="text-muted">{a.location || '—'}</td>
                      <td style={{ width: 90 }}><MetricBar value={cpu}  label="CPU" /></td>
                      <td style={{ width: 90 }}><MetricBar value={mem}  label="RAM" /></td>
                      <td style={{ width: 90 }}><MetricBar value={disk} label="Disco" /></td>
                      <td className={old ? 'text-danger' : 'text-muted'} style={{ fontSize: 12 }}>{ageStr}</td>
                      <td><ChevronRight size={14} color="var(--muted)" /></td>
                    </tr>
                  )
                })}
              </tbody>
            </table>
          </div>
        )}
      </div>
    </Layout>
  )
}
