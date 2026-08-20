import { createContext, useCallback, useContext, useEffect, useState } from 'react'
import { Navigate, Route, Routes } from 'react-router-dom'
import { api, UNAUTHORIZED_EVENT } from './api'
import { setRuntimeDescriptors, type RuntimeDescriptor } from './runtime'
import type { User, Version } from './types'
import { AppShell } from './components/AppShell'
import { Login } from './pages/Login'
import { Dashboard } from './pages/Dashboard'
import { Catalog } from './pages/Catalog'
import { Agents } from './pages/Agents'
import { Workspaces } from './pages/Workspaces'
import { Developer } from './pages/Developer'
import { AdminSettings } from './pages/AdminSettings'
import { AdminDLP } from './pages/AdminDLP'
import { AdminExecution } from './pages/AdminExecution'
import { AdminInsights } from './pages/AdminInsights'
import { AdminPolicy } from './pages/AdminPolicy'
import { AdminRuntimeSettings } from './pages/AdminRuntimeSettings'
import { AdminOperations } from './pages/AdminOperations'
import { Reviews } from './pages/Reviews'
import { AdminResources } from './pages/AdminResources'
import { AdminUsers } from './pages/AdminUsers'
import { Snapshots } from './pages/Snapshots'
import { MCPFabric } from './pages/MCPFabric'
import { AdminSecurity } from './pages/AdminSecurity'
import { Sessions } from './pages/Sessions'
import { Tasks } from './pages/Tasks'
import { Workflows } from './pages/Workflows'
import { Evaluation } from './pages/Evaluation'

export type Capabilities = { teamApprovalEnabled: boolean; highRiskToolApproval: boolean; kubernetesEnabled: boolean; mcpProtocolVersion: string; executionPaused?: boolean; executionPausedReason?: string }
type AuthContextValue = { user: User; version: Version; capabilities: Capabilities; refresh: () => Promise<void>; logout: () => Promise<void> }
const AuthContext = createContext<AuthContextValue | null>(null)
export function useAuth() { const value = useContext(AuthContext); if (!value) throw new Error('AuthContext missing'); return value }

const emptyVersion: Version = { name: 'AgentHub', version: '—', commit: 'unknown', buildTime: 'unknown' }

export function App() {
  const [user, setUser] = useState<User | null | undefined>(undefined)
  const [version, setVersion] = useState<Version>(emptyVersion)
  const [capabilities, setCapabilities] = useState<Capabilities>({teamApprovalEnabled:false,highRiskToolApproval:true,kubernetesEnabled:false,mcpProtocolVersion:'2026-07-28'})

  const refresh = useCallback(async () => {
    try {
      const result = await api.get<{ user: User; version: Version }>('/api/v1/me')
      setUser(result.user); setVersion(result.version)
      api.get<Capabilities>('/api/v1/capabilities').then(setCapabilities).catch(() => undefined)
      // What each runtime is and is good at comes from the platform, so the
      // console cannot describe an adapter this build does not have.
      api.get<{items: RuntimeDescriptor[]}>('/api/v1/runtime-types')
        .then((value) => setRuntimeDescriptors(value.items)).catch(() => undefined)
    } catch { setUser(null) }
  }, [])

  useEffect(() => {
    api.get<Version>('/api/v1/version').then(setVersion).catch(() => undefined)
    void refresh()
  }, [refresh])

  // An expired session takes the user back to the sign-in page instead of leaving
  // every screen behind an error banner it cannot recover from.
  useEffect(() => {
    const listener = () => setUser((current) => (current ? null : current))
    window.addEventListener(UNAUTHORIZED_EVENT, listener)
    return () => window.removeEventListener(UNAUTHORIZED_EVENT, listener)
  }, [])

  const logout = async () => { await api.post('/api/v1/auth/logout'); setUser(null) }
  if (user === undefined) return <div className="boot"><img src="/logo.svg" alt="AgentHub Logo" className="brand-logo-img large" /><span>AgentHub를 준비하고 있습니다</span></div>
  if (!user) return <Login version={version} onLogin={refresh} />

  return <AuthContext.Provider value={{ user, version, capabilities, refresh, logout }}>
    <Routes>
      <Route element={<AppShell />}>
        <Route index element={<Dashboard />} />
        <Route path="catalog" element={<Catalog />} />
        <Route path="agents" element={<Agents />} />
        <Route path="agents/builder" element={<Catalog builder />} />
        <Route path="workspaces" element={<Workspaces />} />
        <Route path="workspaces/snapshots" element={<Snapshots />} />
        <Route path="mcp/catalog" element={<MCPFabric view="catalog" />} />
        <Route path="mcp/bundles" element={<MCPFabric view="bundles" />} />
        <Route path="runtime" element={<Agents runtimeOnly />} />
        <Route path="sessions" element={<Sessions />} />
        <Route path="tasks" element={<Tasks />} />
        <Route path="workflows" element={<Workflows />} />
        <Route path="evaluation" element={<Evaluation />} />
        <Route path="reviews" element={<Reviews />} />
        <Route path="developer" element={<Developer />} />
        <Route path="admin/settings" element={<AdminSettings />} />
        <Route path="admin/overview" element={<AdminInsights />} />
        <Route path="admin/execution" element={<AdminExecution />} />
        <Route path="admin/policy" element={<AdminPolicy />} />
        <Route path="admin/dlp" element={<AdminDLP />} />
        <Route path="admin/runtime-settings" element={<AdminRuntimeSettings />} />
        <Route path="admin/operations" element={<AdminOperations />} />
        <Route path="admin/runtime-profiles" element={<AdminResources kind="profiles" />} />
        <Route path="admin/runtime-images" element={<AdminResources kind="images" />} />
        <Route path="admin/models" element={<AdminResources kind="models" />} />
        <Route path="admin/external-apps" element={<AdminResources kind="apps" />} />
        <Route path="admin/mcp" element={<AdminResources kind="mcp" />} />
        <Route path="admin/mcp-bundles" element={<AdminResources kind="bundles" />} />
        <Route path="admin/users" element={<AdminUsers />} />
        <Route path="admin/security" element={<AdminSecurity />} />
        <Route path="admin/*" element={<Navigate to="/admin/settings" replace />} />
        <Route path="*" element={<Navigate to="/" replace />} />
      </Route>
    </Routes>
  </AuthContext.Provider>
}
