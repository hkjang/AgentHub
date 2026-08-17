import { FormEvent, useEffect, useMemo, useState } from 'react'
import { ExternalLink, FileCode2, ListChecks, MessageSquarePlus, Plus, Square } from 'lucide-react'
import { api } from '../api'
import { Drawer, Empty, ErrorBanner, Loading, PageHeader, StatusBadge } from '../components/UI'
import { runtimeCode, runtimeLabel, runtimeLogoClass } from '../runtime'
import type { Agent, RuntimeSession } from '../types'

export function Sessions() {
  const [sessions,setSessions] = useState<RuntimeSession[]>()
  const [agents,setAgents] = useState<Agent[]>([])
  const [open,setOpen] = useState(false)
  const [handOff,setHandOff] = useState<RuntimeSession|null>(null)
  const [notice,setNotice] = useState('')
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
    {notice&&<div className="notice-banner">{notice}</div>}
    {sessions.length===0?<Empty icon={<FileCode2/>} title="세션이 없습니다" description={available.length?'실행 중인 런타임을 선택해 첫 작업 세션을 시작하세요.':'먼저 내 에이전트에서 런타임을 시작한 뒤 세션을 만들 수 있습니다.'} action={available.length?<button className="button primary" onClick={()=>setOpen(true)}>세션 시작</button>:undefined}/>:<section className="session-list">{sessions.map((item) => <article key={item.id}><div className={runtimeLogoClass(item.runtimeType)}>{runtimeCode(item.runtimeType)}</div><div><div><h3>{item.title}</h3><StatusBadge status={item.status}/></div><p>{item.agentName} · {runtimeLabel(item.runtimeType)}</p><span>{new Date(item.updatedAt).toLocaleString('ko-KR')} · 추적 {Array.isArray(item.trace)?item.trace.length:0}건</span></div><div className="session-actions">{item.status==='active'&&<button className="button primary" onClick={()=>void launch(item.runtimeId)}><ExternalLink size={15}/>열기</button>}{item.status==='active'&&<button className="button ghost" title="지금 하던 일을 에이전트에게 맡기고 화면을 닫아도 계속 진행합니다." onClick={()=>setHandOff(item)}><ListChecks size={15}/>백그라운드로</button>}{item.status==='active'&&<button className="button ghost" onClick={()=>void close(item.id)}><Square size={14}/>종료</button>}</div></article>)}</section>}
    {handOff&&<BackgroundDrawer session={handOff} close={()=>setHandOff(null)} done={(message)=>{setHandOff(null);setNotice(message)}}/>}
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

/** Hands the work in an interactive session to the agent as a background task.
 *  The runtime the session is using is reused rather than a second one started,
 *  so the person can keep watching the same workspace while it runs. */
function BackgroundDrawer({session,close,done}:{session:RuntimeSession;close:()=>void;done:(message:string)=>void}) {
  const [title,setTitle] = useState(session.title)
  const [input,setInput] = useState('')
  const [priority,setPriority] = useState('normal')
  const [busy,setBusy] = useState(false)
  const [error,setError] = useState('')
  const submit = async (event:FormEvent) => {
    event.preventDefault(); setBusy(true); setError('')
    try {
      await api.post('/api/v1/tasks',{agentId:session.agentId,title,input,priority,source:'manual'})
      done('백그라운드 작업으로 넘겼습니다. 작업 화면에서 진행 상황을 볼 수 있습니다.')
    } catch (e) { setError(e instanceof Error?e.message:'작업으로 전환하지 못했습니다.'); setBusy(false) }
  }
  return <Drawer title="백그라운드 작업으로 전환" subtitle={`${session.agentName} · ${runtimeLabel(session.runtimeType)}`} close={close}
    footer={<><button className="button ghost" onClick={close}>취소</button>
      <button className="button primary" form="handoff-form" disabled={busy}>{busy?'전환 중…':'작업 만들기'}</button></>}>
    <form id="handoff-form" className="drawer-form" onSubmit={submit}>
      {error&&<ErrorBanner message={error} onClose={()=>setError('')}/>}
      <label><span>작업 제목 <b>*</b></span><input required maxLength={200} value={title} onChange={(e)=>setTitle(e.target.value)}/></label>
      <label><span>맡길 내용 <b>*</b></span><textarea required rows={6} value={input} onChange={(e)=>setInput(e.target.value)} placeholder="지금까지의 맥락과 이어서 해야 할 일을 적어 주세요."/>
        <small>에이전트는 이 설명만 보고 이어서 진행합니다. 화면에서 하던 대화는 자동으로 전달되지 않습니다.</small></label>
      <label><span>우선순위</span>
        <select value={priority} onChange={(e)=>setPriority(e.target.value)}>
          <option value="critical">긴급</option><option value="high">높음</option>
          <option value="normal">보통</option><option value="low">낮음</option><option value="background">배경</option>
        </select>
      </label>
      <div className="info-box"><ListChecks size={17}/><div><strong>런타임은 그대로 유지됩니다</strong>
        <p>이미 실행 중인 런타임을 그대로 사용하므로, 작업이 도는 동안에도 같은 작업공간을 열어 볼 수 있습니다.</p></div></div>
    </form>
  </Drawer>
}
