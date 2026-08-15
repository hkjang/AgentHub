import { FormEvent, useCallback, useEffect, useState } from 'react'
import { Activity, Bot, CircleStop, ExternalLink, FileText, MoreHorizontal, Pencil, Play, Plus, RefreshCw, Search, Trash2 } from 'lucide-react'
import { Link } from 'react-router-dom'
import { api } from '../api'
import { ConfirmDialog, Drawer, Empty, ErrorBanner, Loading, PageHeader, StatusBadge } from '../components/UI'
import { RUNTIME_TYPES, relativeTime, runtimeCode, runtimeLabel, runtimeLogoClass } from '../runtime'
import type { Agent, MCPBundle, ModelEndpoint, RuntimeProfile, Workspace } from '../types'

export function Agents({runtimeOnly=false}:{runtimeOnly?:boolean}) {
  const [agents,setAgents]=useState<Agent[]|null>(null)
  const [selectedId,setSelectedId]=useState<string|null>(null)
  const [busy,setBusy]=useState<string|null>(null)
  const [error,setError]=useState('')
  const [editing,setEditing]=useState<Agent|null>(null)
  const [removing,setRemoving]=useState<Agent|null>(null)
  const [removeBusy,setRemoveBusy]=useState(false)
  const [removeError,setRemoveError]=useState('')
  const [query,setQuery]=useState('')
  const [runtimeFilter,setRuntimeFilter]=useState('')
  const refresh=useCallback(async()=>{
    try {
      const res = await api.get<{items?:Agent[]}|Agent[]>('/api/v1/agents')
      setAgents(Array.isArray(res)?res:(res.items??[]))
    } catch(err) {
      setError(err instanceof Error?err.message:'에이전트 목록을 불러오지 못했습니다.')
    }
  },[])
  useEffect(()=>{
    void refresh()
    // Each poll makes the control plane reconcile every runtime against
    // Kubernetes, so back off while the tab is in the background.
    const timer = setInterval(() => { if(document.visibilityState==='visible') void refresh() }, 5000)
    const onVisible = () => { if(document.visibilityState==='visible') void refresh() }
    document.addEventListener('visibilitychange', onVisible)
    return () => { clearInterval(timer); document.removeEventListener('visibilitychange', onVisible) }
  },[refresh])
  const act=async(agent:Agent,verb:'spawn'|'start'|'stop'|'restart')=>{
    setError('')
    setBusy(agent.id)
    try {
      if(verb==='spawn') await api.post(`/api/v1/agents/${agent.id}/spawn`)
      else if(verb==='start') await api.post(`/api/v1/runtimes/${agent.runtime!.id}/start`)
      else if(verb==='stop') await api.post(`/api/v1/runtimes/${agent.runtime!.id}/stop`)
      else if(verb==='restart') await api.post(`/api/v1/runtimes/${agent.runtime!.id}/restart`)
      await refresh()
    } catch(err) {
      setError(err instanceof Error?err.message:`작업(${verb})을 수행하지 못했습니다.`)
    } finally {
      setBusy(null)
    }
  }
  const remove=async()=>{
    if(!removing) return
    setRemoveBusy(true); setRemoveError('')
    try {
      await api.delete(`/api/v1/agents/${removing.id}`)
      setRemoving(null); setSelectedId(null)
      await refresh()
    } catch(err) {
      setRemoveError(err instanceof Error?err.message:'에이전트를 삭제하지 못했습니다.')
    } finally {
      setRemoveBusy(false)
    }
  }
  if(!agents) return <Loading/>
  const list = Array.isArray(agents) ? agents : []
  const scoped = runtimeOnly ? list.filter(a => Boolean(a?.runtime)) : list
  const needle = query.trim().toLowerCase()
  const visible = scoped.filter((agent) => {
    if (runtimeFilter && agent.runtimeType !== runtimeFilter) return false
    if (!needle) return true
    // Search what is actually on screen plus the Pod name, which is what users
    // have in hand when they arrive from kubectl output.
    return `${agent.name} ${agent.description} ${runtimeLabel(agent.runtimeType)} ${agent.runtimeType} ${agent.runtime?.podName ?? ''}`.toLowerCase().includes(needle)
  })
  const selected = list.find(a => a?.id === selectedId) || null
  const present = RUNTIME_TYPES.filter((type) => scoped.some((agent) => agent.runtimeType === type))
  return <div className="page">
    <PageHeader eyebrow={runtimeOnly?'런타임 제어':'내 작업공간'} title={runtimeOnly?'내 런타임':'내 에이전트'} description={runtimeOnly?'사용자 전용 Kubernetes 런타임의 수명주기와 상태를 관리합니다.':'에이전트 정의와 실행 중인 런타임을 분리해서 안전하게 관리합니다.'} actions={<Link className="button primary" to="/catalog"><Plus size={17}/>새 에이전트</Link>}/>
    {error&&<ErrorBanner message={error} onClose={()=>setError('')}/>}
    {scoped.length>0&&<div className="toolbar">
      <div className="search-box"><Search size={17}/><input value={query} onChange={(e)=>setQuery(e.target.value)} placeholder="이름, 설명, Pod 이름 검색" aria-label="에이전트 검색"/></div>
      <div className="filter-chips">
        <button className={runtimeFilter===''?'selected':''} onClick={()=>setRuntimeFilter('')}>전체 {scoped.length}</button>
        {present.map((type)=><button key={type} className={runtimeFilter===type?'selected':''} onClick={()=>setRuntimeFilter(type)}>{runtimeLabel(type)} {scoped.filter((a)=>a.runtimeType===type).length}</button>)}
      </div>
    </div>}
    {scoped.length>0&&visible.length===0?<div className="empty-compact">검색 조건에 맞는 에이전트가 없습니다.</div>:visible.length===0?<Empty icon={<Bot/>} title="아직 에이전트가 없습니다" description="카탈로그에서 검증된 템플릿을 선택해 첫 에이전트를 만들어 보세요." action={<Link className="button primary" to="/catalog">카탈로그 열기</Link>}/>:<section className="table-panel"><div className="table-wrap custom-scroll"><table><thead><tr><th>에이전트</th><th>런타임</th><th>상태</th><th>Pod / 노드</th><th>마지막 변경</th><th aria-label="작업"/></tr></thead><tbody>{visible.map(agent=><tr key={agent.id}><td><button className="agent-cell" onClick={()=>setSelectedId(agent.id)}><div className={runtimeLogoClass(agent.runtimeType)}>{runtimeCode(agent.runtimeType)}</div><div><strong>{agent.name}</strong><span>정의 v{agent.version}</span></div></button></td><td><span className="runtime-name">{runtimeLabel(agent.runtimeType)}</span></td><td><StatusBadge status={agent.runtime?.status??'stopped'}/></td><td><div className="mono-stack"><code>{agent.runtime?.podName||'—'}</code><small>{agent.runtime?.nodeName||'할당 전'}</small></div></td><td><span title={new Date(agent.updatedAt).toLocaleString('ko-KR')}>{relativeTime(agent.updatedAt)}</span></td><td><div className="row-actions">{!agent.runtime||['stopped','failed','crashed'].includes(agent.runtime.status)?<button title="시작" disabled={!!busy} onClick={()=>void act(agent,agent.runtime?'start':'spawn')}><Play size={16}/></button>:<button title="중지" disabled={!!busy} onClick={()=>void act(agent,'stop')}><CircleStop size={16}/></button>}<button title="수정" onClick={()=>setEditing(agent)}><Pencil size={15}/></button><button className="danger" title="삭제" disabled={!!busy} onClick={()=>{setRemoveError('');setRemoving(agent)}}><Trash2 size={15}/></button><button title="상세" onClick={()=>setSelectedId(agent.id)}><MoreHorizontal size={18}/></button></div></td></tr>)}</tbody></table></div></section>}
    {selected&&<AgentDrawer agent={selected} close={()=>setSelectedId(null)} action={act} busy={!!busy} edit={()=>setEditing(selected)} remove={()=>{setRemoveError('');setRemoving(selected)}}/>}
    {editing&&<AgentEditDrawer agent={editing} close={()=>setEditing(null)} done={()=>{setEditing(null);void refresh()}}/>}
    {removing&&<ConfirmDialog
      title="에이전트를 삭제할까요?"
      message={<><strong>{removing.name}</strong> 정의와 실행 중인 런타임(Pod, Service, NetworkPolicy)이 함께 삭제됩니다.<br/>연결된 작업공간 볼륨은 보존됩니다.</>}
      busy={removeBusy} error={removeError}
      onConfirm={()=>void remove()} onCancel={()=>setRemoving(null)}/>}
  </div>
}

