import { useEffect, useState } from 'react'
import { Activity, ArrowUpRight, Bot, Clock3, Database, Plus, ShieldCheck } from 'lucide-react'
import { Link } from 'react-router-dom'
import { api } from '../api'
import { useAuth } from '../App'
import { Loading, PageHeader, StatusBadge } from '../components/UI'
import type { Agent } from '../types'

type Metrics={agents:number;running:number;idle:number;failed:number;workspaces:number;pendingApprovals:number}
export function Dashboard(){
  const {user,capabilities}=useAuth();const [metrics,setMetrics]=useState<Metrics>(),[agents,setAgents]=useState<Agent[]>([])
  useEffect(()=>{void Promise.all([api.get<Metrics>('/api/v1/dashboard').then(setMetrics),api.get<{items:Agent[]}>('/api/v1/agents').then((v)=>setAgents(v.items.slice(0,5)))])},[])
  if(!metrics)return <Loading/>
  return <div className="page"><PageHeader eyebrow="CONTROL PLANE" title={`${user.displayName}님, 안녕하세요`} description="Agent Runtime과 Workspace 상태를 한눈에 확인하세요." actions={<Link className="button primary" to="/catalog"><Plus size={17}/>새 Agent</Link>}/>
    <section className="metric-grid"><Metric icon={<Bot/>} label="My Agents" value={metrics.agents} note="정의된 Agent" tone="violet"/><Metric icon={<Activity/>} label="Running" value={metrics.running} note={`${metrics.idle} idle`} tone="green"/><Metric icon={<Database/>} label="Workspaces" value={metrics.workspaces} note="영속 저장소" tone="blue"/>{capabilities.teamApprovalEnabled?<Metric icon={<ShieldCheck/>} label="Approvals" value={metrics.pendingApprovals} note="검토 대기" tone="amber"/>:<Metric icon={<ShieldCheck/>} label="Launch Policy" value="Direct" note="승인 흐름 미사용" tone="amber"/>}</section>
    <div className="dashboard-grid"><section className="panel"><div className="panel-header"><div><h2>최근 Agent</h2><p>현재 Runtime 상태와 마지막 활동</p></div><Link to="/agents">모두 보기<ArrowUpRight size={15}/></Link></div><div className="agent-list">{agents.length===0?<div className="empty-compact">아직 생성한 Agent가 없습니다. Catalog에서 시작해 보세요.</div>:agents.map((agent)=><Link to="/agents" className="agent-row" key={agent.id}><div className={`runtime-logo ${agent.runtimeType}`}>{agent.runtimeType==='opencode'?'OC':'H'}</div><div className="agent-main"><strong>{agent.name}</strong><span>{agent.runtimeType} · v{agent.version}</span></div><StatusBadge status={agent.runtime?.status??'stopped'}/><span className="row-time"><Clock3 size={14}/>{new Date(agent.updatedAt).toLocaleDateString('ko-KR')}</span></Link>)}</div></section>
      <section className="panel getting-started"><div className="panel-header"><div><h2>Quick start</h2><p>첫 Runtime을 준비하는 3단계</p></div></div><ol><li><span>1</span><div><strong>Template 선택</strong><p>검증된 실행환경과 정책을 선택합니다.</p></div></li><li><span>2</span><div><strong>Workspace 연결</strong><p>새 공간 또는 Git Repository를 연결합니다.</p></div></li><li><span>3</span><div><strong>Runtime Spawn</strong><p>개인 전용 Pod에서 바로 시작합니다.</p></div></li></ol><Link to="/catalog" className="text-link">Agent Catalog 열기<ArrowUpRight size={15}/></Link></section></div>
  </div>
}
function Metric({icon,label,value,note,tone}:{icon:React.ReactNode;label:string;value:React.ReactNode;note:string;tone:string}){return <article className="metric-card"><div className={`metric-icon ${tone}`}>{icon}</div><div><span>{label}</span><strong>{value}</strong><small>{note}</small></div></article>}
