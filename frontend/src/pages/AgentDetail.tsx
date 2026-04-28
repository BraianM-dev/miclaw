import { useEffect, useState, useCallback } from 'react'
import { useParams, useNavigate } from 'react-router-dom'
import { Layout } from '../components/Layout'
import { api } from '../api/client'
import { useEvents } from '../hooks/useEvents'
import type { Agent, Heartbeat, Command, SSEEvent } from '../types'
import {
  ArrowLeft, Monitor, Activity, Terminal, ClipboardList,
  Play, CheckCircle, XCircle, Clock,
} from 'lucide-react'

function MetricBar({ label, value, pct }: { label: string; value: string; pct: number }) {
  const color = pct > 90 ? 'danger' : pct > 70 ? 'warning' : 'success'
  return (
    <div style={{ marginBottom: 12 }}>
      <div className="flex justify-between mb-4 text-small">
        <span className="text-muted">{label}</span>
        <span>{value}</span>
      </div>
      <div className="progress">
        <div className={`progress-bar ${color}`} style={{ width: `${Math.min(pct, 100)}%` }} />
      </div>
    </div>
  )
}

const COMMANDS = [
  'info_sistema', 'flush_dns', 'diagnostico', 'mantenimiento',
  'espacio_disco', 'listar_procesos', 'reiniciar_spooler', 'ver_logs_frank'
]

const STATUS_BADGE: Record<string, string> = {
  pending: 'warning', sent: 'info', done: 'success', failed: 'danger', timeout: 'offline'
}

