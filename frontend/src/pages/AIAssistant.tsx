import { useState, useRef, useEffect, useCallback } from 'react'
import { Layout } from '../components/Layout'
import { api } from '../api/client'
import type { Agent, Alert } from '../types'
import { Bot, Send, Trash2, Cpu, AlertTriangle, Copy, Check, Pencil, RotateCcw } from 'lucide-react'

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

function CopyButton({ text }: { text: string }) {
  const [copied, setCopied] = useState(false)
  const copy = () => {
    navigator.clipboard.writeText(text).then(() => {
      setCopied(true)
      setTimeout(() => setCopied(false), 1500)
    })
  }
  return (
    <button
      className="chat-action-btn"
      onClick={copy}
      title="Copiar"
      style={{ opacity: copied ? 1 : undefined }}
    >
      {copied ? <Check size={11} color="var(--success)" /> : <Copy size={11} />}
    </button>
  )
}

export function AIAssistant() {
  const [messages, setMessages] = useState<Message[]>([{
    role: 'assistant',
    content: 'Hola! Soy el asistente IA de MicLaw. Puedo analizar agentes, alertas, tickets y la red AFE. ¿En qué te ayudo?',
    ts: new Date(),
  }])
  const [input,     setInput]     = useState('')
  const [loading,   setLoading]   = useState(false)
  const [agents,    setAgents]    = useState<Agent[]>([])
  const [alerts,    setAlerts]    = useState<Alert[]>([])
  const [editingIdx, setEditingIdx] = useState<number | null>(null)
  const [editText,   setEditText]   = useState('')
  const chatRef  = useRef<HTMLDivElement>(null)
  const inputRef = useRef<HTMLInputElement>(null)

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
    let ctx = `Sistema: MicLaw IT Operations — AFE Uruguay\n`
    ctx += `Agentes: ${online.length} online / ${agents.length} total\n`
    if (offline.length > 0)
      ctx += `Offline: ${offline.map(a => `${a.name}(${a.ip})`).join(', ')}\n`
    if (crits.length > 0) {
      ctx += `Alertas abiertas: ${crits.length}\n`
      crits.slice(0, 5).forEach(a => { ctx += `  [${a.level}] ${a.agent_id}: ${a.message}\n` })
    }
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

  // Edit a past user message and resend from that point
  const startEdit = (idx: number) => {
    setEditingIdx(idx)
    setEditText(messages[idx].content)
  }

  const confirmEdit = async () => {
    if (editingIdx === null || !editText.trim()) return
    const prompt = editText.trim()
    // Truncate history up to (not including) the edited message, then resend
    const history = messages.slice(0, editingIdx)
    setMessages([...history, { role: 'user', content: prompt, ts: new Date() }])
    setEditingIdx(null)
    setEditText('')
    setLoading(true)
    try {
      const res = await api.aiQuery(prompt, buildContext())
      setMessages(prev => [...prev, {
        role: 'assistant', content: res.response, source: res.source, ts: new Date(),
      }])
    } catch (e) {
      setMessages(prev => [...prev, {
        role: 'assistant',
        content: `Error: ${(e as Error).message}`,
        ts: new Date(),
      }])
    } finally { setLoading(false) }
  }

  const retryLast = () => {
    // Find last user message and resend
    for (let i = messages.length - 1; i >= 0; i--) {
      if (messages[i].role === 'user') {
        send(messages[i].content)
        return
      }
    }
  }

  const clear = () => {
    setMessages([{ role: 'assistant', content: 'Conversación reiniciada. ¿En qué te ayudo?', ts: new Date() }])
    setEditingIdx(null)
  }

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
            <div className="flex gap-8">
              <button className="btn btn-ghost btn-sm" onClick={retryLast} title="Reintentar última consulta" disabled={loading}>
                <RotateCcw size={13} />
              </button>
              <button className="btn btn-ghost btn-sm" onClick={clear}>
                <Trash2 size={13} /> Limpiar
              </button>
            </div>
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
              {editingIdx === i ? (
                /* Inline edit mode */
                <div className="chat-edit-wrap">
                  <textarea
                    className="chat-edit-input"
                    value={editText}
                    onChange={e => setEditText(e.target.value)}
                    onKeyDown={e => {
                      if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); confirmEdit() }
                      if (e.key === 'Escape') setEditingIdx(null)
                    }}
                    autoFocus
                    rows={3}
                  />
                  <div className="flex gap-6 mt-6" style={{ justifyContent: 'flex-end' }}>
                    <button className="btn btn-ghost btn-xs" onClick={() => setEditingIdx(null)}>Cancelar</button>
                    <button className="btn btn-primary btn-xs" onClick={confirmEdit} disabled={!editText.trim()}>
                      <Send size={11} /> Enviar
                    </button>
                  </div>
                </div>
              ) : (
                <>
                  <div className="chat-bubble">{m.content}</div>
                  <div className="chat-meta">
                    {m.role === 'assistant' && m.source && (
                      <span style={{ marginRight: 6, opacity: 0.7 }}>via {m.source}</span>
                    )}
                    {m.ts.toLocaleTimeString('es-AR', { hour: '2-digit', minute: '2-digit' })}
                    <span className="chat-actions">
                      <CopyButton text={m.content} />
                      {m.role === 'user' && (
                        <button
                          className="chat-action-btn"
                          onClick={() => startEdit(i)}
                          title="Editar y reenviar"
                        >
                          <Pencil size={11} />
                        </button>
                      )}
                    </span>
                  </div>
                </>
              )}
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
            ref={inputRef}
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
