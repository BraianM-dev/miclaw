import { useEffect, useState, useCallback } from 'react'
import { Layout } from '../components/Layout'
import { api } from '../api/client'
import { useEvents } from '../hooks/useEvents'
import type { Ticket, TicketMessage, SSEEvent } from '../types'
import { Plus, Send, Ticket as TicketIcon } from 'lucide-react'

const STATUS_OPTS = ['open', 'in_progress', 'resolved', 'closed']
const PRIORITY_COLOR: Record<string, string> = {
  critical: 'danger', high: 'warning', normal: 'info', low: 'offline'
}

function TicketDetail({ ticket, onBack, onUpdate, refreshTrigger }: {
  ticket: Ticket; onBack: () => void; onUpdate: () => void; refreshTrigger: number
}) {
  const [messages, setMessages] = useState<TicketMessage[]>([])
  const [author, setAuthor]     = useState('técnico')
  const [content, setContent]   = useState('')
  const [sending, setSending]   = useState(false)
  const [status, setStatus]     = useState(ticket.status)

  // Cargar mensajes al abrir y cuando llega un SSE ticket_update
  useEffect(() => {
    api.messages(ticket.id).then(setMessages).catch(() => {})
  }, [ticket.id, refreshTrigger])

  const changeStatus = async (s: string) => {
    setStatus(s as Ticket['status'])
    await api.updateTicket(ticket.id, s)
    onUpdate()
  }

  const sendMessage = async () => {
    if (!content.trim()) return
    setSending(true)
    try {
      await api.addMessage(ticket.id, author, content)
      setContent('')
      setMessages(await api.messages(ticket.id))
    } finally { setSending(false) }
  }

  return (
    <div className="card flex-col" style={{ height: '70vh' }}>
      <div className="card-header">
        <div>
          <button className="btn btn-ghost btn-sm" onClick={onBack} style={{ marginRight: 12 }}>← Volver</button>
          <span className="card-title">Ticket #{ticket.id} — {ticket.category}</span>
        </div>
        <div className="flex gap-8">
          <span className={`badge badge-${PRIORITY_COLOR[ticket.priority]}`}>{ticket.priority}</span>
          <select className="select" style={{ width: 140 }} value={status} onChange={e => changeStatus(e.target.value)}>
            {STATUS_OPTS.map(s => <option key={s} value={s}>{s}</option>)}
          </select>
        </div>
      </div>

      <div style={{ padding: '0 0 12px', borderBottom: '1px solid var(--border)' }}>
        <div className="text-small text-muted">
          {ticket.pc_name} / {ticket.username} — {new Date(ticket.created_at).toLocaleString('es-UY')}
        </div>
        <div style={{ marginTop: 8, padding: '10px 14px', background: 'var(--surface2)', borderRadius: 'var(--radius-sm)', fontSize: 13 }}>
          {ticket.message}
        </div>
      </div>

      <div style={{ flex: 1, overflowY: 'auto', padding: '12px 0', display: 'flex', flexDirection: 'column', gap: 10 }}>
        {messages.map(m => {
          const isSupport = m.author !== 'Usuario' && m.author !== ticket.username
          return (
            <div key={m.id} style={{ alignSelf: isSupport ? 'flex-start' : 'flex-end', maxWidth: '85%' }}>
              <div className="flex gap-8 mb-4" style={{ justifyContent: isSupport ? 'flex-start' : 'flex-end' }}>
                <span style={{ fontWeight: 600, fontSize: 12, color: isSupport ? 'var(--blue)' : 'var(--text)' }}>
                  {isSupport ? '🔧 ' : '👤 '}{m.author}
                </span>
                <span className="text-muted text-small">{new Date(m.ts).toLocaleString('es-UY')}</span>
              </div>
              <div style={{
                padding: '8px 12px',
                background: isSupport ? 'rgba(56,139,253,0.1)' : 'var(--surface2)',
                border: isSupport ? '1px solid rgba(56,139,253,0.25)' : '1px solid var(--border)',
                borderRadius: 'var(--radius-sm)', fontSize: 13,
                userSelect: 'text', whiteSpace: 'pre-wrap',
              }}>
                {m.content}
              </div>
            </div>
          )
        })}
        {messages.length === 0 && <div className="text-muted text-small">Sin mensajes aún.</div>}
      </div>

      <div style={{ borderTop: '1px solid var(--border)', paddingTop: 12, display: 'flex', gap: 8 }}>
        <input className="input" style={{ width: 120 }} value={author} onChange={e => setAuthor(e.target.value)} placeholder="Autor" />
        <input className="input flex-1" value={content} onChange={e => setContent(e.target.value)}
          onKeyDown={e => e.key === 'Enter' && !e.shiftKey && sendMessage()}
          placeholder="Escribe un mensaje..." />
        <button className="btn btn-primary" onClick={sendMessage} disabled={sending || !content.trim()}>
          {sending ? <span className="spinner spinner-sm" /> : <Send size={13} />}
          {sending ? 'Enviando...' : 'Enviar'}
        </button>
      </div>
    </div>
  )
}