export function AgentDetail() {
  const { id } = useParams<{ id: string }>()
  const nav = useNavigate()
  const [agent, setAgent]         = useState<Agent | null>(null)
  const [heartbeats, setHBs]      = useState<Heartbeat[]>([])
  const [commands, setCommands]   = useState<Command[]>([])
  const [cmd, setCmd]             = useState(COMMANDS[0])
  const [sending, setSending]     = useState(false)
  const [cmdResult, setCmdResult] = useState('')
  const [loading, setLoading]     = useState(true)

  const load = useCallback(async () => {
    if (!id) return
    try {
      const [detail, cmds] = await Promise.all([
        api.agent(id),
        api.commands(id, 10),
      ])
      setAgent(detail.agent)
      setHBs(detail.heartbeats ?? [])
      setCommands(cmds ?? [])
    } finally { setLoading(false) }
  }, [id])

  useEffect(() => { load() }, [load])

  useEvents(useCallback((ev: SSEEvent) => {
    if (ev.type === 'heartbeat') {
      const p = ev.payload as { agent_id: string }
      if (p.agent_id === id) load()
    }
    if (ev.type === 'command_result') {
      const p = ev.payload as { agent_id: string; result: string; status: string }
      if (p.agent_id === id) {
        setCmdResult(`[${p.status.toUpperCase()}] ${p.result}`)
        load()
      }
    }
  }, [id, load]))

  const sendCommand = async () => {
    if (!id || !cmd) return
    setSending(true)
    setCmdResult('Enviando comando...')
    try {
      const res = await api.sendCommand(id, cmd)
      setCmdResult(`Comando enviado (${res.id}). Esperando resultado...`)
    } catch (e) {
      setCmdResult(`Error: ${(e as Error).message}`)
    } finally { setSending(false) }
  }

  const latest = heartbeats[0]

  if (loading) return (
    <Layout title="Agente">
      <div className="loading-center"><span className="spinner spinner-lg" /></div>
    </Layout>
  )
  if (!agent) return (
    <Layout title="Agente">
      <div className="empty-state">
        <div className="empty-state-icon"><Monitor size={40} color="var(--muted)" /></div>
        <div className="empty-state-title">Agente no encontrado</div>
      </div>
    </Layout>
  )

  return (
    <Layout title={agent.name}>
      <button className="btn btn-ghost btn-sm mb-16" onClick={() => nav('/agents')}>
        <ArrowLeft size={14} /> Volver a agentes
      </button>

      <div className="grid-2" style={{ marginBottom: 20 }}>
        {/* Información */}
        <div className="card">
          <div className="card-header">
            <div className="flex items-center gap-8">
              <Monitor size={15} color="var(--blue)" />
              <span className="card-title">Información</span>
            </div>
            <span className={`badge badge-${agent.status}`}>{agent.status}</span>
          </div>
          {[
            ['ID',        agent.id],
            ['Hostname',  agent.hostname || '—'],
            ['IP',        agent.ip],
            ['Puerto',    String(agent.port)],
            ['Ubicación', agent.location || '—'],
            ['Gateway',   agent.gateway || '—'],
            ['Versión',   agent.version || '—'],
            ['Último HB', new Date(agent.last_seen).toLocaleString('es-UY')],
          ].map(([label, value]) => (
            <div key={label} className="flex justify-between" style={{ padding: '6px 0', borderBottom: '1px solid var(--border2)' }}>
              <span className="text-muted text-small">{label}</span>
              <span className="text-small" style={{ fontWeight: 500 }}>{value}</span>
            </div>
          ))}
        </div>

        {/* Métricas */}
        <div className="card">
          <div className="card-header">
            <div className="flex items-center gap-8">
              <Activity size={15} color="var(--blue)" />
              <span className="card-title">Métricas actuales</span>
            </div>
            {latest && (
              <span className="text-small text-muted flex items-center gap-4">
                <Clock size={11} /> {new Date(latest.ts).toLocaleTimeString('es-UY')}
              </span>
            )}
          </div>
          {latest ? (
            <>
              <MetricBar label="CPU"   value={`${latest.cpu_pct.toFixed(1)}%`}  pct={latest.cpu_pct}  />
              <MetricBar label="RAM"   value={`${latest.mem_pct.toFixed(1)}%`}  pct={latest.mem_pct}  />
              <MetricBar label="Disco" value={`${latest.disk_pct.toFixed(1)}%`} pct={latest.disk_pct} />
            </>
          ) : (
            <div className="empty-state text-small">Sin datos de heartbeat aún.</div>
          )}
        </div>
      </div>

      {/* Consola remota */}
      <div className="card mb-16">
        <div className="card-header">
          <div className="flex items-center gap-8">
            <Terminal size={15} color="var(--blue)" />
            <span className="card-title">Ejecutar comando</span>
          </div>
        </div>
        <div className="flex gap-8 mb-12">
          <select className="select" value={cmd} onChange={e => setCmd(e.target.value)} style={{ width: 220 }}>
            {COMMANDS.map(c => <option key={c} value={c}>{c}</option>)}
          </select>
          <button className="btn btn-primary" onClick={sendCommand} disabled={sending}>
            {sending
              ? <><span className="spinner spinner-sm" /> Enviando...</>
              : <><Play size={13} /> Ejecutar</>
            }
          </button>
        </div>
        {cmdResult && (
          <div className="code-block" style={{ userSelect: 'text', WebkitUserSelect: 'text' }}>
            {cmdResult}
          </div>
        )}
      </div>

      {/* Historial de comandos */}
      <div className="card">
        <div className="flex items-center gap-8 mb-12">
          <ClipboardList size={15} color="var(--blue)" />
          <div className="card-title">Últimos comandos</div>
        </div>
        {commands.length === 0 ? (
          <div className="text-muted text-small">Sin comandos ejecutados.</div>
        ) : (
          <div className="table-wrap">
            <table>
              <thead>
                <tr><th>Comando</th><th>Estado</th><th>Resultado</th><th>Fecha</th></tr>
              </thead>
              <tbody>
                {commands.map(c => (
                  <tr key={c.id}>
                    <td><code style={{ fontSize: 12 }}>{c.command}</code></td>
                    <td>
                      <span className={`badge badge-${STATUS_BADGE[c.status] ?? 'info'}`}>
                        {c.status === 'done'    && <CheckCircle size={10} />}
                        {c.status === 'failed'  && <XCircle size={10} />}
                        {c.status}
                      </span>
                    </td>
                    <td className="truncate" style={{ maxWidth: 300, userSelect: 'text' }}>{c.result || '—'}</td>
                    <td className="text-muted text-small">{new Date(c.created_at).toLocaleString('es-UY')}</td>
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
