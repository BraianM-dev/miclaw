import { useState, useRef, useEffect, useCallback } from 'react'
import { Layout } from '../components/Layout'
import { api } from '../api/client'
import type { Agent, Alert } from '../types'
import {
  Bot, Send, Trash2, Cpu, AlertTriangle, Copy, Check, Pencil,
  RotateCcw, ShieldAlert, Play, X, Info, Zap,
} from 'lucide-react'

// ── Types ────────────────────────────────────────────────────────────────────

interface ActionProposal {
  action:     string
  target:     string
  message:    string
  confidence: number
}

type ActionState = 'pending' | 'confirmed' | 'cancelled' | 'executing' | 'done' | 'failed'

interface Message {
  role:     'user' | 'assistant'
  content:  string
  source?:  string
  ts:       Date
  // set when this message is an action proposal
  proposal?:   ActionProposal
  actionState?: ActionState
  actionResult?: string
}

const SUGGESTIONS = [
  'Analiza el estado de todos los agentes',
  'Hay algún agente con problemas de conectividad?',
  'Qué alertas críticas hay activas?',
  'Reinicia el spooler de impresión en el servidor',
  'Hacé un diagnóstico en el agente offline',
  'Resume el estado de la red AFE',
]

// Friendly label map for action names
const ACTION_LABELS: Record<string, string> = {
  info_sistema:       'Obtener información del sistema',
  flush_dns:          'Limpiar caché DNS',
  diagnostico:        'Diagnóstico completo',
  mantenimiento:      'Ejecutar mantenimiento',
  espacio_disco:      'Verificar espacio en disco',
  listar_procesos:    'Listar procesos activos',
  reiniciar_spooler:  'Reiniciar servicio de impresión',
  estado_red:         'Verificar estado de red',
  ver_logs_frank:     'Ver logs del agente',
  velocidad_red:      'Medir velocidad de red',
  generar_inventario: 'Generar inventario del equipo',
}

// ── CopyButton ───────────────────────────────────────────────────────────────

function CopyButton({ text }: { text: string }) {
  const [copied, setCopied] = useState(false)
  const copy = () => {
    navigator.clipboard.writeText(text).then(() => {
      setCopied(true)
      setTimeout(() => setCopied(false), 1500)
    })
  }
  return (
    <button className="chat-action-btn" onClick={copy} title="Copiar">
      {copied
        ? <Check size={11} color="var(--success)" />
        : <Copy size={11} />}
    </button>
  )
}

// ── ActionConfirmCard ─────────────────────────────────────────────────────────

interface ActionConfirmCardProps {
  proposal:    ActionProposal
  state:       ActionState
  result?:     string
  onConfirm:   () => void
  onCancel:    () => void
}

function ConfidenceBar({ value }: { value: number }) {
  const pct   = Math.round(value * 100)
  const color = pct >= 80 ? 'var(--success)' : pct >= 50 ? 'var(--warning)' : 'var(--danger)'
  return (
    <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
      <div style={{
        flex: 1, height: 5, background: 'var(--surface3)',
        borderRadius: 3, overflow: 'hidden',
      }}>
        <div style={{ width: `${pct}%`, height: '100%', background: color, borderRadius: 3, transition: 'width 0.3s' }} />
      </div>
      <span style={{ fontSize: 11, color, fontWeight: 600, minWidth: 34 }}>{pct}%</span>
    </div>
  )
}