export function Tickets() {
  const [tickets, setTickets]       = useState<Ticket[]>([])
  const [selected, setSelected]     = useState<Ticket | null>(null)
  const [filter, setFilter]         = useState('open')
  const [loading, setLoading]       = useState(true)
  const [creating, setCreating]     = useState(false)
  const [newMsg, setNewMsg]         = useState('')
  const [newPc, setNewPc]           = useState('')
  const [msgRefresh, setMsgRefresh] = useState(0)

  const load = useCallback(async () => {
    try { setTickets(await api.tickets(undefined, 100)) } finally { setLoading(false) }
  }, [])

  useEffect(() => { load() }, [load])

  useEvents(useCallback((ev: SSEEvent) => {
    if (ev.type === 'ticket_update') {
      load()
      setMsgRefresh(n => n + 1) // trigger message refresh in detail view
    }
  }, [load]))

  const createTicket = async () => {
    if (!newMsg.trim()) return
    setCreating(true)
    try {
      await api.createTicket({ message: newMsg, pc_name: newPc || 'manual', username: 'ui', category: 'general', priority: 'normal' })
      setNewMsg(''); setNewPc('')
      await load()
    } finally { setCreating(false) }
  }

  const filtered = tickets.filter(t => !filter || filter === 'all' || t.status === filter)

  if (selected) {
    return (
      <Layout title={`Ticket #${selected.id}`}>
        <TicketDetail ticket={selected} onBack={() => setSelected(null)} onUpdate={load} refreshTrigger={msgRefresh} />
      </Layout>
    )
  }

  return (
    <Layout title="Tickets">
      {/* Crear ticket manual */}
      <div className="card mb-16">
        <div className="flex items-center gap-8 mb-12">
          <Plus size={15} color="var(--blue)" />
          <div className="card-title">Nuevo ticket</div>
        </div>
        <div className="flex gap-8">
          <input className="input" style={{ width: 160 }} value={newPc} onChange={e => setNewPc(e.target.value)} placeholder="Equipo (opcional)" />
          <input className="input flex-1" value={newMsg} onChange={e => setNewMsg(e.target.value)}
            onKeyDown={e => e.key === 'Enter' && createTicket()}
            placeholder="Descripción del problema..." />
          <button className="btn btn-primary" onClick={createTicket} disabled={creating || !newMsg.trim()}>
            {creating ? <><span className="spinner spinner-sm" /> Creando...</> : <><Plus size={13} /> Crear</>}
          </button>
        </div>
      </div>

      {/* Filtros */}
      <div className="flex gap-8 mb-16">
        {['all', ...STATUS_OPTS].map(s => (
          <button key={s} className={`btn btn-sm ${filter === s ? 'btn-primary' : 'btn-ghost'}`}
            onClick={() => setFilter(s)}>
            {s === 'all' ? 'Todos' : s}
          </button>
        ))}
        <span className="text-muted text-small" style={{ marginLeft: 'auto', alignSelf: 'center' }}>
          {filtered.length} tickets
        </span>
      </div>

      <div className="card">
        {loading ? (
          <div className="empty-state"><div className="loading spin" /></div>
        ) : filtered.length === 0 ? (
          <div className="empty-state">
            <div className="empty-state-icon"><TicketIcon size={40} color="var(--muted)" /></div>
            <div className="empty-state-title">Sin tickets {filter !== 'all' ? filter : ''}</div>
          </div>
        ) : (
          <div className="table-wrap">
            <table>
              <thead><tr><th>#</th><th>Equipo</th><th>Mensaje</th><th>Categoría</th><th>Prioridad</th><th>Estado</th><th>Fecha</th></tr></thead>
              <tbody>
                {filtered.map(t => (
                  <tr key={t.id} className="clickable-row" onClick={() => setSelected(t)}>
                    <td style={{ color: 'var(--muted)', fontSize: 12 }}>#{t.id}</td>
                    <td style={{ fontWeight: 500 }}>{t.pc_name}</td>
                    <td className="truncate" style={{ maxWidth: 280 }}>{t.message}</td>
                    <td>{t.category}</td>
                    <td><span className={`badge badge-${PRIORITY_COLOR[t.priority] ?? 'info'}`}>{t.priority}</span></td>
                    <td><span className={`badge badge-${t.status}`}>{t.status}</span></td>
                    <td className="text-muted text-small">{new Date(t.created_at).toLocaleDateString('es-UY')}</td>
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
