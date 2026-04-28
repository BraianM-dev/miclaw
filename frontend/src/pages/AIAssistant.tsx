import { useState, useRef, useEffect, useCallback } from 'react'
import { Layout } from '../components/Layout'
import { api } from '../api/client'
import type { Agent, Alert } from '../types'
import { Bot, Send, Trash2, Cpu, AlertTriangle } from 'lucide-react'

interface Message {
  role: 'user' | 'assistant'
  content: string
  source?: string
  ts: Date
}

const SUGGESTIONS = [
  'Analiza el estado de todos los agentes',
  'Hay algún agente con problemas de conectividad?',
  'Qué alertas críticas hay activas?',
  'Cuántos tickets abiertos hay?',
  'Resume el estado de la red AFE',
  'Sugiere acciones para los problemas detectados',
]

export function AIAssistant() {
  const [messages, setMessages] = useState<Message[]>([{
    role: 'assistant',
    content: 'Hola! Soy el asistente IA de MicLaw. Puedo analizar agentes, alertas, tickets y la red AFE. ¿En qué te ayudo?',
    ts: new Date(),
  }])
  const [input,   setInput]   = useState('')
  const [loading, setLoading] = useState(false)
  const [agents,  setAgents]  = useState<Agent[]>([])
  const [alerts,  setAlerts]  = useState<Alert[]>([])
  const chatRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    Promise.all([api.agents(), api.alerts(undefined, 20)])
      .then(([a, al]) => { setAgents(a); setAlerts(al) })
      .catch(() => {})
  }, [])

  useEffect(() => {
    chatRef.current?.scrollTo(0, chatRef.current.scrollHeight)
  }, [messages])

  const buildContext = useCallback(() => {
    const online  = agents.filter(a => a.status === 'ok')
    const offline = agents.filter(a => a.status === 'offline')
    const crits   = alerts.filter(a => a.status === 'open')
    let ctx = `Sistema: MicLaw IT Operations\n`
    ctx += `Agentes: ${online.length} online / ${agents.length} total\n`
    if (offline.length > 0)
      ctx += `Offline: ${offline.map(a => `${a.name}(${a.ip})`).join(', ')}\n`
    if (crits.length > 0) {
      ctx += `Alertas abiertas: ${crits.length}\n`
      crits.slice(0, 5).forEach(a => { ctx += `  [${a.level}] ${a.agent_id}: ${a.message}\n` })
    }
    ctx += `Responde en español, conciso y técnico. Máximo 3 párrafos.`
    return ctx
  }, [agents, alerts])

  const send = async (text?: string) => {
    const prompt = (text ?? input).trim()
    if (!prompt || loading) return
    setMessages(prev => [...prev, { role: 'user', content: prompt, ts: new Date() }])
    setInput('')
    setLoading(true)
    try {
      const res = await api.aiQuery(prompt, buildContext())
      setMessages(prev => [...prev, {
        role: 'assistant', content: res.response, source: res.source, ts: new Date(),
      }])
    } catch (e) {
      setMessages(prev => [...prev, {
        role: 'assistant',
        content: `Error: ${(e as Error).message}. Verificá que la API key sea correcta en Configuración.`,
        ts: new Date(),
      }])
    } finally { setLoading(false) }
  }

  const clear = () => setMessages([{
    role: 'assistant', content: 'Conversación reiniciada. ¿En qué te ayudo?', ts: new Date(),
  }])

  const onlineCount = agents.filter(a => a.status === 'ok').length
  const openAlerts  = alerts.filter(a => a.status === 'open').length

  return (
    <Layout title="IA Asistente">
      <div style={{ display: 'flex', flexDirection: 'column', height: 'calc(100vh - 106px)' }}>
        {/* Header */}
        <div className="card" style={{ borderRadius: 'var(--radius) var(--radius) 0 0', borderBottom: 'none', flexShrink: 0 }}>
          <div className="flex items-center justify-between">
            <div className="flex items-center gap-10">
              <div style={{
                width: 36, height: 36, borderRadius: 8,
                background: 'linear-gradient(135deg, var(--blue-dim), var(--blue))',
                display: 'flex', alignItems: 'center', justifyContent: 'center',
              }}>
                <Bot size={18} color="#fff" />
              </div>
              <div>
                <div className="card-title">Asistente IA — Ollama</div>
                <div className="flex items-center gap-10 mt-2">
                  <span className="text-xs text-muted">
                    <Cpu size={10} style={{ marginRight: 3 }} />
                    {onlineCount}/{agents.length} agentes online
                  </span>
                  {openAlerts > 0 && (
                    <span className="text-xs text-danger">
                      <AlertTriangle size={10} style={{ marginRight: 3 }} />
                      {openAlerts} alertas abiertas
                    </span>
                  )}
                </div>
              </div>
            </div>
            <button className="btn btn-ghost btn-sm" onClick={clear}>
              <Trash2 size={13} /> Limpiar
            </button>
          </div>
        </div>

        {/* Sugerencias */}
        {messages.length <= 1 && (
          <div className="chat-suggestions" style={{ background: 'var(--surface)', borderLeft: '1px solid var(--border)', borderRight: '1px solid var(--border)' }}>
            {SUGGESTIONS.map(s => (
              <button key={s} className="chip" onClick={() => send(s)}>{s}</button>
            ))}
          </div>
        )}

        {/* Chat */}
        <div className="chat-area" ref={chatRef} style={{
          flex: 1, overflowY: 'auto',
          background: 'var(--surface)',
          border: '1px solid var(--border)',
          borderTop: 'none', borderBottom: 'none',
        }}>
          {messages.map((m, i) => (
            <div key={i} className={`chat-msg ${m.role}`}>
              <div className="chat-bubble">{m.content}</div>
              <div className="chat-meta">
                {m.role === 'assistant' && m.source && <span style={{ marginRight: 6 }}>via {m.source}</span>}
                {m.ts.toLocaleTimeString('es-AR', { hour: '2-digit', minute: '2-digit' })}
              </div>
            </div>
          ))}
          {loading && (
            <div className="chat-msg assistant">
              <div className="chat-bubble text-muted flex items-center gap-8">
                <span className="spinner spinner-sm" /> Procesando...
              </div>
            </div>
          )}
        </div>

        {/* Input */}
        <div className="chat-input-bar" style={{
          background: 'var(--surface)',
          border: '1px solid var(--border)',
          borderTop: 'none',
          borderRadius: '0 0 var(--radius) var(--radius)',
        }}>
          <input
            className="input"
            style={{ flex: 1 }}
            value={input}
            onChange={e => setInput(e.target.value)}
            onKeyDown={e => e.key === 'Enter' && !e.shiftKey && send()}
            placeholder="Preguntá sobre agentes, alertas, red..."
            disabled={loading}
          />
          <button
            className="btn btn-primary"
            onClick={() => send()}
            disabled={loading || !input.trim()}
          >
            <Send size={14} />
          </button>
        </div>
      </div>
    </Layout>
  )
}
