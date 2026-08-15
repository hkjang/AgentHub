import { FormEvent, useEffect, useMemo, useState } from 'react'
import { ExternalLink, FileCode2, MessageSquarePlus, Plus, Square } from 'lucide-react'
import { api } from '../api'
import { Drawer, Empty, ErrorBanner, Loading, PageHeader, StatusBadge } from '../components/UI'
import { runtimeCode, runtimeLabel, runtimeLogoClass } from '../runtime'
import type { Agent, RuntimeSession } from '../types'

export function Sessions() {
  const [sessions,setSessions] = useState<RuntimeSession[]>()
  const [agents,setAgents] = useState<Agent[]>([])
  const [open,setOpen] = useState(false)
  const [error,setError] = useState('')
  const load = () => Promise.all([
    api.get<{items?:RuntimeSession[]}>('/api/v1/sessions').then((v) => setSessions(v.items??[])),
    api.get<{items?:Agent[]}>('/api/v1/agents').then((v) => setAgents(v.items??[]))
  ]).catch((e) => { setSessions([]); setError(e instanceof Error?e.message:'세션 목록을 불러오지 못했습니다.') })
  useEffect(() => { void load() }, [])
  const available = useMemo(() => agents.filter((item) => item.runtime&&['running','ready'].includes(item.runtime.status.toLowerCase())),[agents])
  if (!sessions) return <Loading/>
  const close = async (id:string) => { try { await api.post(`/api/v1/sessions/${id}/close`); void load() } catch (e) { setError(e instanceof Error?e.message:'세션을 종료하지 못했습니다.') } }
  const launch = async (runtimeId:string) => { try { const result=await api.post<{url:string}>(`/api/v1/runtimes/${runtimeId}/launch`); window.open(result.url,'_blank','noopener,noreferrer') } catch (e) { setError(e instanceof Error?e.message:'세션을 열지 못했습니다.') } }
  return <div className="page">
    <PageHeader eyebrow="에이전트 작업공간" title="런타임 세션" description="에이전트별 작업 맥락과 실행 진입점을 분리해 관리합니다." actions={<button className="button primary" disabled={available.length===0} title={available.length===0?'실행 중이거나 준비된 런타임이 있어야 세션을 시작할 수 있습니다.':undefined} onClick={() => setOpen(true)}><Plus size={16}/>새 세션</button>}/>
    {error&&<ErrorBanner message={error} onClose={()=>setError('')}/>}
    {sessions.length===0?<Empty icon={<FileCode2/>} title="세션이 없습니다" description={available.length?'실행 중인 런타임을 선택해 첫 작업 세션을 시작하세요.':'먼저 내 에이전트에서 런타임을 시작한 뒤 세션을 만들 수 있습니다.'} action={available.length?<button className="button primary" onClick={()=>setOpen(true)}>세션 시작</button>:undefined}/>:<section className="session-list">{sessions.map((item) => <article key={item.id}><div className={runtimeLogoClass(item.runtimeType)}>{runtimeCode(item.runtimeType)}</div><div><div><h3>{item.title}</h3><StatusBadge status={item.status}/></div><p>{item.agentName} · {runtimeLabel(item.runtimeType)}</p><span>{new Date(item.updatedAt).toLocaleString('ko-KR')} · 추적 {Array.isArray(item.trace)?item.trace.length:0}건</span></div><div className="session-actions">{item.status==='active'&&<button className="button primary" onClick={()=>void launch(item.runtimeId)}><ExternalLink size={15}/>열기</button>}{item.status==='active'&&<button className="button ghost" onClick={()=>void close(item.id)}><Square size={14}/>종료</button>}</div></article>)}</section>}
    {open&&<SessionDrawer agents={available} close={()=>setOpen(false)} done={()=>{setOpen(false);void load()}} error={setError}/>}
  </div>
}

function SessionDrawer({agents,close,done,error}:{agents:Agent[];close:()=>void;done:()=>void;error:(value:string)=>void}) {
  const [runtimeId,setRuntimeId] = useState(agents[0]?.runtime?.id??'')
  const [title,setTitle] = useState('New session')
  const [busy,setBusy] = useState(false)
  const submit = async (event:FormEvent) => { event.preventDefault(); setBusy(true); try { await api.post(`/api/v1/runtimes/${runtimeId}/sessions`,{title}); done() } catch (e) { error(e instanceof Error?e.message:'세션을 생성하지 못했습니다.') } finally { setBusy(false) } }
  return <Drawer title="새 런타임 세션" subtitle="실행 중인 사용자 전용 에이전트 런타임에 연결합니다." close={close} footer={<><button className="button ghost" onClick={close}>취소</button><button className="button primary" form="session-form" disabled={busy}>{busy?'시작 중…':'세션 시작'}</button></>}><form id="session-form" className="drawer-form" onSubmit={submit}><label><span>에이전트 런타임</span><select required value={runtimeId} onChange={(e)=>setRuntimeId(e.target.value)}>{agents.map((item)=><option key={item.runtime!.id} value={item.runtime!.id}>{item.name} · {runtimeLabel(item.runtimeType)}</option>)}</select></label><label><span>세션 제목</span><input required maxLength={120} value={title} onChange={(e)=>setTitle(e.target.value)} placeholder="API 오류 분석"/></label><div className="info-box"><MessageSquarePlus size={17}/><div><strong>격리된 작업 맥락</strong><p>세션 종료는 런타임이나 영속 작업공간을 삭제하지 않습니다.</p></div></div></form></Drawer>
}