function ActionConfirmCard({ proposal, state, result, onConfirm, onCancel }: ActionConfirmCardProps) {
  const isPending   = state === 'pending'
  const isExecuting = state === 'executing'
  const isDone      = state === 'done'
  const isFailed    = state === 'failed'
  const isCancelled = state === 'cancelled'

  const borderColor = isFailed   ? 'var(--danger)'
                    : isDone     ? 'var(--success)'
                    : isCancelled? 'var(--border)'
                    :              'var(--warning)'

  return (
    <div style={{
      background: 'var(--surface2)',
      border: `1px solid ${borderColor}`,
      borderRadius: 'var(--radius)',
      padding: '14px 16px',
      width: '100%',
    }}>
      {/* Header */}
      <div className="flex items-center gap-8 mb-12">
        <div style={{
          width: 30, height: 30, borderRadius: 6, flexShrink: 0,
          background: isPending || isExecuting
            ? 'rgba(210, 153, 34, 0.15)'
            : isDone ? 'rgba(63, 185, 80, 0.15)' : 'rgba(248, 81, 73, 0.15)',
          display: 'flex', alignItems: 'center', justifyContent: 'center',
        }}>
          {isDone      ? <Check size={15} color="var(--success)" />
           : isFailed  ? <X size={15} color="var(--danger)" />
           : isCancelled? <X size={15} color="var(--muted)" />
           : isExecuting? <span className="spinner spinner-sm" />
           : <ShieldAlert size={15} color="var(--warning)" />}
        </div>
        <div>
          <div style={{ fontSize: 12, fontWeight: 700, color: 'var(--text)', letterSpacing: '-0.01em' }}>
            {isDone       ? 'Acción ejecutada'
             : isFailed   ? 'Acción fallida'
             : isCancelled ? 'Acción cancelada'
             : isExecuting ? 'Ejecutando...'
             : 'Propuesta de acción — requiere confirmación'}
          </div>
          <div style={{ fontSize: 11, color: 'var(--muted)', marginTop: 1 }}>
            El agente actuará solo con tu aprobación explícita
          </div>
        </div>
      </div>

      {/* Action detail rows */}
      <div style={{
        background: 'var(--surface3)', borderRadius: 'var(--radius-sm)',
        padding: '10px 12px', marginBottom: 12,
        display: 'flex', flexDirection: 'column', gap: 8,
      }}>
        <div className="flex items-center justify-between">
          <span style={{ fontSize: 11, color: 'var(--muted)', minWidth: 80 }}>Acción</span>
          <span style={{ fontSize: 12, fontWeight: 600, fontFamily: 'monospace', color: 'var(--blue)' }}>
            {ACTION_LABELS[proposal.action] ?? proposal.action}
          </span>
        </div>
        <div className="flex items-center justify-between">
          <span style={{ fontSize: 11, color: 'var(--muted)', minWidth: 80 }}>Comando</span>
          <code style={{ fontSize: 11, color: 'var(--text2)' }}>{proposal.action}</code>
        </div>
        <div className="flex items-center justify-between">
          <span style={{ fontSize: 11, color: 'var(--muted)', minWidth: 80 }}>Destino</span>
          <code style={{ fontSize: 11, color: 'var(--text2)' }}>{proposal.target}</code>
        </div>
        <div>
          <span style={{ fontSize: 11, color: 'var(--muted)', display: 'block', marginBottom: 4 }}>
            Confianza IA
          </span>
          <ConfidenceBar value={proposal.confidence} />
        </div>
      </div>

      {/* LLM explanation */}
      <div style={{
        fontSize: 13, color: 'var(--text2)', lineHeight: 1.55,
        padding: '8px 12px', background: 'var(--surface)',
        borderRadius: 'var(--radius-sm)', border: '1px solid var(--border)',
        marginBottom: 12, userSelect: 'text',
      }}>
        <Info size={11} style={{ marginRight: 6, flexShrink: 0, color: 'var(--muted)' }} />
        {proposal.message}
      </div>

      {/* Result (post-execution) */}
      {result && (
        <div style={{
          fontSize: 12, color: isDone ? 'var(--success)' : 'var(--danger)',
          padding: '8px 12px', background: 'var(--surface)',
          borderRadius: 'var(--radius-sm)', border: `1px solid ${borderColor}`,
          fontFamily: 'monospace', whiteSpace: 'pre-wrap', userSelect: 'text',
          marginBottom: 12,
        }}>
          {result}
        </div>
      )}

      {/* Action buttons — only while pending */}
      {isPending && (
        <div className="flex gap-8" style={{ justifyContent: 'flex-end' }}>
          <button className="btn btn-ghost btn-sm" onClick={onCancel}>
            <X size={13} /> Cancelar
          </button>
          <button className="btn btn-primary btn-sm" onClick={onConfirm}>
            <Play size={13} /> Confirmar y ejecutar
          </button>
        </div>
      )}

      {/* Warning footer */}
      {isPending && (
        <div style={{
          marginTop: 10, fontSize: 11, color: 'var(--muted)',
          display: 'flex', alignItems: 'center', gap: 5,
        }}>
          <Zap size={10} />
          Esta acción se ejecutará en el equipo remoto. No se puede deshacer automáticamente.
        </div>
      )}
    </div>
  )
}

