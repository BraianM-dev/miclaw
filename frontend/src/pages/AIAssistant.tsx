import { useState, useRef, useEffect, useCallback } from 'react'
import { Layout } from '../components/Layout'
import { api } from '../api/client'
import type { Agent, Alert } from '../types'

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
  'Cuántos tickets abiertos hay y cuáles son urgentes?',
  'Resume el estado de la red AFE',
  'Sugiere acciones para los problemas detectados',
]

export function AIAssistant() {
  const [messages, setMessages]   = useState<Message[]>([
    {
      role: 'assistant',
      content: '¡Hola! Soy el asistente IA de MicLaw. Puedo ayudarte a analizar el estado de los agentes, alertas, tickets y la red AFE. ¿En qué te ayudo?',
      ts: new Date(),
    }
  ])
  const [input, setInput]         = useState('')
  const [loading, setLoading]     = useState(false)
  const [agents, setAgents]       = useState<Agent[]>([])
  const [alerts, setAlerts]       = useState<Alert[]>([])
  const chatRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    Promise.all([api.agents(), api.alerts('critical', 20)]).then(([a, al]) => {
      setAgents(a); setAlerts(al)
    }).catch(() => {})
  }, [])

  useEffect(() => {
    chatRef.current?.scrollTo(0, chatRef.current.scrollHeight)
  }, [messages])

  const buildContext = useCallback(() => {
    const onlineAgents  = agents.filter(a => a.status === 'ok')
    const offlineAgents = agents.filter(a => a.status === 'offline')
    const critAlerts    = alerts.filter(a => a.status === 'open')

    let ctx = `Sistema: MicLaw IT Operations Gateway\n`
    ctx += `Agentes en línea: ${onlineAgents.length}/${agents.length}\n`
    if (offlineAgents.length > 0) {
      ctx += `Agentes offline: ${offlineAgents.map(a => `${a.name} (${a.location || a.ip})`).join(', ')}\n`
    }
    if (critAlerts.length > 0) {
      ctx += `Alertas críticas abiertas (${critAlerts.length}):\n`
      critAlerts.slice(0, 5).forEach(a => {
        ctx += `  - [${a.level}] ${a.agent_id}: ${a.message}\n`
      })
    }
    ctx += `Red AFE MPLS activa. Responde en español, conciso y técnico.`
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
        role: 'assistant',
        content: res.response,
        source: res.source,
        ts: new Date(),
      }])
    } catch (e) {
      setMessages(prev => [...prev, {
        role: 'assistant',
        content: `❌ Error: ${(e as Error).message}`,
        ts: new Date(),
      }])
    } finally { setLoading(false) }
  }

  const clear = () => setMessages([{
    role: 'assistant',
    content: 'Conversación reiniciada. ¿En qué te ayudo?',
    ts: new Date(),
  }])

  return (
    <Layout title="IA Asistente">
      <div className="card flex-col" style={{ height: 'calc(100vh - 130px)' }}>
        {/* Header */}
        <div className="card-header" style={{ borderBottom: '1px solid var(--border)', paddingBottom: 12 }}>
          <div>
            <span className="card-title">🤖 Asistente IA — Ollama</span>
            <span className="text-small text-muted" style={{ marginLeft: 12 }}>
              {agents.filter(a => a.status === 'ok').length}/{agents.length} agentes en línea
              {alerts.filter(a => a.status === 'open').length > 0 && (
                <span className="text-danger" style={{ marginLeft: 8 }}>
                  ⚠ {alerts.filter(a => a.status === 'open').length} alertas
                </span>
              )}
            </span>
          </div>
          <button className="btn btn-ghost btn-sm" onClick={clear}>🗑 Limpiar</button>
        </div>

        {/* Sugerencias */}
        {messages.length <= 1 && (
          <div style={{ padding: '12px 16px', display: 'flex', flexWrap: 'wrap', gap: 8 }}>
            {SUGGESTIONS.map(s => (
              <button key={s} className="btn btn-ghost btn-sm" onClick={() => send(s)}
                style={{ fontSize: 12 }}>
                {s}
              </button>
            ))}
          </div>
        )}

        {/* Chat */}
        <div className="chat-area" ref={chatRef}>
          {messages.map((m, i) => (
            <div key={i} className={`chat-msg ${m.role}`}>
              <div className="chat-bubble">{m.content}</div>
              <div className="chat-meta">
                {m.role === 'assistant' && m.source && (
                  <span style={{ marginRight: 8 }}>via {m.source}</span>
                )}
                {m.ts.toLocaleTimeString('es-UY')}
              </div>
            </div>
          ))}
          {loading && (
            <div className="chat-msg assistant">
              <div className="chat-bubble text-muted">
                <span className="loading spin" style={{ marginRight: 8 }} />
                Pensando...
              </div>
            </div>
          )}
        </div>

        {/* Input */}
        <div className="chat-input-row">
          <input
            className="input chat-input"
            value={input}
            onChange={e => setInput(e.target.value)}
            onKeyDown={e => e.key === 'Enter' && !e.shiftKey && send()}
            placeholder="Pregunta sobre agentes, alertas, red AFE..."
            disabled={loading}
          />
          <button className="btn btn-primary" onClick={() => send()} disabled={loading || !input.trim()}>
            {loading ? '⏳' : '↑ Enviar'}
          </button>
        </div>
      </div>
    </Layout>
  )
}
