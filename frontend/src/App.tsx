import { BrowserRouter, Routes, Route } from 'react-router-dom'
import { Dashboard }   from './pages/Dashboard'
import { Agents }      from './pages/Agents'
import { AgentDetail } from './pages/AgentDetail'
import { Tickets }     from './pages/Tickets'
import { Alerts }      from './pages/Alerts'
import { Console }     from './pages/Console'
import { AIAssistant } from './pages/AIAssistant'
import { Settings }    from './pages/Settings'

export default function App() {
  return (
    <BrowserRouter>
      <Routes>
        <Route path="/"            element={<Dashboard />} />
        <Route path="/agents"      element={<Agents />} />
        <Route path="/agents/:id"  element={<AgentDetail />} />
        <Route path="/tickets"     element={<Tickets />} />
        <Route path="/alerts"      element={<Alerts />} />
        <Route path="/console"     element={<Console />} />
        <Route path="/ai"          element={<AIAssistant />} />
        <Route path="/settings"    element={<Settings />} />
      </Routes>
    </BrowserRouter>
  )
}
