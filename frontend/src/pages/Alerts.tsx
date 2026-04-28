import { useEffect, useState, useCallback } from 'react'
import { Layout } from '../components/Layout'
import { api } from '../api/client'
import { useEvents } from '../hooks/useEvents'
import type { Alert, SSEEvent } from '../types'
import { Bell, AlertTriangle, Info, CheckCircle, RefreshCw, Filter } from 'lucide-react'

const LEVEL_ICON: Record<string, React.ReactNode> = {
  critical: <AlertTriangle size={12} />,
  warning:  <AlertTriangle size={12} />,
  info:     <Info size={12} />,
}

const FILTERS = [
  { val: '',         label: 'Todas' },
  { val: 'open',     label: 'Abiertas' },
  { val: 'critical', label: 'Críticas' },
  { val: 'warning',  label: 'Advertencias' },
  { val: 'ack',      label: 'Reconocidas' },
]

export function Alerts() {
  const [alerts,  setAlerts]  = useState<Alert[]>([])
  const [filter,  setFilter]  = useState('')
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
  const resolve = async (id: number) => {
    await api.ackAlert(id, 'resolved')
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
      {/* Stats bar */}
      <div className="flex items-center gap-12 mb-20" style={{ flexWrap: 'wrap' }}>
        <div className="flex gap-8">
          {critical > 0 && (
            <span className="badge badge-danger"><AlertTriangle size={10} /> {critical} crítica{critical > 1 ? 's' : ''}</span>
          )}
          <span className="badge badge-warning"><Bell size={10} /> {open} abierta{open !== 1 ? 's' : ''}</span>
          <span className="badge badge-offline">{alerts.length} total</span>
        </div>

        <div className="flex gap-4" style={{ marginLeft: 8 }}>
          <Filter size={13} color="var(--muted)" style={{ alignSelf: 'center' }} />
          {FILTERS.map(f => (
            <button
              key={f.val}
              className={`btn btn-sm ${filter === f.val ? 'btn-primary' : 'btn-ghost'}`}
              onClick={() => setFilter(f.val)}
            >
              {f.label}
            </button>
          ))}
        </div>

        <button className="btn btn-ghost btn-sm" style={{ marginLeft: 'auto' }} onClick={load}>
          <RefreshCw size={13} /> Actualizar
        </button>
      </div>

      <div className="card" style={{ padding: 0 }}>
        {loading ? (
          <div className="loading-center"><span className="spinner" /></div>
        ) : filtered.length === 0 ? (
          <div className="empty-state">
            <div className="empty-state-icon"><Bell size={40} color="var(--muted)" /></div>
            <div className="empty-state-title">Sin alertas{filter ? ` (${filter})` : ''}</div>
            <div className="empty-state-sub">El sistema está operativo.</div>
          </div>
        ) : (
          <div className="table-wrap">
            <table>
              <thead>
                <tr>
                  <th>Nivel</th>
                  <th>Agente</th>
                  <th>Fuente</th>
                  <th>Mensaje</th>
                  <th>Estado</th>
                  <th>Fecha</th>
                  <th></th>
                </tr>
              </thead>
              <tbody>
                {filtered.map(a => (
                  <tr key={a.id}>
                    <td>
                      <span className={`badge badge-${a.level === 'critical' ? 'critical' : a.level === 'warning' ? 'warning' : 'info'}`}>
                        {LEVEL_ICON[a.level]} {a.level}
                      </span>
                    </td>
                    <td>
                      <code style={{ fontSize: 12 }}>{a.agent_id || '—'}</code>
                    </td>
                    <td className="text-muted text-small">{a.source}</td>
                    <td style={{ maxWidth: 340 }}>
                      <div className="truncate">{a.message}</div>
                      {a.details && (
                        <div className="text-xs text-muted truncate">{a.details}</div>
                      )}
                    </td>
                    <td>
                      <span className={`badge badge-${
                        a.status === 'open' && a.level === 'critical' ? 'critical' :
                        a.status === 'open' ? 'warning' :
                        a.status === 'resolved' ? 'success' : 'offline'
                      }`}>
                        {a.status}
                      </span>
                    </td>
                    <td className="text-muted text-small" style={{ whiteSpace: 'nowrap' }}>
                      {new Date(a.ts).toLocaleString('es-AR', { dateStyle: 'short', timeStyle: 'short' })}
                    </td>
                    <td>
                      {a.status === 'open' && (
                        <div className="flex gap-6">
                          <button className="btn btn-ghost btn-xs" title="Reconocer" onClick={() => ack(a.id)}>
                            <CheckCircle size={12} /> Ack
                          </button>
                          <button className="btn btn-ghost btn-xs" title="Resolver" onClick={() => resolve(a.id)}>
                            ✓ Resolver
                          </button>
                        </div>
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
