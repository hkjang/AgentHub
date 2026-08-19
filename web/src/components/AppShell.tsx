import { useEffect, useMemo, useState, type KeyboardEvent as ReactKeyboardEvent } from 'react'
import { NavLink, Outlet, useLocation, useNavigate } from 'react-router-dom'
import {
  Activity, Bell, Boxes, Bot, Braces, ChevronDown, ChevronRight, CircleUserRound, Command,
  Database, FileClock, FileCode2, Gauge, KeyRound, LayoutDashboard, Library, LogOut, Menu, Network,
  ListChecks, Search, Settings, ShieldAlert, ShieldCheck, Sparkles, UsersRound, Workflow, X
} from 'lucide-react'
import { useAuth } from '../App'
import { api } from '../api'
import type { Notification } from '../types'

// Menu labels are Korean; `keywords` keeps the original English terms (and the
// route) searchable so the command palette still answers "agents" or "mcp".
type NavItem = { to: string; label: string; icon: typeof Gauge; keywords?: string }
type NavGroup = { label: string; items: NavItem[]; admin?: boolean; review?: boolean }
const groups: NavGroup[] = [
  { label: '개요', items: [{ to: '/', label: '홈', icon: LayoutDashboard, keywords: 'home overview dashboard 대시보드' }] },
  { label: '에이전트', items: [
    { to: '/catalog', label: '에이전트 카탈로그', icon: Library, keywords: 'agent catalog template 템플릿' },
    { to: '/agents', label: '내 에이전트', icon: Bot, keywords: 'my agents 목록' },
    { to: '/agents/builder', label: '에이전트 빌더', icon: Sparkles, keywords: 'agent builder create 생성' },
    { to: '/workflows', label: '워크플로', icon: Workflow, keywords: 'workflow dag 그래프' }
  ]},
  { label: '작업공간 · 런타임', items: [
    { to: '/workspaces', label: '작업공간', icon: Database, keywords: 'workspace storage pvc 저장소' },
    { to: '/workspaces/snapshots', label: '스냅샷', icon: Boxes, keywords: 'snapshot backup restore 복원 백업' },
    { to: '/runtime', label: '런타임', icon: Activity, keywords: 'runtime pod 실행' },
    { to: '/sessions', label: '세션', icon: FileCode2, keywords: 'session 작업' },
    { to: '/tasks', label: '작업 대기열', icon: ListChecks, keywords: 'task queue run 자동 실행 큐' }
  ]},
  { label: '연동', items: [
    { to: '/mcp/catalog', label: 'MCP 카탈로그', icon: Network, keywords: 'mcp catalog server 서버' },
    { to: '/mcp/bundles', label: 'MCP 번들', icon: Boxes, keywords: 'mcp bundle 묶음' },
    { to: '/developer', label: '시크릿 · API 키', icon: Braces, keywords: 'secret api key developer 개인키 토큰' },
    { to: '/evaluation', label: '사전검증', icon: ShieldCheck, keywords: 'evaluation test quality 품질 테스트' }
  ]},
  { label: '거버넌스', review: true, items: [{ to: '/reviews', label: '검토 · 승인', icon: ShieldCheck, keywords: 'review approval 승인 요청' }] },
  { label: '관리자', admin: true, items: [
    { to: '/admin/overview', label: '운영 현황', icon: Gauge, keywords: 'overview dashboard usage statistics 통계 사용량 현황 대시보드 비용' },
    { to: '/admin/policy', label: '정책', icon: ShieldCheck, keywords: 'policy rule guardrail 정책 규칙 차단 허용 승인 거버넌스' },
    { to: '/admin/dlp', label: '내용 검사', icon: ShieldAlert, keywords: 'dlp 개인정보 민감정보 마스킹 유출 방지 주민등록번호 카드 검사' },
    { to: '/admin/execution', label: '실행 제어', icon: Gauge, keywords: 'execution control pause worker retention cleanup 워커 중지 재개 회수 보관 정리' },
    { to: '/admin/operations', label: '로그 · 감사', icon: FileClock, keywords: 'control center operations log audit approval 로그 감사 승인 운영' },
    { to: '/admin/runtime-profiles', label: '런타임 프로파일', icon: Bot, keywords: 'runtime profile cpu memory 자원' },
    { to: '/admin/runtime-images', label: '런타임 이미지', icon: Boxes, keywords: 'runtime image registry 이미지' },
    { to: '/admin/models', label: '모델 엔드포인트', icon: Sparkles, keywords: 'model endpoint llm vllm ollama 모델' },
    { to: '/admin/mcp', label: 'MCP 서버', icon: Network, keywords: 'mcp server 서버' },
    { to: '/admin/mcp-bundles', label: 'MCP 번들 관리', icon: Boxes, keywords: 'mcp bundle 번들' },
    { to: '/admin/users', label: '사용자 · 팀', icon: UsersRound, keywords: 'user team role 권한 사용자' },
    { to: '/admin/security', label: '보안 · 네트워크', icon: ShieldCheck, keywords: 'security network policy 정책 보안' },
    { to: '/admin/settings', label: '시스템 설정', icon: Settings, keywords: 'system settings config 설정' }
  ]}
]

