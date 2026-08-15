import { useCallback, useEffect, useState } from 'react'
import { Activity, Bot, CircleStop, ExternalLink, FileText, MoreHorizontal, Play, Plus, RefreshCw } from 'lucide-react'
import { Link } from 'react-router-dom'
import { api } from '../api'
import { Drawer, Empty, ErrorBanner, Loading, PageHeader, StatusBadge } from '../components/UI'
import type { Agent } from '../types'

export function Agents({runtimeOnly=false}:{runtimeOnly?:boolean}) {
  const [agents,setAgents]=useState<Agent[]>()
  const [selectedId,setSelectedId]=useState<string|null>(null)
  const [error,setError]=useState('')
  const [busy,setBusy]=useState('')
  const load=useCallback(()=>api.get<{items:Agent[]}>('/api/v1/agents').then(v=>setAgents(v.items)).catch(e=>setError(e.message)),[])
  useEffect(()=>{
    void load()
    const timer = setInterval(() => { void load() }, 3000)
    return () => clearInterval(timer)
  },[load])
  const act=async(agent:Agent,action:'spawn'|'start'|'stop'|'restart')=>{
    setBusy(`${agent.id}:${action}`)
    setError('')
    try {
      if(action==='spawn') await api.post(`/api/v1/agents/${agent.id}/spawn`)
      else await api.post(`/api/v1/runtimes/${agent.runtime?.id}/${action}`)
      await load()
    } catch(e) {
      setError(e instanceof Error?e.message:'요청에 실패했습니다.')
    } finally {
      setBusy('')
    }
  }
  if(!agents) return <Loading/>
  const visible=runtimeOnly?agents.filter(a=>a.runtime):agents
  const selected = agents.find(a => a.id === selectedId) || null
  return <div className="page">
    <PageHeader eyebrow={runtimeOnly?'RUNTIME CONTROL':'MY WORKSPACE'} title={runtimeOnly?'My Runtimes':'My Agents'} description={runtimeOnly?'사용자 전용 Kubernetes Runtime의 수명주기와 상태를 관리합니다.':'Agent 정의와 실행 중인 Runtime을 분리해서 안전하게 관리합니다.'} actions={<Link className="button primary" to="/catalog"><Plus size={17}/>새 Agent</Link>}/>
    {error&&<ErrorBanner message={error} onClose={()=>setError('')}/>}
    {visible.length===0?<Empty icon={<Bot/>} title="아직 Agent가 없습니다" description="Catalog에서 검증된 Template을 선택해 첫 Agent를 만들어 보세요." action={<Link className="button primary" to="/catalog">Catalog 열기</Link>}/>:<section className="table-panel"><div className="table-wrap custom-scroll"><table><thead><tr><th>Agent</th><th>Runtime</th><th>Status</th><th>Pod / Node</th><th>마지막 변경</th><th aria-label="작업"/></tr></thead><tbody>{visible.map(agent=><tr key={agent.id}><td><button className="agent-cell" onClick={()=>setSelectedId(agent.id)}><div className={`runtime-logo ${agent.runtimeType}`}>{agent.runtimeType==='opencode'?'OC':'H'}</div><div><strong>{agent.name}</strong><span>Definition v{agent.version}</span></div></button></td><td><span className="runtime-name">{agent.runtimeType}</span></td><td><StatusBadge status={agent.runtime?.status??'stopped'}/></td><td><div className="mono-stack"><code>{agent.runtime?.podName||'—'}</code><small>{agent.runtime?.nodeName||'할당 전'}</small></div></td><td>{new Date(agent.updatedAt).toLocaleString('ko-KR',{dateStyle:'short',timeStyle:'short'})}</td><td><div className="row-actions">{!agent.runtime||['stopped','failed','crashed'].includes(agent.runtime.status)?<button title="시작" disabled={!!busy} onClick={()=>void act(agent,agent.runtime?'start':'spawn')}><Play size={16}/></button>:<button title="중지" disabled={!!busy} onClick={()=>void act(agent,'stop')}><CircleStop size={16}/></button>}<button title="상세" onClick={()=>setSelectedId(agent.id)}><MoreHorizontal size={18}/></button></div></td></tr>)}</tbody></table></div></section>}
    {selected&&<AgentDrawer agent={selected} close={()=>setSelectedId(null)} action={act} busy={!!busy}/>}
  </div>
}

