import { FormEvent, useEffect, useMemo, useState } from 'react'
import { ExternalLink, FileCode2, MessageSquarePlus, Plus, Square } from 'lucide-react'
import { api } from '../api'
import { Drawer, Empty, ErrorBanner, Loading, PageHeader, StatusBadge } from '../components/UI'
import type { Agent, RuntimeSession } from '../types'

export function Sessions() {
  const [sessions,setSessions] = useState<RuntimeSession[]>()
  const [agents,setAgents] = useState<Agent[]>([])
  const [open,setOpen] = useState(false)
  const [error,setError] = useState('')
  const load = () => Promise.all([
    api.get<{items:RuntimeSession[]}>('/api/v1/sessions').then((v) => setSessions(v.items)),
    api.get<{items:Agent[]}>('/api/v1/agents').then((v) => setAgents(v.items))
  ]).catch((e) => setError(e.message))
  useEffect(() => { void load() }, [])
  const available = useMemo(() => agents.filter((item) => item.runtime&&['running','ready'].includes(item.runtime.status.toLowerCase())),[agents])
  if (!sessions) return <Loading/>
  const close = async (id:string) => { try { await api.post(`/api/v1/sessions/${id}/close`); void load() } catch (e) { setError(e instanceof Error?e.message:'Session을 종료하지 못했습니다.') } }
  const launch = async (runtimeId:string) => { try { const result=await api.post<{url:string}>(`/api/v1/runtimes/${runtimeId}/launch`); window.open(result.url,'_blank','noopener,noreferrer') } catch (e) { setError(e instanceof Error?e.message:'Session을 열지 못했습니다.') } }
  return <div className="page">
    <PageHeader eyebrow="AGENT WORKSPACE" title="Runtime Sessions" description="Agent별 작업 맥락과 실행 진입점을 분리해 관리합니다." actions={<button className="button primary" disabled={available.length===0} onClick={() => setOpen(true)}><Plus size={16}/>새 Session</button>}/>
    {error&&<ErrorBanner message={error}/>}
    {sessions.length===0?<Empty icon={<FileCode2/>} title="Session이 없습니다" description="실행 중인 Runtime을 선택해 첫 작업 Session을 시작하세요." action={available.length?<button className="button primary" onClick={()=>setOpen(true)}>Session 시작</button>:undefined}/>:<section className="session-list">{sessions.map((item) => <article key={item.id}><div className={`runtime-logo ${item.runtimeType}`}>{item.runtimeType==='opencode'?'OC':'H'}</div><div><div><h3>{item.title}</h3><StatusBadge status={item.status}/></div><p>{item.agentName} · {item.runtimeType}</p><span>{new Date(item.updatedAt).toLocaleString('ko-KR')} · Trace {Array.isArray(item.trace)?item.trace.length:0}</span></div><div className="session-actions">{item.status==='active'&&<button className="button primary" onClick={()=>void launch(item.runtimeId)}><ExternalLink size={15}/>열기</button>}{item.status==='active'&&<button className="button ghost" onClick={()=>void close(item.id)}><Square size={14}/>종료</button>}</div></article>)}</section>}
    {open&&<SessionDrawer agents={available} close={()=>setOpen(false)} done={()=>{setOpen(false);void load()}} error={setError}/>}
  </div>
}

function SessionDrawer({agents,close,done,error}:{agents:Agent[];close:()=>void;done:()=>void;error:(value:string)=>void}) {
  const [runtimeId,setRuntimeId] = useState(agents[0]?.runtime?.id??'')
  const [title,setTitle] = useState('New session')
  const [busy,setBusy] = useState(false)
  const submit = async (event:FormEvent) => { event.preventDefault(); setBusy(true); try { await api.post(`/api/v1/runtimes/${runtimeId}/sessions`,{title}); done() } catch (e) { error(e instanceof Error?e.message:'Session을 생성하지 못했습니다.') } finally { setBusy(false) } }
  return <Drawer title="새 Runtime Session" subtitle="실행 중인 사용자 전용 Agent Runtime에 연결합니다." close={close} footer={<><button className="button ghost" onClick={close}>취소</button><button className="button primary" form="session-form" disabled={busy}>{busy?'시작 중…':'Session 시작'}</button></>}><form id="session-form" className="drawer-form" onSubmit={submit}><label><span>Agent Runtime</span><select required value={runtimeId} onChange={(e)=>setRuntimeId(e.target.value)}>{agents.map((item)=><option key={item.runtime!.id} value={item.runtime!.id}>{item.name} · {item.runtimeType}</option>)}</select></label><label><span>Session 제목</span><input required maxLength={120} value={title} onChange={(e)=>setTitle(e.target.value)} placeholder="API 오류 분석"/></label><div className="info-box"><MessageSquarePlus size={17}/><div><strong>격리된 작업 맥락</strong><p>Session 종료는 Runtime이나 영속 Workspace를 삭제하지 않습니다.</p></div></div></form></Drawer>
}
