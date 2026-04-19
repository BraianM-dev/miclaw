import { NavLink, useLocation } from 'react-router-dom'
import type { ReactNode } from 'react'

interface LayoutProps {
  children: ReactNode
  title: string
}

const NAV = [
  { to: '/',         icon: '📊', label: 'Dashboard' },
  { to: '/agents',   icon: '🖥️', label: 'Agentes' },
  { to: '/tickets',  icon: '🎫', label: 'Tickets' },
  { to: '/alerts',   icon: '🔔', label: 'Alertas' },
  { to: '/console',  icon: '⌨️', label: 'Consola' },
  { to: '/ai',       icon: '🤖', label: 'IA Asistente' },
]

export function Layout({ children, title }: LayoutProps) {
  const location = useLocation()

  return (
    <div className="app-shell">
      {/* ── Sidebar ── */}
      <aside className="sidebar">
        <div className="sidebar-logo">
          <span style={{ fontSize: 24 }}>🛡️</span>
          <div>
            <div className="sidebar-logo-text">MicLaw</div>
            <div className="sidebar-logo-sub">IT Operations</div>
          </div>
        </div>

        <nav className="sidebar-nav">
          <div className="nav-section">Operaciones</div>
          {NAV.map(({ to, icon, label }) => (
            <NavLink
              key={to}
              to={to}
              end={to === '/'}
              className={({ isActive }) => `nav-item${isActive ? ' active' : ''}`}
            >
              <span className="icon">{icon}</span>
              <span>{label}</span>
            </NavLink>
          ))}
        </nav>

        <div style={{ padding: '12px 16px', borderTop: '1px solid var(--border)' }}>
          <div className="text-small text-muted">v1.0.0 — AFE</div>
        </div>
      </aside>

      {/* ── Main ── */}
      <div className="main-area">
        <header className="topbar">
          <span className="topbar-title">{title}</span>
          <div className="topbar-actions">
            <span className="text-small text-muted">
              {new Date().toLocaleDateString('es-UY', { weekday: 'long', day: 'numeric', month: 'long' })}
            </span>
          </div>
        </header>
        <main className="page-content">
          {children}
        </main>
      </div>
    </div>
  )
}