// ── AIAssistant page ──────────────────────────────────────────────────────────

export function AIAssistant() {
  const [messages,   setMessages]   = useState<Message[]>([{
    role:    'assistant',
    content: 'Hola! Soy el asistente IA de MicLaw. Propongo acciones pero nunca las ejecuto sin tu confirmación. ¿En qué te ayudo?',
    ts:      new Date(),
  }])
  const [input,      setInput]      = useState('')
  const [loading,    setLoading]    = useState(false)
  const [agents,     setAgents]     = useState<Agent[]>([])
  const [alerts,     setAlerts]     = useState<Alert[]>([])
  const [editingIdx, setEditingIdx] = useState<number | null>(null)
  const [editText,   setEditText]   = useState('')
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

  // Send a user message and get the AI structured response
  const send = async (text?: string) => {
    const prompt = (text ?? input).trim()
    if (!prompt || loading) return
    const userMsg: Message = { role: 'user', content: prompt, ts: new Date() }
    setMessages(prev => [...prev, userMsg])
    setInput('')
    setLoading(true)
    try {
      const res = await api.aiQuery(prompt, buildContext())

      if (res.type === 'action_request' && res.action && res.target) {
        // AI proposes an action — add a special assistant message with proposal data
        setMessages(prev => [...prev, {
          role:       'assistant',
          content:    res.message ?? res.response,
          source:     res.source,
          ts:         new Date(),
          proposal: {
            action:     res.action!,
            target:     res.target!,
            message:    res.message ?? res.response,
            confidence: res.confidence ?? 0.5,
          },
          actionState: 'pending',
        }])
      } else {
        // Plain informational message
        setMessages(prev => [...prev, {
          role:    'assistant',
          content: res.content ?? res.response,
          source:  res.source,
          ts:      new Date(),
        }])
      }
    } catch (e) {
      setMessages(prev => [...prev, {
        role:    'assistant',
        content: `Error: ${(e as Error).message}. Verificá la API key en Configuración.`,
        ts:      new Date(),
      }])
    } finally { setLoading(false) }
  }

  // User confirmed an action proposal — execute via gateway command API
  const confirmAction = async (msgIdx: number) => {
    const msg = messages[msgIdx]
    if (!msg.proposal) return

    // Mark as executing
    setMessages(prev => prev.map((m, i) =>
      i === msgIdx ? { ...m, actionState: 'executing' } : m
    ))

    try {
      const res = await api.sendCommand(msg.proposal.target, msg.proposal.action)
      setMessages(prev => prev.map((m, i) =>
        i === msgIdx ? {
          ...m,
          actionState:  'done',
          actionResult: `Comando encolado: ${res.id}\nEstado: ${res.status}\nEsperando respuesta del agente...`,
        } : m
      ))
      // Add a follow-up system message
      setMessages(prev => [...prev, {
        role:    'assistant',
        content: `Comando "${msg.proposal!.action}" enviado al agente "${msg.proposal!.target}". El resultado llegará en unos instantes vía SSE.`,
        ts:      new Date(),
        source:  'system',
      }])
    } catch (e) {
      setMessages(prev => prev.map((m, i) =>
        i === msgIdx ? {
          ...m,
          actionState:  'failed',
          actionResult: `Error al ejecutar: ${(e as Error).message}`,
        } : m
      ))
    }
  }

  // User cancelled a proposal
  const cancelAction = (msgIdx: number) => {
    setMessages(prev => prev.map((m, i) =>
      i === msgIdx ? { ...m, actionState: 'cancelled' } : m
    ))
    setMessages(prev => [...prev, {
      role:    'assistant',
      content: 'Acción cancelada. El equipo no fue afectado.',
      ts:      new Date(),
    }])
  }

  // Edit a past user message and resend from that point
  const startEdit = (idx: number) => { setEditingIdx(idx); setEditText(messages[idx].content) }

  const confirmEdit = async () => {
    if (editingIdx === null || !editText.trim()) return
    const prompt  = editText.trim()
    const history = messages.slice(0, editingIdx)
    setMessages([...history, { role: 'user', content: prompt, ts: new Date() }])
    setEditingIdx(null); setEditText('')
    setLoading(true)
    try {
      const res = await api.aiQuery(prompt, buildContext())
      const isAction = res.type === 'action_request' && res.action && res.target
      setMessages(prev => [...prev, {
        role:        'assistant',
        content:     res.content ?? res.response,
        source:      res.source,
        ts:          new Date(),
        ...(isAction ? {
          proposal:    { action: res.action!, target: res.target!, message: res.message ?? res.response, confidence: res.confidence ?? 0.5 },
          actionState: 'pending' as ActionState,
        } : {}),
      }])
    } catch (e) {
      setMessages(prev => [...prev, { role: 'assistant', content: `Error: ${(e as Error).message}`, ts: new Date() }])
    } finally { setLoading(false) }
  }

  const retryLast = () => {
    for (let i = messages.length - 1; i >= 0; i--) {
      if (messages[i].role === 'user') { send(messages[i].content); return }
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
                <div className="card-title">Asistente IA — Modo seguro</div>
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
                  <span className="badge" style={{
                    background: 'rgba(63, 185, 80, 0.12)',
                    color: 'var(--success)', border: '1px solid rgba(63,185,80,0.3)',
                    fontSize: 10, padding: '1px 7px',
                  }}>
                    <ShieldAlert size={9} style={{ marginRight: 3 }} />
                    Confirmación requerida
                  </span>
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

        {/* Suggestions */}
        {messages.length <= 1 && (
          <div className="chat-suggestions" style={{ background: 'var(--surface)', borderLeft: '1px solid var(--border)', borderRight: '1px solid var(--border)' }}>
            {SUGGESTIONS.map(s => (
              <button key={s} className="chip" onClick={() => send(s)}>{s}</button>
            ))}
          </div>
        )}

        {/* Chat area */}
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
              ) : m.proposal ? (
                /* Action proposal card */
                <div style={{ width: '100%', maxWidth: 520 }}>
                  <ActionConfirmCard
                    proposal={m.proposal}
                    state={m.actionState ?? 'pending'}
                    result={m.actionResult}
                    onConfirm={() => confirmAction(i)}
                    onCancel={() => cancelAction(i)}
                  />
                  <div className="chat-meta" style={{ marginTop: 6 }}>
                    {m.ts.toLocaleTimeString('es-AR', { hour: '2-digit', minute: '2-digit' })}
                    <span className="chat-actions">
                      <CopyButton text={m.proposal.message} />
                    </span>
                  </div>
                </div>
              ) : (
                /* Plain text message */
                <>
                  <div className="chat-bubble">{m.content}</div>
                  <div className="chat-meta">
                    {m.role === 'assistant' && m.source && m.source !== 'system' && (
                      <span style={{ marginRight: 6, opacity: 0.6 }}>via {m.source}</span>
                    )}
                    {m.ts.toLocaleTimeString('es-AR', { hour: '2-digit', minute: '2-digit' })}
                    <span className="chat-actions">
                      <CopyButton text={m.content} />
                      {m.role === 'user' && (
                        <button className="chat-action-btn" onClick={() => startEdit(i)} title="Editar y reenviar">
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
                <span className="spinner spinner-sm" /> Analizando solicitud...
              </div>
            </div>
          )}
        </div>

        {/* Input bar */}
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
            placeholder="Preguntá sobre agentes, alertas, red... o pedí ejecutar un comando"
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