function AgentDrawer({agent,close,action,busy}:{agent:Agent;close:()=>void;action:(a:Agent,v:'spawn'|'start'|'stop'|'restart')=>Promise<void>;busy:boolean}) {
  const runtime=agent.runtime
  const ready=Boolean(runtime&&['running','ready'].includes(runtime.status))
  const [logs,setLogs]=useState('')
  const [logError,setLogError]=useState('')
  const loadLogs=async()=>{
    if(!runtime) return
    setLogError('')
    try {
      const result=await api.get<{content?:string;message?:string}>(`/api/v1/runtimes/${runtime.id}/logs`)
      setLogs(result.content||result.message||'표시할 로그가 없습니다.')
    } catch(error) {
      setLogError(error instanceof Error?error.message:'로그를 불러오지 못했습니다.')
    }
  }
  const launch=async()=>{
    if(!runtime) return
    setLogError('')
    try {
      const result=await api.post<{url:string}>(`/api/v1/runtimes/${runtime.id}/launch`)
      window.open(result.url,'_blank','noopener,noreferrer')
    } catch(error) {
      setLogError(error instanceof Error?error.message:'Runtime 세션을 열지 못했습니다.')
    }
  }
  return <Drawer title={agent.name} subtitle={`Agent Definition v${agent.version}`} close={close} footer={<>{runtime&&<button className="button ghost" disabled={busy} onClick={()=>void action(agent,'restart')}><RefreshCw size={16}/>재시작</button>}{(!runtime||['stopped','failed'].includes(runtime.status))?<button className="button primary" disabled={busy} onClick={()=>void action(agent,runtime?'start':'spawn')}><Play size={16}/>Runtime 시작</button>:<button className="button danger" disabled={busy} onClick={()=>void action(agent,'stop')}><CircleStop size={16}/>중지</button>}</>}>
    <div className="detail-hero"><div className={`runtime-logo xlarge ${agent.runtimeType}`}>{agent.runtimeType==='opencode'?'OC':'H'}</div><div><StatusBadge status={runtime?.status??'stopped'}/><h3>{agent.runtimeType}</h3><p>{agent.description||'설명 없음'}</p></div></div>
    <section className="detail-section"><h4>Runtime</h4><dl className="detail-list"><div><dt>Instance ID</dt><dd><code>{runtime?.id??'아직 생성되지 않음'}</code></dd></div><div><dt>Desired state</dt><dd>{runtime?.desiredState??'stopped'}</dd></div><div><dt>CRD</dt><dd><code>{runtime?.crdName||'—'}</code></dd></div><div><dt>Pod</dt><dd><code>{runtime?.podName||'—'}</code></dd></div><div><dt>Node</dt><dd>{runtime?.nodeName||'할당 전'}</dd></div><div><dt>Restarts</dt><dd>{runtime?.restartCount??0}</dd></div></dl></section>
    <section className="detail-section"><h4>Workspace tools</h4><div className="tool-links">{runtime&&<button disabled={!ready||busy} onClick={()=>void launch()}><Activity size={17}/>Workspace 열기<ExternalLink size={14}/></button>}<button disabled={!runtime} onClick={()=>void loadLogs()}><FileText size={17}/>Logs</button></div></section>
    {logError&&<ErrorBanner message={logError}/>} {logs&&<section className="detail-section"><h4>Runtime logs</h4><pre className="runtime-log-preview custom-scroll">{logs}</pre></section>}
    {runtime?.failureReason&&<ErrorBanner message={runtime.failureReason}/>}
  </Drawer>
}
