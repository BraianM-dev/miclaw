import { NavLink } from 'react-router-dom'
import type { ReactNode } from 'react'
import { useState, useEffect } from 'react'
import {
  LayoutDashboard, Monitor, Ticket, Bell, Terminal,
  Bot, Settings, Wifi, WifiOff, ShieldCheck,
} from 'lucide-react'
import { connectEvents } from '../api/client'
import type { SSEEvent } from '../types'

interface LayoutProps {
  children: ReactNode
  title: string
}

const NAV = [
  { to: '/',        Icon: LayoutDashboard, label: 'Dashboard' },
  { to: '/agents',  Icon: Monitor,         label: 'Agentes' },
  { to: '/tickets', Icon: Ticket,          label: 'Tickets' },
  { to: '/alerts',  Icon: Bell,            label: 'Alertas' },
  { to: '/console', Icon: Terminal,        label: 'Consola' },
  { to: '/ai',      Icon: Bot,             label: 'IA Asistente' },
  { to: '/settings',Icon: Settings,        label: 'Configuración' },
]

export function Layout({ children, title }: LayoutProps) {
  const [connected, setConnected] = useState(false)

  useEffect(() => {
    let alive = true
    const unsub = connectEvents((_ev: SSEEvent) => {
      if (alive) setConnected(true)
    })
    return () => {
      alive = false
      unsub()
      setConnected(false)
    }
  }, [])

  return (
    <div className="layout">
      {/* ── Sidebar ── */}
      <aside className="sidebar">
        <div className="sidebar-logo">
          <div className="sidebar-logo-icon">
            <ShieldCheck size={16} color="#58a6ff" />
          </div>
          <div>
            <div className="sidebar-logo-text">MicLaw</div>
            <div className="sidebar-logo-sub">IT Operations</div>
          </div>
        </div>

        <nav className="sidebar-nav">
          <div className="nav-section">Operaciones</div>
          {NAV.slice(0, 6).map(({ to, Icon, label }) => (
            <NavLink
              key={to}
              to={to}
              end={to === '/'}
              className={({ isActive }) => `nav-item${isActive ? ' active' : ''}`}
            >
              <span className="nav-item-icon"><Icon size={16} /></span>
              <span className="nav-item-label">{label}</span>
            </NavLink>
          ))}
          <div className="nav-section" style={{ marginTop: 8 }}>Sistema</div>
          {NAV.slice(6).map(({ to, Icon, label }) => (
            <NavLink
              key={to}
              to={to}
              className={({ isActive }) => `nav-item${isActive ? ' active' : ''}`}
            >
              <span className="nav-item-icon"><Icon size={16} /></span>
              <span className="nav-item-label">{label}</span>
            </NavLink>
          ))}
        </nav>

        <div className="sidebar-footer">
          <div className="flex items-center gap-6">
            <span className={`dot dot-${connected ? 'connected' : 'disconnected'}`} />
            <span className="text-xs text-muted">{connected ? 'Conectado' : 'Sin conexión'}</span>
          </div>
          <div className="text-xs text-muted mt-4">v2.0 — AFE</div>
        </div>
      </aside>

      {/* ── Main ── */}
      <div className="main-content">
        <header className="topbar">
          <span className="topbar-title">{title}</span>
          <div className="topbar-right">
            <div className="connection-status">
              {connected
                ? <><Wifi size={13} color="var(--success)" /> <span style={{ color: 'var(--success)' }}>En línea</span></>
                : <><WifiOff size={13} color="var(--danger)" /> <span style={{ color: 'var(--danger)' }}>Desconectado</span></>
              }
            </div>
            <span className="text-small text-muted">
              {new Date().toLocaleDateString('es-AR', { weekday: 'short', day: 'numeric', month: 'short' })}
            </span>
          </div>
        </header>
        <main className="page">
          {children}
        </main>
      </div>
    </div>
  )
}