/** Edits a saved definition. The runtime type is fixed once created, so it is shown read-only. */
function AgentEditDrawer({agent,close,done}:{agent:Agent;close:()=>void;done:()=>void}) {
  const [name,setName]=useState(agent.name)
  const [description,setDescription]=useState(agent.description)
  const [profile,setProfile]=useState(agent.runtimeProfileId??'')
  const [model,setModel]=useState(agent.modelEndpointId??'')
  const [bundle,setBundle]=useState(agent.mcpBundleId??'')
  const [workspace,setWorkspace]=useState(agent.workspaceId??'')
  const [prompt,setPrompt]=useState(()=>{
    const spec=agent.spec as {systemPrompt?:string}|undefined
    return spec?.systemPrompt??''
  })
  const [profiles,setProfiles]=useState<RuntimeProfile[]>([])
  const [models,setModels]=useState<ModelEndpoint[]>([])
  const [bundles,setBundles]=useState<MCPBundle[]>([])
  const [workspaces,setWorkspaces]=useState<Workspace[]>([])
  const [busy,setBusy]=useState(false)
  const [error,setError]=useState('')
  const [notice,setNotice]=useState('')
  useEffect(()=>{
    void Promise.all([
      api.get<{items?:RuntimeProfile[]}>('/api/v1/runtime-profiles').then(v=>setProfiles(v.items??[])),
      api.get<{items?:ModelEndpoint[]}>('/api/v1/models').then(v=>setModels(v.items??[])),
      api.get<{items?:MCPBundle[]}>('/api/v1/mcp-bundles').then(v=>setBundles(v.items??[])),
      api.get<{items?:Workspace[]}>('/api/v1/workspaces').then(v=>setWorkspaces(v.items??[])),
    ]).catch(()=>setError('선택 목록을 불러오지 못했습니다.'))
  },[])
  const submit=async(event:FormEvent)=>{
    event.preventDefault(); setBusy(true); setError(''); setNotice('')
    try {
      const result=await api.put<{warning?:string}>(`/api/v1/agents/${agent.id}`,{name,description,runtimeProfileId:profile,modelEndpointId:model,mcpBundleId:bundle,workspaceId:workspace,systemPrompt:prompt})
      if(result.warning){ setNotice(result.warning); setBusy(false); return }
      done()
    } catch(err) {
      setError(err instanceof Error?err.message:'에이전트를 수정하지 못했습니다.')
      setBusy(false)
    }
  }
  return <Drawer title={`${agent.name} 수정`} subtitle={`${runtimeLabel(agent.runtimeType)} · 정의 v${agent.version}`} close={close} footer={<><button type="button" className="button ghost" onClick={close}>취소</button>{notice?<button type="button" className="button primary" onClick={done}>확인</button>:<button className="button primary" form="agent-edit" disabled={busy}>{busy?'저장 중…':'변경사항 저장'}</button>}</>}>
    <form id="agent-edit" className="drawer-form" onSubmit={submit}>
      {error&&<ErrorBanner message={error} onClose={()=>setError('')}/>}
      {notice&&<div className="info-box"><RefreshCw size={17}/><div><strong>저장되었습니다</strong><p>{notice}</p></div></div>}
      <section className="selection-summary"><div className={runtimeLogoClass(agent.runtimeType,'large')}>{runtimeCode(agent.runtimeType)}</div><div><span>런타임 (변경 불가)</span><strong>{runtimeLabel(agent.runtimeType)}</strong><small>런타임 유형은 Pod와 저장소 구조를 결정하므로 생성 후 바꿀 수 없습니다.</small></div></section>
      <label><span>에이전트 이름 <b>*</b></span><input required maxLength={80} value={name} onChange={e=>setName(e.target.value)}/></label>
      <label><span>설명</span><textarea rows={2} value={description} onChange={e=>setDescription(e.target.value)}/></label>
      <label><span>런타임 프로파일</span><select value={profile} onChange={e=>setProfile(e.target.value)}><option value="">선택 안 함</option>{profiles.map(p=><option value={p.id} key={p.id}>{p.name} · {p.cpuMillis/1000} CPU / {p.memoryMb/1024} GB</option>)}</select></label>
      <label><span>모델</span><select value={model} onChange={e=>setModel(e.target.value)}><option value="">연결 안 함</option>{models.map(v=><option value={v.id} key={v.id}>{v.name} · {v.defaultModel}</option>)}</select></label>
      <label><span>MCP Bundle</span><select value={bundle} onChange={e=>setBundle(e.target.value)}><option value="">MCP 없음</option>{bundles.map(v=><option value={v.id} key={v.id}>{v.name}</option>)}</select></label>
      <label><span>작업공간</span><select value={workspace} onChange={e=>setWorkspace(e.target.value)}><option value="">작업공간 없음</option>{workspaces.map(v=><option value={v.id} key={v.id}>{v.name} · {v.sizeGb} GB</option>)}</select></label>
      <label><span>추가 지시사항</span><textarea rows={5} value={prompt} onChange={e=>setPrompt(e.target.value)}/></label>
    </form>
  </Drawer>
}

