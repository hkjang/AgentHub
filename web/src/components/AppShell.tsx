import { useEffect, useMemo, useState } from 'react'
import { NavLink, Outlet, useLocation, useNavigate } from 'react-router-dom'
import {
  Activity, Bell, Boxes, Bot, Braces, ChevronDown, ChevronRight, CircleUserRound, Command,
  Database, FileCode2, Gauge, KeyRound, LayoutDashboard, Library, LogOut, Menu, Network,
  Search, Settings, ShieldCheck, Sparkles, UsersRound, Workflow, X
} from 'lucide-react'
import { useAuth } from '../App'
import { api } from '../api'
import type { Notification } from '../types'

type NavItem = { to: string; label: string; icon: typeof Gauge }
type NavGroup = { label: string; items: NavItem[]; admin?: boolean; review?: boolean }
const groups: NavGroup[] = [
  { label: 'Overview', items: [{ to: '/', label: 'Home', icon: LayoutDashboard }] },
  { label: 'Agents', items: [
    { to: '/catalog', label: 'Agent Catalog', icon: Library }, { to: '/agents', label: 'My Agents', icon: Bot },
    { to: '/agents/builder', label: 'Agent Builder', icon: Sparkles }, { to: '/workflows', label: 'Workflows', icon: Workflow }
  ]},
  { label: 'Workspace & Runtime', items: [
    { to: '/workspaces', label: 'Workspaces', icon: Database }, { to: '/workspaces/snapshots', label: 'Snapshots', icon: Boxes },
    { to: '/runtime', label: 'Runtimes', icon: Activity }, { to: '/sessions', label: 'Sessions', icon: FileCode2 }
  ]},
  { label: 'Integration', items: [
    { to: '/mcp/catalog', label: 'MCP Catalog', icon: Network }, { to: '/mcp/bundles', label: 'MCP Bundles', icon: Boxes },
    { to: '/developer', label: 'Secrets & API', icon: Braces }, { to: '/evaluation', label: 'Evaluation', icon: ShieldCheck }
  ]},
  { label: 'Governance', review: true, items: [{ to: '/reviews', label: 'Reviews & Approvals', icon: ShieldCheck }] },
  { label: 'Administration', admin: true, items: [
    { to: '/admin/operations', label: 'Control Center', icon: Gauge }, { to: '/admin/runtime-profiles', label: 'Runtime Profiles', icon: Bot },
    { to: '/admin/runtime-images', label: 'Runtime Images', icon: Boxes }, { to: '/admin/models', label: 'Models', icon: Sparkles },
    { to: '/admin/mcp', label: 'MCP Servers', icon: Network }, { to: '/admin/mcp-bundles', label: 'MCP Bundles', icon: Boxes }, { to: '/admin/users', label: 'Users & Teams', icon: UsersRound }, { to: '/admin/security', label: 'Security & Network', icon: ShieldCheck },
    { to: '/admin/settings', label: 'System Settings', icon: Settings }
  ]}
]

