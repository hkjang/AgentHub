import { createContext, lazy, Suspense, useCallback, useContext, useEffect, useState } from 'react'
import { Navigate, Route, Routes } from 'react-router-dom'
import { api, UNAUTHORIZED_EVENT } from './api'
import { setViewModeScope } from './viewmode'
import { setRunnerExperience, setRuntimeDescriptors, type RunnerExperience, type RuntimeDescriptor } from './runtime'
import type { User, Version } from './types'
import { AppShell } from './components/AppShell'
import { Loading } from './components/UI'
import { Login } from './pages/Login'
import { Dashboard } from './pages/Dashboard'

// Every screen but the first is fetched when somebody opens it. The console
// shipped as one file holding all of them: 629 kB, parsed before anything
// rendered, on a platform that is often installed where the network is the
// slow part. Login and the dashboard stay in the first file because they are
// what a person sees before they have chosen anything.
const AdminDLP = lazy(() => import('./pages/AdminDLP').then((m) => ({ default: m.AdminDLP })))
const AdminExecution = lazy(() => import('./pages/AdminExecution').then((m) => ({ default: m.AdminExecution })))
const AdminInsights = lazy(() => import('./pages/AdminInsights').then((m) => ({ default: m.AdminInsights })))
const AdminOperations = lazy(() => import('./pages/AdminOperations').then((m) => ({ default: m.AdminOperations })))
const AdminPolicy = lazy(() => import('./pages/AdminPolicy').then((m) => ({ default: m.AdminPolicy })))
const AdminQuota = lazy(() => import('./pages/AdminQuota').then((m) => ({ default: m.AdminQuota })))
const AdminResources = lazy(() => import('./pages/AdminResources').then((m) => ({ default: m.AdminResources })))
const AdminRuntimeSettings = lazy(() => import('./pages/AdminRuntimeSettings').then((m) => ({ default: m.AdminRuntimeSettings })))
const AdminSecurity = lazy(() => import('./pages/AdminSecurity').then((m) => ({ default: m.AdminSecurity })))
const AdminSettings = lazy(() => import('./pages/AdminSettings').then((m) => ({ default: m.AdminSettings })))
const AdminUsers = lazy(() => import('./pages/AdminUsers').then((m) => ({ default: m.AdminUsers })))
const Agents = lazy(() => import('./pages/Agents').then((m) => ({ default: m.Agents })))
const Catalog = lazy(() => import('./pages/Catalog').then((m) => ({ default: m.Catalog })))
const CodeReview = lazy(() => import('./pages/CodeReview').then((m) => ({ default: m.CodeReview })))
const Developer = lazy(() => import('./pages/Developer').then((m) => ({ default: m.Developer })))
const Evaluation = lazy(() => import('./pages/Evaluation').then((m) => ({ default: m.Evaluation })))
const MCPFabric = lazy(() => import('./pages/MCPFabric').then((m) => ({ default: m.MCPFabric })))
const Reviews = lazy(() => import('./pages/Reviews').then((m) => ({ default: m.Reviews })))
const Runs = lazy(() => import('./pages/Runs').then((m) => ({ default: m.Runs })))
const Sessions = lazy(() => import('./pages/Sessions').then((m) => ({ default: m.Sessions })))
const Snapshots = lazy(() => import('./pages/Snapshots').then((m) => ({ default: m.Snapshots })))
const Tasks = lazy(() => import('./pages/Tasks').then((m) => ({ default: m.Tasks })))
const Workflows = lazy(() => import('./pages/Workflows').then((m) => ({ default: m.Workflows })))
const Workspaces = lazy(() => import('./pages/Workspaces').then((m) => ({ default: m.Workspaces })))

export type Capabilities = { teamApprovalEnabled: boolean; highRiskToolApproval: boolean; kubernetesEnabled: boolean; mcpProtocolVersion: string; executionPaused?: boolean; executionPausedReason?: string }
type AuthContextValue = { user: User; version: Version; capabilities: Capabilities; refresh: () => Promise<void>; logout: () => Promise<void> }
const AuthContext = createContext<AuthContextValue | null>(null)
export function useAuth() { const value = useContext(AuthContext); if (!value) throw new Error('AuthContext missing'); return value }

const emptyVersion: Version = { name: 'AgentHub', version: '—', commit: 'unknown', buildTime: 'unknown' }

export function App() {
  const [user, setUser] = useState<User | null | undefined>(undefined)
  const [version, setVersion] = useState<Version>(emptyVersion)
  const [capabilities, setCapabilities] = useState<Capabilities>({teamApprovalEnabled:false,highRiskToolApproval:true,kubernetesEnabled:false,mcpProtocolVersion:'2026-07-28'})

  // Bumped when the platform's runtime descriptions arrive. Nothing reads the
  // number: it exists to re-render what was drawn from the seed.
  const [, setDescribed] = useState(0)

  const refresh = useCallback(async () => {
    try {
      const result = await api.get<{ user: User; version: Version }>('/api/v1/me')
      setUser(result.user); setVersion(result.version)
      // The reading preference belongs to the person, not the browser: a shared
      // machine must not hand one person's console to whoever signs in next.
      setViewModeScope(result.user.id)
      api.get<Capabilities>('/api/v1/capabilities').then(setCapabilities).catch(() => undefined)
      // What each runtime is and is good at comes from the platform, so the
      // console cannot describe an adapter this build does not have.
      api.get<{items: RuntimeDescriptor[]; runners?: Record<string, RunnerExperience>}>('/api/v1/runtime-types')
        // The descriptions live outside React, so a screen already on the page
        // would keep showing the seeded list until something else re-rendered
        // it. The counter is what carries the platform's answer to it.
        .then((value) => { setRuntimeDescriptors(value.items); setRunnerExperience(value.runners); setDescribed((n) => n + 1) })
        .catch(() => undefined)
    } catch { setUser(null); setViewModeScope('') }
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
    <Suspense fallback={<Loading />}>
      <Routes>
        <Route element={<AppShell />}>
          <Route index element={<Dashboard />} />
          <Route path="catalog" element={<Catalog />} />
          <Route path="agents" element={<Agents />} />
          <Route path="agents/builder" element={<Catalog builder />} />
          <Route path="workspaces" element={<Workspaces />} />
          <Route path="workspaces/snapshots" element={<Snapshots />} />
          <Route path="runs" element={<Runs />} />
          <Route path="code-review" element={<CodeReview />} />
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
          <Route path="admin/agent-servers" element={<AdminResources kind="servers" />} />
          <Route path="admin/mcp" element={<AdminResources kind="mcp" />} />
          <Route path="admin/mcp-bundles" element={<AdminResources kind="bundles" />} />
          <Route path="admin/users" element={<AdminUsers />} />
          <Route path="admin/quotas" element={<AdminQuota />} />
          <Route path="admin/security" element={<AdminSecurity />} />
          <Route path="admin/*" element={<Navigate to="/admin/settings" replace />} />
          <Route path="*" element={<Navigate to="/" replace />} />
        </Route>
      </Routes>
    </Suspense>
  </AuthContext.Provider>
}