// matchesRoute is segment-aware: a plain `startsWith` makes /agents match
// /agents/builder — and /admin/mcp match /admin/mcp-bundles — which left the
// previous menu highlighted after moving to the next one.
function matchesRoute(pathname: string, to: string) {
  if (to === '/') return pathname === '/'
  return pathname === to || pathname.startsWith(to + '/')
}

export function AppShell() {
  const { user, version, capabilities, logout } = useAuth()
  const [sidebar, setSidebar] = useState(false), [command, setCommand] = useState(false), [profile, setProfile] = useState(false)
  const [notificationOpen,setNotificationOpen]=useState(false),[notifications,setNotifications]=useState<Notification[]>([])
  const [collapsed, setCollapsed] = useState<Record<string, boolean>>({})
  const location = useLocation(), navigate = useNavigate()
  const visibleGroups = useMemo(() => groups.filter((g) => (!g.admin || user.role === 'admin') && (!g.review || (capabilities.teamApprovalEnabled && (user.role === 'manager' || user.role === 'admin')))), [user.role, capabilities.teamApprovalEnabled])
  // The longest match wins, so /agents/builder highlights the builder rather than
  // the first menu that happens to be a prefix of it. Exactly one item is active,
  // and the breadcrumb names that same item.
  const active = useMemo(() => visibleGroups.flatMap((g) => g.items)
    .filter((item) => matchesRoute(location.pathname, item.to))
    .sort((a, b) => b.to.length - a.to.length)[0], [visibleGroups, location.pathname])

  useEffect(() => {
    const listener = (event: KeyboardEvent) => {
      if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === 'k') { event.preventDefault(); setCommand((v) => !v) }
      if (event.key === 'Escape') { setCommand(false); setProfile(false); setNotificationOpen(false); setSidebar(false) }
    }
    window.addEventListener('keydown', listener); return () => window.removeEventListener('keydown', listener)
  }, [])

  // Escape closed the menus but a click anywhere else did not, so a notification
  // list stayed open over the page the user had moved on to.
  useEffect(() => {
    if (!profile && !notificationOpen) return
    const listener = (event: MouseEvent) => {
      const target = event.target as HTMLElement | null
      if (target?.closest('.profile-wrap')) return
      if (target?.closest('.notification-wrap')) return
      setProfile(false); setNotificationOpen(false)
    }
    document.addEventListener('mousedown', listener); return () => document.removeEventListener('mousedown', listener)
  }, [profile, notificationOpen])

  // Any navigation — a command palette jump, a notification, a browser Back —
  // closes what was open on the previous screen.
  useEffect(() => { setProfile(false); setNotificationOpen(false); setSidebar(false) }, [location.pathname])

  // Poll notifications only while the tab is in front, like every other polling
  // surface, and catch up immediately when the user comes back to it.
  useEffect(()=>{const load=()=>api.get<{items:Notification[]}>('/api/v1/notifications').then((value)=>setNotifications(value.items)).catch(()=>undefined);void load()
    const timer=window.setInterval(()=>{if(document.visibilityState==='visible')void load()},30000)
    const onVisible=()=>{if(document.visibilityState==='visible')void load()}
    document.addEventListener('visibilitychange',onVisible)
    return()=>{window.clearInterval(timer);document.removeEventListener('visibilitychange',onVisible)}},[])
  const openNotification=async(item:Notification)=>{if(!item.readAt){await api.post(`/api/v1/notifications/${item.id}/read`).catch(()=>undefined);setNotifications((values)=>values.map((value)=>value.id===item.id?{...value,readAt:new Date().toISOString()}:value))}setNotificationOpen(false);if(item.resourceUrl)navigate(item.resourceUrl)}

  return <div className="app-frame">
    <aside className={`sidebar ${sidebar ? 'sidebar-open' : ''}`}>
      <div className="sidebar-brand"><img src="/logo.svg" alt="AgentHub Logo" className="brand-logo-img" /><div><strong>AgentHub</strong><span>런타임 플랫폼</span></div><button className="icon-button mobile-only" onClick={() => setSidebar(false)} aria-label="메뉴 닫기"><X size={18}/></button></div>
      <button className="quick-button" onClick={() => setCommand(true)}><Search size={16}/><span>빠른 이동</span><kbd>⌘ K</kbd></button>
      <nav className="nav-scroll" aria-label="주 메뉴">
        {visibleGroups.map((group) => <section className="nav-group" key={group.label}>
          <button className="nav-group-title" onClick={() => setCollapsed((v) => ({...v, [group.label]: !v[group.label]}))} aria-expanded={!collapsed[group.label]}>
            <span>{group.label}</span>{collapsed[group.label] ? <ChevronRight size={14}/> : <ChevronDown size={14}/>}
          </button>
          {!collapsed[group.label] && group.items.map(({to,label,icon:Icon}) => <NavLink end to={to} key={to} onClick={() => setSidebar(false)} className={active?.to === to ? 'nav-link active' : 'nav-link'}><Icon size={18}/><span>{label}</span></NavLink>)}
        </section>)}
      </nav>
      <div className="sidebar-status"><span className="status-dot online"/><div><strong>컨트롤 플레인</strong><span>정상 · v{version.version}</span></div></div>
    </aside>
    {sidebar && <button className="sidebar-scrim" aria-label="메뉴 닫기" onClick={() => setSidebar(false)}/>}
    <div className="main-column">
      <header className="topbar">
        <button className="icon-button mobile-only" onClick={() => setSidebar(true)} aria-label="메뉴 열기"><Menu size={20}/></button>
        <div className="breadcrumb"><span>AgentHub</span><ChevronRight size={14}/><strong>{active?.label ?? '작업공간'}</strong></div>
        <div className="top-actions">
          <button className="command-chip" onClick={() => setCommand(true)}><Command size={15}/><span>빠른 이동</span></button>
          <div className="notification-wrap"><button className="icon-button notification-button" onClick={()=>{setProfile(false);setNotificationOpen((value)=>!value)}} aria-label="알림" aria-expanded={notificationOpen}><Bell size={18}/>{notifications.some((item)=>!item.readAt)&&<i/>}</button>{notificationOpen&&<div className="notification-menu custom-scroll"><header><strong>알림</strong><span>읽지 않음 {notifications.filter((item)=>!item.readAt).length}건</span></header>{notifications.length===0?<div className="empty-compact">새 알림이 없습니다.</div>:notifications.map((item)=><button key={item.id} className={item.readAt?'read':''} onClick={()=>void openNotification(item)}><i/><span><strong>{item.title}</strong><small>{item.message}</small><time>{new Date(item.createdAt).toLocaleString('ko-KR')}</time></span></button>)}</div>}</div>
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
  const [cursor,setCursor] = useState(0)
  const results = useMemo(() => {
    const needle = query.trim().toLowerCase()
    if (!needle) return items
    // Match the Korean label, the English keywords and the route, so the menu
    // stays reachable by whichever name the user has in mind.
    return items.filter((item) => `${item.label} ${item.keywords ?? ''} ${item.to}`.toLowerCase().includes(needle))
  }, [items, query])
  useEffect(() => { setCursor(0) }, [query])
  const move = (delta: number) => setCursor((current) => {
    if (results.length === 0) return 0
    return (current + delta + results.length) % results.length
  })
  const onKeyDown = (event: ReactKeyboardEvent<HTMLDivElement>) => {
    if (event.key === 'ArrowDown') { event.preventDefault(); move(1) }
    else if (event.key === 'ArrowUp') { event.preventDefault(); move(-1) }
    else if (event.key === 'Enter') { event.preventDefault(); const hit = results[cursor]; if (hit) go(hit.to) }
  }
  return <div className="modal-layer" role="dialog" aria-modal="true" aria-label="빠른 이동">
    <button className="modal-scrim" onClick={close} aria-label="닫기"/>
    <div className="command-panel" onKeyDown={onKeyDown}>
      <div className="command-input">
        <Search size={20}/>
        <input autoFocus value={query} onChange={(e)=>setQuery(e.target.value)} placeholder="메뉴 검색 (한글/영문)…"
          role="combobox" aria-expanded aria-controls="command-results" aria-activedescendant={results[cursor]?`command-option-${cursor}`:undefined}/>
        <kbd>ESC</kbd>
      </div>
      <div className="command-results custom-scroll" id="command-results" role="listbox">
        {results.map(({to,label,icon:Icon},index)=>
          <button key={to} id={`command-option-${index}`} role="option" aria-selected={index===cursor}
            className={index===cursor?'active':''} onMouseEnter={()=>setCursor(index)} onClick={()=>go(to)}>
            <Icon size={18}/><span>{label}</span><ChevronRight size={16}/>
          </button>)}
        {results.length===0&&<div className="empty-compact">검색 결과가 없습니다.</div>}
      </div>
      <footer className="command-hint"><kbd>↑</kbd><kbd>↓</kbd> 이동 · <kbd>Enter</kbd> 열기 · <kbd>ESC</kbd> 닫기</footer>
    </div>
  </div>
}