export function AppShell() {
  const { user, version, capabilities, logout } = useAuth()
  const [sidebar, setSidebar] = useState(false), [command, setCommand] = useState(false), [profile, setProfile] = useState(false)
  const [notificationOpen,setNotificationOpen]=useState(false),[notifications,setNotifications]=useState<Notification[]>([])
  const [collapsed, setCollapsed] = useState<Record<string, boolean>>({})
  const location = useLocation(), navigate = useNavigate()
  const visibleGroups = useMemo(() => groups.filter((g) => (!g.admin || user.role === 'admin') && (!g.review || (capabilities.teamApprovalEnabled && (user.role === 'manager' || user.role === 'admin')))), [user.role, capabilities.teamApprovalEnabled])
  const active = visibleGroups.flatMap((g) => g.items).find((item) => item.to === '/' ? location.pathname === '/' : location.pathname.startsWith(item.to))

  useEffect(() => {
    const listener = (event: KeyboardEvent) => {
      if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === 'k') { event.preventDefault(); setCommand((v) => !v) }
      if (event.key === 'Escape') { setCommand(false); setProfile(false); setNotificationOpen(false); setSidebar(false) }
    }
    window.addEventListener('keydown', listener); return () => window.removeEventListener('keydown', listener)
  }, [])

  useEffect(()=>{const load=()=>api.get<{items:Notification[]}>('/api/v1/notifications').then((value)=>setNotifications(value.items)).catch(()=>undefined);void load();const timer=window.setInterval(()=>void load(),30000);return()=>window.clearInterval(timer)},[])
  const openNotification=async(item:Notification)=>{if(!item.readAt){await api.post(`/api/v1/notifications/${item.id}/read`).catch(()=>undefined);setNotifications((values)=>values.map((value)=>value.id===item.id?{...value,readAt:new Date().toISOString()}:value))}setNotificationOpen(false);if(item.resourceUrl)navigate(item.resourceUrl)}

  return <div className="app-frame">
    <aside className={`sidebar ${sidebar ? 'sidebar-open' : ''}`}>
      <div className="sidebar-brand"><img src="/logo.svg" alt="AgentHub Logo" className="brand-logo-img" /><div><strong>AgentHub</strong><span>Runtime Platform</span></div><button className="icon-button mobile-only" onClick={() => setSidebar(false)} aria-label="메뉴 닫기"><X size={18}/></button></div>
      <button className="quick-button" onClick={() => setCommand(true)}><Search size={16}/><span>빠른 이동</span><kbd>⌘ K</kbd></button>
      <nav className="nav-scroll" aria-label="주 메뉴">
        {visibleGroups.map((group) => <section className="nav-group" key={group.label}>
          <button className="nav-group-title" onClick={() => setCollapsed((v) => ({...v, [group.label]: !v[group.label]}))} aria-expanded={!collapsed[group.label]}>
            <span>{group.label}</span>{collapsed[group.label] ? <ChevronRight size={14}/> : <ChevronDown size={14}/>}
          </button>
          {!collapsed[group.label] && group.items.map(({to,label,icon:Icon}) => <NavLink end={to === '/'} to={to} key={to} onClick={() => setSidebar(false)} className={({isActive}) => `nav-link ${isActive ? 'active' : ''}`}><Icon size={18}/><span>{label}</span></NavLink>)}
        </section>)}
      </nav>
      <div className="sidebar-status"><span className="status-dot online"/><div><strong>Control Plane</strong><span>Online · v{version.version}</span></div></div>
    </aside>
    {sidebar && <button className="sidebar-scrim" aria-label="메뉴 닫기" onClick={() => setSidebar(false)}/>}
    <div className="main-column">
      <header className="topbar">
        <button className="icon-button mobile-only" onClick={() => setSidebar(true)} aria-label="메뉴 열기"><Menu size={20}/></button>
        <div className="breadcrumb"><span>AgentHub</span><ChevronRight size={14}/><strong>{active?.label ?? 'Workspace'}</strong></div>
        <div className="top-actions">
          <button className="command-chip" onClick={() => setCommand(true)}><Command size={15}/><span>빠른 이동</span></button>
          <div className="notification-wrap"><button className="icon-button notification-button" onClick={()=>{setProfile(false);setNotificationOpen((value)=>!value)}} aria-label="알림" aria-expanded={notificationOpen}><Bell size={18}/>{notifications.some((item)=>!item.readAt)&&<i/>}</button>{notificationOpen&&<div className="notification-menu custom-scroll"><header><strong>Notifications</strong><span>{notifications.filter((item)=>!item.readAt).length} unread</span></header>{notifications.length===0?<div className="empty-compact">새 알림이 없습니다.</div>:notifications.map((item)=><button key={item.id} className={item.readAt?'read':''} onClick={()=>void openNotification(item)}><i/><span><strong>{item.title}</strong><small>{item.message}</small><time>{new Date(item.createdAt).toLocaleString('ko-KR')}</time></span></button>)}</div>}</div>
          <div className="profile-wrap"><button className="profile-button" onClick={() => {setNotificationOpen(false);setProfile((v) => !v)}} aria-expanded={profile}><div className="avatar">{user.displayName.slice(0,1).toUpperCase()}</div><div><strong>{user.displayName}</strong><span>{user.role}</span></div><ChevronDown size={15}/></button>
            {profile && <div className="profile-menu"><div className="profile-card"><CircleUserRound size={20}/><div><strong>{user.displayName}</strong><span>{user.email || user.username}</span></div></div><NavLink to="/developer" onClick={() => setProfile(false)}><KeyRound size={16}/>개인 키와 API</NavLink><div className="version-row"><span>AgentHub</span><code>v{version.version}</code><small>{version.commit.slice(0,8)}</small></div><button onClick={() => void logout()}><LogOut size={16}/>로그아웃</button></div>}
          </div>
        </div>
      </header>
      <main className="content-scroll"><Outlet /></main>
    </div>
    {command && <CommandPalette items={visibleGroups.flatMap((g) => g.items)} close={() => setCommand(false)} go={(to) => {navigate(to);setCommand(false)}}/>}
  </div>
}

function CommandPalette({items,close,go}:{items:NavItem[];close:()=>void;go:(to:string)=>void}) {
  const [query,setQuery] = useState('')
  const results = items.filter((item) => item.label.toLowerCase().includes(query.toLowerCase()))
  return <div className="modal-layer" role="dialog" aria-modal="true" aria-label="빠른 이동"><button className="modal-scrim" onClick={close} aria-label="닫기"/><div className="command-panel"><div className="command-input"><Search size={20}/><input autoFocus value={query} onChange={(e)=>setQuery(e.target.value)} placeholder="메뉴, Agent, 설정 검색…"/><kbd>ESC</kbd></div><div className="command-results custom-scroll">{results.map(({to,label,icon:Icon})=><button key={to} onClick={()=>go(to)}><Icon size={18}/><span>{label}</span><ChevronRight size={16}/></button>)}{results.length===0&&<div className="empty-compact">검색 결과가 없습니다.</div>}</div></div></div>
}
