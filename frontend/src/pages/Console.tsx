import { useEffect, useState, useRef, useCallback } from 'react'
import { Layout } from '../components/Layout'
import { api } from '../api/client'
import { useEvents } from '../hooks/useEvents'
import type { Agent, Command, SSEEvent } from '../types'

const PRESET_CMDS = [
  'info_sistema', 'flush_dns', 'diagnostico', 'mantenimiento', 'espacio_disco',
  'listar_procesos', 'reiniciar_spooler', 'estado_red', 'ver_logs_frank',
  'velocidad_red', 'analizar_wazuh', 'generar_inventario',
]

interface LogLine {
  ts: string
  type: 'info' | 'success' | 'error' | 'system'
  text: string
}

export function Console() {
  const [agents, setAgents]     = useState<Agent[]>([])
  const [agentId, setAgentId]   = useState('')
  const [cmd, setCmd]           = useState(PRESET_CMDS[0])
  const [custom, setCustom]     = useState('')
  const [sending, setSending]   = useState(false)
  const [log, setLog]           = useState<LogLine[]>([])
  const [history, setHistory]   = useState<Command[]>([])
  const logRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    api.agents().then(list => {
      setAgents(list)
      if (list.length > 0) setAgentId(list[0].id)
    })
    setLog([{ ts: now(), type: 'system', text: '── MicLaw Remote Console ──' }])
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
  }, [agentId]))

  useEffect(() => {
    logRef.current?.scrollTo(0, logRef.current.scrollHeight)
  }, [log])

  const now = () => new Date().toLocaleTimeString('es-UY')
  const addLog = (type: LogLine['type'], text: string) =>
    setLog(prev => [...prev, { ts: now(), type, text }])

  const execute = async () => {
    const command = custom.trim() || cmd
    if (!agentId || !command) return
    setSending(true)
    addLog('info', `▶ ${command} → ${agentId}`)
    try {
      const res = await api.sendCommand(agentId, command)
      addLog('info', `Comando encolado: ${res.id} — esperando resultado...`)
    } catch (e) {
      addLog('error', `❌ ${(e as Error).message}`)
    } finally { setSending(false) }
  }

  const logColor: Record<LogLine['type'], string> = {
    info: 'var(--text2)', success: 'var(--success)', error: 'var(--danger)', system: 'var(--muted)'
  }

  return (
    <Layout title="Consola Remota">
      <div className="card" style={{ marginBottom: 16 }}>
        <div className="card-title mb-12">⌨️ Ejecutar comando remoto</div>
        <div className="flex gap-8 mb-8" style={{ flexWrap: 'wrap' }}>
          <select className="select" style={{ width: 220 }}
            value={agentId} onChange={e => setAgentId(e.target.value)}>
            {agents.length === 0 && <option value="">Sin agentes</option>}
            {agents.map(a => (
              <option key={a.id} value={a.id}>
                {a.name} {a.status === 'offline' ? '(offline)' : ''}
              </option>
            ))}
          </select>

          <select className="select" style={{ width: 200 }}
            value={cmd} onChange={e => setCmd(e.target.value)}>
            {PRESET_CMDS.map(c => <option key={c} value={c}>{c}</option>)}
          </select>

          <input className="input" style={{ flex: 1, minWidth: 160 }}
            value={custom} onChange={e => setCustom(e.target.value)}
            placeholder="Comando personalizado (sobreescribe selección)"
            onKeyDown={e => e.key === 'Enter' && execute()} />

          <button className="btn btn-primary" onClick={execute}
            disabled={sending || !agentId}>
            {sending ? '⏳ Enviando...' : '▶ Ejecutar'}
          </button>
          <button className="btn btn-ghost btn-sm" onClick={() =>
            setLog([{ ts: now(), type: 'system', text: '── Terminal limpia ──' }])}>
            Limpiar
          </button>
        </div>
      </div>

      <div className="grid-2">
        {/* Terminal */}
        <div>
          <div className="card-title mb-8">Terminal</div>
          <div className="terminal" ref={logRef}>
            {log.map((l, i) => (
              <div key={i} style={{ color: logColor[l.type], marginBottom: 4 }}>
                <span style={{ color: 'var(--muted)', marginRight: 8, fontSize: 11 }}>[{l.ts}]</span>
                {l.text}
              </div>
            ))}
          </div>
        </div>

        {/* Historial */}
        <div>
          <div className="card-title mb-8">Historial del agente</div>
          <div className="card" style={{ padding: 0, overflow: 'hidden' }}>
            {history.length === 0 ? (
              <div className="empty-state text-small">Sin comandos previos</div>
            ) : (
              <table>
                <thead><tr><th>Cmd</th><th>Estado</th><th>Hace</th></tr></thead>
                <tbody>
                  {history.map(c => {
                    const age = Math.round((Date.now() - new Date(c.created_at).getTime()) / 1000)
                    const ageStr = age < 60 ? `${age}s` : age < 3600 ? `${Math.round(age/60)}m` : `${Math.round(age/3600)}h`
                    return (
                      <tr key={c.id} style={{ cursor: 'pointer' }}
                        onClick={() => addLog('info', `[HIST] ${c.command}\n${c.result || '(sin resultado)'}`)}>
                        <td style={{ fontSize: 12, fontFamily: 'monospace' }}>{c.command}</td>
                        <td><span className={`badge badge-${c.status}`}>{c.status}</span></td>
                        <td className="text-muted text-small">{ageStr}</td>
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
