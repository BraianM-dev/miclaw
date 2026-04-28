import { useState, useEffect } from 'react'
import { Layout } from '../components/Layout'
import { api } from '../api/client'
import type { GatewaySettings } from '../types'
import { Key, Server, Bell, Brain, CheckCircle, AlertCircle } from 'lucide-react'

export function Settings() {
  const [apiKey, setApiKey]       = useState('')
  const [testState, setTestState] = useState<'idle' | 'testing' | 'ok' | 'fail'>('idle')
  const [settings, setSettings]   = useState<GatewaySettings | null>(null)
  const [saving, setSaving]       = useState(false)
  const [saved, setSaved]         = useState(false)

  useEffect(() => {
    // Cargar API key guardada
    setApiKey(localStorage.getItem('miclaw_api_key') || '')
    // Cargar settings del gateway
    api.getSettings().then(setSettings).catch(() => {})
  }, [])

  const saveApiKey = () => {
    if (apiKey.trim()) {
      localStorage.setItem('miclaw_api_key', apiKey.trim())
    } else {
      localStorage.removeItem('miclaw_api_key')
    }
    setSaved(true)
    setTimeout(() => setSaved(false), 2000)
  }

  const testConnection = async () => {
    setTestState('testing')
    try {
      const h = await api.health()
      setTestState(h.status === 'ok' ? 'ok' : 'fail')
    } catch {
      setTestState('fail')
    }
    setTimeout(() => setTestState('idle'), 3000)
  }

  const saveGatewaySettings = async () => {
    if (!settings) return
    setSaving(true)
    try {
      await api.saveSettings(settings)
      setSaved(true)
      setTimeout(() => setSaved(false), 2000)
    } catch (e) {
      alert('Error al guardar: ' + (e as Error).message)
    } finally {
      setSaving(false)
    }
  }

  return (
    <Layout title="Configuración">
      {/* API Key */}
      <div className="settings-section">
        <div className="flex items-center gap-8 mb-4">
          <Key size={18} color="var(--blue)" />
          <div>
            <div className="settings-section-title">Autenticación</div>
            <div className="settings-section-sub">Clave de API para conectar con el gateway</div>
          </div>
        </div>

        <div className="settings-row">
          <div>
            <div className="settings-row-label">API Key</div>
            <div className="settings-row-sub">
              La misma clave configurada en MICLAW_AGENT_KEY del gateway
            </div>
          </div>
          <div className="flex gap-8" style={{ minWidth: 320 }}>
            <input
              type="password"
              className="input"
              placeholder="Ingresá tu API key..."
              value={apiKey}
              onChange={e => setApiKey(e.target.value)}
              onKeyDown={e => e.key === 'Enter' && saveApiKey()}
            />
          </div>
        </div>

        <div className="flex gap-8 mt-16" style={{ justifyContent: 'flex-end' }}>
          <button className="btn btn-ghost" onClick={testConnection} disabled={testState === 'testing'}>
            {testState === 'testing' && <span className="spinner spinner-sm" />}
            {testState === 'ok'      && <CheckCircle size={14} color="var(--success)" />}
            {testState === 'fail'    && <AlertCircle size={14} color="var(--danger)" />}
            {testState === 'idle'    && <Server size={14} />}
            {testState === 'testing' ? 'Probando...' : testState === 'ok' ? 'Conectado!' : testState === 'fail' ? 'Sin conexión' : 'Probar conexión'}
          </button>
          <button className="btn btn-primary" onClick={saveApiKey}>
            {saved ? <><CheckCircle size={14} /> Guardado</> : 'Guardar clave'}
          </button>
        </div>

        <div className="alert-banner info mt-16">
          <AlertCircle size={15} style={{ flexShrink: 0, marginTop: 1 }} />
          <div>
            Si el dashboard muestra "unauthorized", ingresá la clave correcta y hacé click en "Guardar clave".
            La clave se guarda en el navegador y se usa en todas las consultas al gateway.
          </div>
        </div>
      </div>

      {/* Gateway Settings */}
      {settings && (
        <div className="settings-section">
          <div className="flex items-center gap-8 mb-4">
            <Bell size={18} color="var(--warning)" />
            <div>
              <div className="settings-section-title">Alertas automáticas</div>
              <div className="settings-section-sub">Umbrales para generar alertas desde los heartbeats de los agentes</div>
            </div>
          </div>

          <div className="settings-row">
            <div>
              <div className="settings-row-label">CPU — umbral de alerta (%)</div>
              <div className="settings-row-sub">Genera alerta cuando CPU supere este valor</div>
            </div>
            <input
              type="number" className="input" style={{ width: 100 }}
              min={50} max={100}
              value={settings.alert_cpu_threshold}
              onChange={e => setSettings({ ...settings, alert_cpu_threshold: +e.target.value })}
            />
          </div>

          <div className="settings-row">
            <div>
              <div className="settings-row-label">RAM — umbral de alerta (%)</div>
              <div className="settings-row-sub">Genera alerta cuando memoria supere este valor</div>
            </div>
            <input
              type="number" className="input" style={{ width: 100 }}
              min={50} max={100}
              value={settings.alert_mem_threshold}
              onChange={e => setSettings({ ...settings, alert_mem_threshold: +e.target.value })}
            />
          </div>

          <div className="settings-row">
            <div>
              <div className="settings-row-label">Disco — umbral de alerta (%)</div>
              <div className="settings-row-sub">Genera alerta cuando disco supere este valor</div>
            </div>
            <input
              type="number" className="input" style={{ width: 100 }}
              min={50} max={100}
              value={settings.alert_disk_threshold}
              onChange={e => setSettings({ ...settings, alert_disk_threshold: +e.target.value })}
            />
          </div>

          <div className="settings-row">
            <div>
              <div className="settings-row-label">Auto-cerrar tickets (días)</div>
              <div className="settings-row-sub">Días sin actividad para cerrar tickets automáticamente (0 = desactivado)</div>
            </div>
            <input
              type="number" className="input" style={{ width: 100 }}
              min={0} max={365}
              value={settings.auto_close_days}
              onChange={e => setSettings({ ...settings, auto_close_days: +e.target.value })}
            />
          </div>
        </div>
      )}

      {/* Ollama / IA */}
      {settings && (
        <div className="settings-section">
          <div className="flex items-center gap-8 mb-4">
            <Brain size={18} color="var(--blue)" />
            <div>
              <div className="settings-section-title">Inteligencia Artificial</div>
              <div className="settings-section-sub">Configuración de Ollama para el asistente IA</div>
            </div>
          </div>

          <div className="settings-row">
            <div>
              <div className="settings-row-label">Modelo Ollama</div>
              <div className="settings-row-sub">Modelo activo en el contenedor Ollama</div>
            </div>
            <input
              className="input" style={{ width: 220 }}
              value={settings.ollama_model}
              onChange={e => setSettings({ ...settings, ollama_model: e.target.value })}
              placeholder="phi4-mini:3.8b"
            />
          </div>

          <div className="settings-row">
            <div>
              <div className="settings-row-label">IA habilitada</div>
              <div className="settings-row-sub">Activar o desactivar el asistente IA</div>
            </div>
            <label className="toggle">
              <input
                type="checkbox"
                checked={settings.ollama_enabled}
                onChange={e => setSettings({ ...settings, ollama_enabled: e.target.checked })}
              />
              <span className="toggle-slider" />
            </label>
          </div>
        </div>
      )}

      {settings && (
        <div className="flex justify-end">
          <button className="btn btn-primary" onClick={saveGatewaySettings} disabled={saving}>
            {saving ? <><span className="spinner spinner-sm" /> Guardando...</> :
             saved  ? <><CheckCircle size={14} /> Guardado</> : 'Guardar configuración'}
          </button>
        </div>
      )}
    </Layout>
  )
}
