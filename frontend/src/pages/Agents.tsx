import { useEffect, useState, useCallback } from 'react'
import { useNavigate } from 'react-router-dom'
import { Layout } from '../components/Layout'
import { api } from '../api/client'
import { useEvents } from '../hooks/useEvents'
import type { Agent, SSEEvent } from '../types'

const STATUS_LABEL: Record<string, string> = { ok: 'En línea', offline: 'Offline', warning: 'Advertencia', unknown: 'Desconocido' }

export function Agents() {
  const [agents, setAgents]   = useState<Agent[]>([])
  const [filter, setFilter]   = useState('')
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
    a.location.toLowerCase().includes(filter.toLowerCase()) ||
    a.hostname.toLowerCase().includes(filter.toLowerCase())
  )

  const online  = agents.filter(a => a.status === 'ok').length
  const offline = agents.filter(a => a.status === 'offline').length

  return (
    <Layout title="Agentes">
      <div className="flex items-center gap-12 mb-16">
        <div className="flex gap-8">
          <span className="badge badge-ok">En línea: {online}</span>
          <span className="badge badge-offline">Offline: {offline}</span>
        </div>
        <input
          className="input" style={{ maxWidth: 280 }}
          placeholder="Filtrar por nombre, IP, ubicación..."
          value={filter}
          onChange={e => setFilter(e.target.value)}
        />
        <button className="btn btn-ghost btn-sm" style={{ marginLeft: 'auto' }} onClick={load}>
          ↻ Actualizar
        </button>
      </div>

      <div className="card">
        {loading ? (
          <div className="empty-state"><div className="loading spin" /></div>
        ) : filtered.length === 0 ? (
          <div className="empty-state">
            <div className="empty-icon">🖥️</div>
            <div>{filter ? 'Sin resultados' : 'No hay agentes registrados'}</div>
          </div>
        ) : (
          <div className="table-wrap">
            <table>
              <thead>
                <tr>
                  <th>Estado</th>
                  <th>Nombre</th>
                  <th>Hostname</th>
                  <th>IP</th>
                  <th>Ubicación</th>
                  <th>Versión</th>
                  <th>Último heartbeat</th>
                </tr>
              </thead>
              <tbody>
                {filtered.map(a => {
                  const age = Math.round((Date.now() - new Date(a.last_seen).getTime()) / 1000)
                  const ageStr = age < 60 ? `${age}s` : age < 3600 ? `${Math.round(age/60)}m` : `${Math.round(age/3600)}h`
                  return (
                    <tr key={a.id} className="clickable-row" onClick={() => nav(`/agents/${encodeURIComponent(a.id)}`)}>
                      <td>
                        <span className={`badge badge-${a.status}`}>
                          {STATUS_LABEL[a.status] ?? a.status}
                        </span>
                      </td>
                      <td style={{ fontWeight: 500, color: 'var(--text)' }}>{a.name}</td>
                      <td>{a.hostname || '—'}</td>
                      <td><code style={{ fontSize: 12 }}>{a.ip}</code></td>
                      <td>{a.location || '—'}</td>
                      <td>{a.version || '—'}</td>
                      <td className={age > 120 ? 'text-danger' : 'text-muted'}>{ageStr}</td>
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
