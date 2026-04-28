import { useEffect, useState, useRef, useCallback } from 'react'
import { Layout } from '../components/Layout'
import { api } from '../api/client'
import { useEvents } from '../hooks/useEvents'
import type { Agent, Command, SSEEvent } from '../types'
import { Terminal, Play, Trash2, CheckCircle, XCircle, Clock } from 'lucide-react'

const PRESET_CMDS = [
  'info_sistema', 'flush_dns', 'diagnostico', 'mantenimiento', 'espacio_disco',
  'listar_procesos', 'reiniciar_spooler', 'estado_red', 'ver_logs_frank',
  'velocidad_red', 'generar_inventario',
]

interface LogLine {
  ts: string
  type: 'info' | 'success' | 'error' | 'system'
  text: string
}

const LOG_COLORS: Record<LogLine['type'], string> = {
  info:    'var(--text2)',
  success: 'var(--success)',
  error:   'var(--danger)',
  system:  'var(--muted)',
}

const STATUS_BADGE: Record<string, string> = {
  pending: 'warning', sent: 'info', done: 'success', failed: 'danger', timeout: 'offline'
}

export function Console() {
  const [agents,  setAgents]  = useState<Agent[]>([])
  const [agentId, setAgentId] = useState('')
  const [cmd,     setCmd]     = useState(PRESET_CMDS[0])
  const [custom,  setCustom]  = useState('')
  const [sending, setSending] = useState(false)
  const [log,     setLog]     = useState<LogLine[]>([])
  const [history, setHistory] = useState<Command[]>([])
  const logRef = useRef<HTMLDivElement>(null)

  const now = () => new Date().toLocaleTimeString('es-UY')

  const addLog = (type: LogLine['type'], text: string) =>
    setLog(prev => [...prev, { ts: now(), type, text }])

  useEffect(() => {
    api.agents().then(list => {
      setAgents(list)
      if (list.length > 0) setAgentId(list[0].id)
    })
    setLog([{ ts: now(), type: 'system', text: '── MicLaw Remote Console ready ──' }])
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  useEffect(() => {
    if (agentId) api.commands(agentId, 20).then(setHistory).catch(() => {})
  }, [agentId])

  useEvents(useCallback((ev: SSEEvent) => {
    if (ev.type === 'command_result') {
      const p = ev.payload as { agent_id: string; result: string; status: string; command: string }
      if (p.agent_id === agentId) {
        addLog(p.status === 'done' ? 'success' : 'error',
          `[${p.status.toUpperCase()}] ${p.command}\n${p.result}`)
        api.commands(agentId, 20).then(setHistory).catch(() => {})
      }
    }
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [agentId]))

  useEffect(() => {
    logRef.current?.scrollTo(0, logRef.current.scrollHeight)
  }, [log])

  const execute = async () => {
    const command = custom.trim() || cmd
    if (!agentId || !command) return
    setSending(true)
    addLog('info', `> ${command} → ${agentId}`)
    try {
      const res = await api.sendCommand(agentId, command)
      addLog('info', `Encolado: ${res.id} — esperando respuesta del agente...`)
    } catch (e) {
      addLog('error', `Error: ${(e as Error).message}`)
    } finally { setSending(false) }
  }

  const clearLog = () => setLog([{ ts: now(), type: 'system', text: '── Terminal limpia ──' }])

  return (
    <Layout title="Consola Remota">
      {/* Toolbar */}
      <div className="card mb-16">
        <div className="flex items-center gap-8 mb-12">
          <Terminal size={15} color="var(--blue)" />
          <div className="card-title">Ejecutar comando remoto</div>
        </div>
        <div className="flex gap-8" style={{ flexWrap: 'wrap' }}>
          <select
            className="select"
            style={{ width: 220 }}
            value={agentId}
            onChange={e => setAgentId(e.target.value)}
          >
            {agents.length === 0 && <option value="">Sin agentes</option>}
            {agents.map(a => (
              <option key={a.id} value={a.id}>
                {a.name}{a.status === 'offline' ? ' (offline)' : ''}
              </option>
            ))}
          </select>

          <select
            className="select"
            style={{ width: 200 }}
            value={cmd}
            onChange={e => setCmd(e.target.value)}
          >
            {PRESET_CMDS.map(c => <option key={c} value={c}>{c}</option>)}
          </select>

          <input
            className="input"
            style={{ flex: 1, minWidth: 160 }}
            value={custom}
            onChange={e => setCustom(e.target.value)}
            placeholder="Comando personalizado (sobreescribe selección)"
            onKeyDown={e => e.key === 'Enter' && execute()}
          />

          <button
            className="btn btn-primary"
            onClick={execute}
            disabled={sending || !agentId}
          >
            {sending
              ? <><span className="spinner spinner-sm" /> Enviando...</>
              : <><Play size={13} /> Ejecutar</>
            }
          </button>

          <button className="btn btn-ghost btn-sm" onClick={clearLog} title="Limpiar terminal">
            <Trash2 size={13} />
          </button>
        </div>
      </div>

      <div className="grid-2">
        {/* Terminal output */}
        <div>
          <div className="flex items-center gap-8 mb-8">
            <Terminal size={14} color="var(--muted)" />
            <div className="card-title">Terminal</div>
          </div>
          <div className="terminal" ref={logRef} style={{ userSelect: 'text', WebkitUserSelect: 'text' }}>
            {log.map((l, i) => (
              <div key={i} style={{ color: LOG_COLORS[l.type], marginBottom: 4, whiteSpace: 'pre-wrap' }}>
                <span style={{ color: 'var(--muted)', marginRight: 8, fontSize: 11, userSelect: 'none' }}>
                  [{l.ts}]
                </span>
                {l.text}
              </div>
            ))}
          </div>
        </div>

        {/* Command history */}
        <div>
          <div className="card-title mb-8">Historial del agente</div>
          <div className="card" style={{ padding: 0, overflow: 'hidden' }}>
            {history.length === 0 ? (
              <div className="empty-state text-small">Sin comandos previos</div>
            ) : (
              <table>
                <thead>
                  <tr><th>Comando</th><th>Estado</th><th>Hace</th></tr>
                </thead>
                <tbody>
                  {history.map(c => {
                    const age    = Math.round((Date.now() - new Date(c.created_at).getTime()) / 1000)
                    const ageStr = age < 60 ? `${age}s` : age < 3600 ? `${Math.round(age / 60)}m` : `${Math.round(age / 3600)}h`
                    return (
                      <tr
                        key={c.id}
                        style={{ cursor: 'pointer' }}
                        onClick={() => addLog('info', `[HIST] ${c.command}\n${c.result || '(sin resultado)'}`)}
                      >
                        <td style={{ fontSize: 12, fontFamily: 'monospace' }}>{c.command}</td>
                        <td>
                          <span className={`badge badge-${STATUS_BADGE[c.status] ?? 'info'}`}>
                            {c.status === 'done'   && <CheckCircle size={10} />}
                            {c.status === 'failed' && <XCircle size={10} />}
                            {c.status}
                          </span>
                        </td>
                        <td className="text-muted text-small">
                          <Clock size={10} style={{ marginRight: 3 }} />{ageStr}
                        </td>
                      </tr>
                    )
                  })}
                </tbody>
              </table>
            )}
          </div>
        </div>
      </div>
    </Layout>
  )
}