function AgentDrawer({agent,close,action,busy,edit,remove}:{agent:Agent;close:()=>void;action:(a:Agent,v:'spawn'|'start'|'stop'|'restart')=>Promise<void>;busy:boolean;edit:()=>void;remove:()=>void}) {
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
      setLogError(error instanceof Error?error.message:'런타임 세션을 열지 못했습니다.')
    }
  }
  return <Drawer title={agent.name} subtitle={`에이전트 정의 v${agent.version}`} close={close} footer={<><button className="button ghost" onClick={edit}><Pencil size={15}/>수정</button><button className="button ghost danger-text" onClick={remove}><Trash2 size={15}/>삭제</button>{runtime&&<button className="button ghost" disabled={busy} onClick={()=>void action(agent,'restart')}><RefreshCw size={16}/>재시작</button>}{(!runtime||['stopped','failed'].includes(runtime.status))?<button className="button primary" disabled={busy} onClick={()=>void action(agent,runtime?'start':'spawn')}><Play size={16}/>런타임 시작</button>:<button className="button danger" disabled={busy} onClick={()=>void action(agent,'stop')}><CircleStop size={16}/>중지</button>}</>}>
    <div className="detail-hero"><div className={runtimeLogoClass(agent.runtimeType,'xlarge')}>{runtimeCode(agent.runtimeType)}</div><div><StatusBadge status={runtime?.status??'stopped'}/><h3>{runtimeLabel(agent.runtimeType)}</h3><p>{agent.description||'설명 없음'}</p></div></div>
    <section className="detail-section"><h4>런타임</h4><dl className="detail-list"><div><dt>인스턴스 ID</dt><dd><code>{runtime?.id??'아직 생성되지 않음'}</code></dd></div><div><dt>목표 상태</dt><dd>{runtime?.desiredState??'stopped'}</dd></div><div><dt>CRD</dt><dd><code>{runtime?.crdName||'—'}</code></dd></div><div><dt>Pod</dt><dd><code>{runtime?.podName||'—'}</code></dd></div><div><dt>노드</dt><dd>{runtime?.nodeName||'할당 전'}</dd></div><div><dt>재시작 횟수</dt><dd>{runtime?.restartCount??0}</dd></div></dl></section>
    <section className="detail-section"><h4>작업공간 도구</h4><div className="tool-links">{runtime&&<button disabled={!ready||busy} onClick={()=>void launch()}><Activity size={17}/>작업공간 열기<ExternalLink size={14}/></button>}<button disabled={!runtime} onClick={()=>void loadLogs()}><FileText size={17}/>Logs</button></div></section>
    {logError&&<ErrorBanner message={logError}/>} {logs&&<section className="detail-section"><h4>런타임 로그</h4><pre className="runtime-log-preview custom-scroll">{logs}</pre></section>}
    {runtime?.failureReason&&<ErrorBanner message={runtime.failureReason}/>}
  </Drawer>
}
