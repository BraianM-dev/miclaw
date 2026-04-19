import { useEffect, useState, useCallback } from 'react'
import { Layout } from '../components/Layout'
import { api } from '../api/client'
import { useEvents } from '../hooks/useEvents'
import type { Alert, SSEEvent } from '../types'

const LEVEL_COLOR: Record<string, string> = { info: 'info', warning: 'warning', critical: 'danger' }

export function Alerts() {
  const [alerts, setAlerts]   = useState<Alert[]>([])
  const [filter, setFilter]   = useState('')
  const [loading, setLoading] = useState(true)

  const load = useCallback(async () => {
    try { setAlerts(await api.alerts(undefined, 200)) } finally { setLoading(false) }
  }, [])

  useEffect(() => { load() }, [load])

  useEvents(useCallback((ev: SSEEvent) => {
    if (ev.type === 'alert') load()
  }, [load]))

  const ack = async (id: number) => {
    await api.ackAlert(id, 'ack')
    await load()
  }

  const filtered = alerts.filter(a =>
    !filter ||
    a.level === filter ||
    a.status === filter ||
    a.message.toLowerCase().includes(filter.toLowerCase()) ||
    a.agent_id.toLowerCase().includes(filter.toLowerCase())
  )

  const critical = alerts.filter(a => a.level === 'critical' && a.status === 'open').length
  const open     = alerts.filter(a => a.status === 'open').length

  return (
    <Layout title="Alertas">
      <div className="flex gap-8 mb-16 items-center">
        <span className="badge badge-danger">Críticas: {critical}</span>
        <span className="badge badge-warning">Abiertas: {open}</span>
        <div className="flex gap-4" style={{ marginLeft: 8 }}>
          {['', 'open', 'critical', 'warning', 'info', 'ack'].map(f => (
            <button key={f} className={`btn btn-sm ${filter === f ? 'btn-primary' : 'btn-ghost'}`}
              onClick={() => setFilter(f)}>
              {f === '' ? 'Todas' : f}
            </button>
          ))}
        </div>
        <button className="btn btn-ghost btn-sm" style={{ marginLeft: 'auto' }} onClick={load}>↻</button>
      </div>

      <div className="card">
        {loading ? (
          <div className="empty-state"><div className="loading spin" /></div>
        ) : filtered.length === 0 ? (
          <div className="empty-state">
            <div className="empty-icon">🔔</div>
            <div>Sin alertas {filter ? `(${filter})` : ''}</div>
          </div>
        ) : (
          <div className="table-wrap">
            <table>
              <thead>
                <tr><th>Nivel</th><th>Agente</th><th>Fuente</th><th>Mensaje</th><th>Estado</th><th>Fecha</th><th></th></tr>
              </thead>
              <tbody>
                {filtered.map(a => (
                  <tr key={a.id}>
                    <td><span className={`badge badge-${LEVEL_COLOR[a.level] ?? 'info'}`}>{a.level}</span></td>
                    <td style={{ fontSize: 12, fontFamily: 'monospace' }}>{a.agent_id || '—'}</td>
                    <td className="text-muted text-small">{a.source}</td>
                    <td style={{ maxWidth: 360, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                      {a.message}
                      {a.details && <span className="text-muted text-small" style={{ marginLeft: 8 }}>{a.details}</span>}
                    </td>
                    <td><span className={`badge badge-${a.status === 'open' ? (a.level === 'critical' ? 'danger' : 'warning') : 'offline'}`}>{a.status}</span></td>
                    <td className="text-muted text-small">{new Date(a.ts).toLocaleString('es-UY')}</td>
                    <td>
                      {a.status === 'open' && (
                        <button className="btn btn-ghost btn-sm" onClick={() => ack(a.id)}>✓ Ack</button>
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </div>
    </Layout>
  )
}
