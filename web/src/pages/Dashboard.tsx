import { useCallback, useEffect, useState } from 'react'
import { Activity, ArrowUpRight, Bot, Clock3, Database, Plus, RefreshCw, ShieldCheck } from 'lucide-react'
import { Link } from 'react-router-dom'
import { api } from '../api'
import { useAuth } from '../App'
import { ErrorBanner, Loading, PageHeader, StatusBadge } from '../components/UI'
import { useTerms } from '../viewmode'
import { relativeTime, runtimeCode, runtimeLabel, runtimeLogoClass } from '../runtime'
import type { Agent } from '../types'

type Metrics={agents:number;running:number;idle:number;failed:number;workspaces:number;pendingApprovals:number}

export function Dashboard(){
  const t=useTerms()
  const {user,capabilities}=useAuth();const [metrics,setMetrics]=useState<Metrics>(),[agents,setAgents]=useState<Agent[]>([]),[error,setError]=useState('')
  // A failed load used to leave the home screen on its loading spinner for good,
  // with nothing but an unhandled rejection in the console to say why.
  const load=useCallback(async()=>{setError('');try{const [dashboard,list]=await Promise.all([api.get<Metrics>('/api/v1/dashboard'),api.get<{items?:Agent[]}>('/api/v1/agents')]);setMetrics(dashboard);setAgents((list.items??[]).slice(0,5))}catch(e){setError(e instanceof Error?e.message:'현황을 불러오지 못했습니다.')}},[])
  useEffect(()=>{void load()},[load])
  if(error&&!metrics)return <div className="page"><PageHeader eyebrow="컨트롤 플레인" title={`${user.displayName}님, 안녕하세요`} description={t('homeHint')} actions={<button className="button ghost" onClick={()=>void load()}><RefreshCw size={17}/>다시 시도</button>}/><ErrorBanner message={error}/></div>
  if(!metrics)return <Loading/>
  return <div className="page"><PageHeader eyebrow="컨트롤 플레인" title={`${user.displayName}님, 안녕하세요`} description={t('homeHint')} actions={<Link className="button primary" to="/catalog"><Plus size={17}/>새 {t('agentSingular')}</Link>}/>{error&&<ErrorBanner message={error}/>}
    <section className="metric-grid"><Metric icon={<Bot/>} label={t('metricAgents')} value={metrics.agents} note={t('metricAgentsNote')} tone="violet"/><Metric icon={<Activity/>} label={t('metricRunning')} value={metrics.running} note={metrics.failed>0?`유휴 ${metrics.idle}개 · 실패 ${metrics.failed}개`:`유휴 ${metrics.idle}개`} alert={metrics.failed>0} tone="green"/><Metric icon={<Database/>} label={t('metricWorkspaces')} value={metrics.workspaces} note="영속 저장소" tone="blue"/>{capabilities.teamApprovalEnabled?<Metric icon={<ShieldCheck/>} label="승인 대기" value={metrics.pendingApprovals} note="검토 대기" tone="amber"/>:<Metric icon={<ShieldCheck/>} label="실행 정책" value="즉시 실행" note="승인 흐름 미사용" tone="amber"/>}</section>
    <div className="dashboard-grid"><section className="panel"><div className="panel-header"><div><h2>{t('recentAgents')}</h2><p>{t('recentAgentsHint')}</p></div><Link to="/agents">모두 보기<ArrowUpRight size={15}/></Link></div><div className="agent-list">{agents.length===0?<div className="empty-compact">{t('emptyAgents')}</div>:agents.map((agent)=><Link to="/agents" className="agent-row" key={agent.id}><div className={runtimeLogoClass(agent.runtimeType)}>{runtimeCode(agent.runtimeType)}</div><div className="agent-main"><strong>{agent.name}</strong><span>{runtimeLabel(agent.runtimeType)} · v{agent.version}</span></div><StatusBadge status={agent.runtime?.status??'stopped'}/><span className="row-time" title={new Date(agent.updatedAt).toLocaleString('ko-KR')}><Clock3 size={14}/>{relativeTime(agent.updatedAt)}</span></Link>)}</div></section>
      <section className="panel getting-started"><div className="panel-header"><div><h2>{t('quickStart')}</h2><p>{t('quickStartHint')}</p></div></div><ol><li><span>1</span><div><strong>{t('quickStep1')}</strong><p>검증된 실행환경과 정책을 선택합니다.</p></div></li><li><span>2</span><div><strong>{t('quickStep2')}</strong><p>새 공간 또는 Git 저장소를 연결합니다.</p></div></li><li><span>3</span><div><strong>{t('quickStep3')}</strong><p>개인 전용 Pod에서 바로 시작합니다.</p></div></li></ol><Link to="/catalog" className="text-link">{t('openCatalog')}<ArrowUpRight size={15}/></Link></section></div>
  </div>
}
// A number gets the big display size; a phrase like "즉시 실행" does not, or it
// wraps across the note beside it.
function Metric({icon,label,value,note,tone,alert}:{icon:React.ReactNode;label:string;value:React.ReactNode;note:string;tone:string;alert?:boolean}){return <article className={typeof value==='number'?'metric-card':'metric-card text'}><div className={`metric-icon ${tone}`}>{icon}</div><div><span>{label}</span><strong>{value}</strong><small className={alert?'alert-note':undefined}>{note}</small></div></article>}
