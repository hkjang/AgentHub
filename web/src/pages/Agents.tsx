import { FormEvent, useCallback, useEffect, useState } from 'react'
import { Activity, Bot, CircleStop, ExternalLink, FileText, ListChecks, MoreHorizontal, Pencil, Play, Plus, RefreshCw, Search, Target, Trash2, Zap } from 'lucide-react'
import { Link } from 'react-router-dom'
import { api } from '../api'
import { ConfirmDialog, Drawer, Empty, ErrorBanner, Loading, PageHeader, StatusBadge } from '../components/UI'
import { RUNTIME_TYPES, relativeTime, runtimeCode, runtimeLabel, runtimeLogoClass } from '../runtime'
import type { Agent, AgentGoal, AgentMemory, AgentTrigger, ExecutionMode, MCPBundle, ModelEndpoint, RuntimeProfile, Workspace } from '../types'

export function Agents({runtimeOnly=false}:{runtimeOnly?:boolean}) {
  const [agents,setAgents]=useState<Agent[]|null>(null)
  const [selectedId,setSelectedId]=useState<string|null>(null)
  const [busy,setBusy]=useState<string|null>(null)
  const [error,setError]=useState('')
  const [editing,setEditing]=useState<Agent|null>(null)
  const [goalFor,setGoalFor]=useState<Agent|null>(null)
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
    {scoped.length>0&&visible.length===0?<div className="empty-compact">검색 조건에 맞는 에이전트가 없습니다.</div>:visible.length===0?<Empty icon={<Bot/>} title="아직 에이전트가 없습니다" description="카탈로그에서 검증된 템플릿을 선택해 첫 에이전트를 만들어 보세요." action={<Link className="button primary" to="/catalog">카탈로그 열기</Link>}/>:<section className="table-panel"><div className="table-wrap custom-scroll"><table><thead><tr><th>에이전트</th><th>런타임</th><th>상태</th><th>Pod / 노드</th><th>마지막 변경</th><th aria-label="작업"/></tr></thead><tbody>{visible.map(agent=><tr key={agent.id}><td><button className="agent-cell" onClick={()=>setSelectedId(agent.id)}><div className={runtimeLogoClass(agent.runtimeType)}>{runtimeCode(agent.runtimeType)}</div><div><strong>{agent.name}</strong><span>정의 v{agent.version}</span></div></button></td><td><span className="runtime-name">{runtimeLabel(agent.runtimeType)}</span></td><td><StatusBadge status={agent.runtime?.status??'stopped'}/></td><td><div className="mono-stack"><code>{agent.runtime?.podName||'—'}</code><small>{agent.runtime?.nodeName||'할당 전'}</small></div></td><td><span title={new Date(agent.updatedAt).toLocaleString('ko-KR')}>{relativeTime(agent.updatedAt)}</span></td><td><div className="row-actions">{!agent.runtime||['stopped','failed','crashed'].includes(agent.runtime.status)?<button title="시작" disabled={!!busy} onClick={()=>void act(agent,agent.runtime?'start':'spawn')}><Play size={16}/></button>:<button title="중지" disabled={!!busy} onClick={()=>void act(agent,'stop')}><CircleStop size={16}/></button>}<button title="목표 · 자동화" onClick={()=>setGoalFor(agent)}><Target size={15}/></button><button title="수정" onClick={()=>setEditing(agent)}><Pencil size={15}/></button><button className="danger" title="삭제" disabled={!!busy} onClick={()=>{setRemoveError('');setRemoving(agent)}}><Trash2 size={15}/></button><button title="상세" onClick={()=>setSelectedId(agent.id)}><MoreHorizontal size={18}/></button></div></td></tr>)}</tbody></table></div></section>}
    {selected&&<AgentDrawer agent={selected} close={()=>setSelectedId(null)} action={act} busy={!!busy} edit={()=>setEditing(selected)} remove={()=>{setRemoveError('');setRemoving(selected)}}/>}
    {editing&&<AgentEditDrawer agent={editing} close={()=>setEditing(null)} done={()=>{setEditing(null);void refresh()}}/>}
    {goalFor&&<GoalDrawer agent={goalFor} close={()=>setGoalFor(null)}/>}
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

const EXECUTION_MODES: {value: ExecutionMode; label: string; hint: string}[] = [
  {value: 'interactive', label: '대화형', hint: '기존 방식. Runtime을 열어 직접 작업합니다.'},
  {value: 'task', label: '작업', hint: '버튼이나 API로 일회성 작업을 맡깁니다.'},
  {value: 'scheduled', label: '예약', hint: '정해진 일정에 스스로 실행합니다.'},
  {value: 'event', label: '이벤트', hint: 'Webhook 등 외부 이벤트로 실행합니다.'},
  {value: 'service', label: '상시', hint: 'API로 항상 응답하는 에이전트입니다.'},
  {value: 'hybrid', label: '하이브리드', hint: '대화형과 자동 실행을 함께 사용합니다.'},
]

/**
 * Goal, guardrails and triggers. This is the screen that turns an interactive
 * agent into one that works on its own, so it deliberately keeps the runtime and
 * model bindings out of the way — those stay on the edit drawer.
 */
function GoalDrawer({agent,close}:{agent:Agent;close:()=>void}) {
  const [mode,setMode]=useState<ExecutionMode>('interactive')
  const [goal,setGoal]=useState<AgentGoal|null>(null)
  const [triggers,setTriggers]=useState<AgentTrigger[]>([])
  const [memories,setMemories]=useState<AgentMemory[]>([])
  const [busy,setBusy]=useState(false)
  const [error,setError]=useState('')
  const [notice,setNotice]=useState('')
  const [addingTrigger,setAddingTrigger]=useState(false)

  const load=useCallback(async()=>{
    try{
      const [goalResult,triggerResult,memoryResult]=await Promise.all([
        api.get<{goal:AgentGoal;executionMode:ExecutionMode}>(`/api/v1/agents/${agent.id}/goal`),
        api.get<{items?:AgentTrigger[]}>(`/api/v1/agents/${agent.id}/triggers`),
        api.get<{items?:AgentMemory[]}>(`/api/v1/agents/${agent.id}/memories`),
      ])
      setGoal(goalResult.goal); setMode(goalResult.executionMode); setTriggers(triggerResult.items??[]); setMemories(memoryResult.items??[])
    }catch(e){ setError(e instanceof Error?e.message:'목표 설정을 불러오지 못했습니다.') }
  },[agent.id])
  useEffect(()=>{void load()},[load])

  const update=(patch:Partial<AgentGoal>)=>setGoal((current)=>current?{...current,...patch}:current)
  const submit=async(event:FormEvent)=>{
    event.preventDefault()
    if(!goal) return
    setBusy(true); setError(''); setNotice('')
    try{
      await api.put(`/api/v1/agents/${agent.id}/goal`,{...goal,executionMode:mode})
      setNotice('저장했습니다. 다음 작업부터 적용됩니다.')
    }catch(e){ setError(e instanceof Error?e.message:'저장하지 못했습니다.') }
    finally{ setBusy(false) }
  }
  const runNow=async()=>{
    setError(''); setNotice('')
    try{
      await api.post(`/api/v1/agents/${agent.id}/run`,{title:`${agent.name} 수동 실행`,input:goal?.description??'',priority:'normal'})
      setNotice('작업을 대기열에 넣었습니다. 작업 대기열 화면에서 진행 상황을 볼 수 있습니다.')
    }catch(e){ setError(e instanceof Error?e.message:'작업을 시작하지 못했습니다.') }
  }
  const removeMemory=async(id:string)=>{
    try{ await api.delete(`/api/v1/memories/${id}`); await load() }
    catch(e){ setError(e instanceof Error?e.message:'기억을 삭제하지 못했습니다.') }
  }
  const removeTrigger=async(id:string)=>{
    try{ await api.delete(`/api/v1/triggers/${id}`); await load() }
    catch(e){ setError(e instanceof Error?e.message:'Trigger를 삭제하지 못했습니다.') }
  }

  if(!goal) return <Drawer title={`${agent.name} · 목표`} close={close}>{error?<ErrorBanner message={error}/>:<Loading/>}</Drawer>

  const criteriaText=(values:string[])=>values.join('\n')
  const parseCriteria=(value:string)=>value.split('\n').map((line)=>line.trim()).filter(Boolean)

  return <Drawer title={`${agent.name} · 목표와 자동화`} subtitle={EXECUTION_MODES.find((m)=>m.value===mode)?.hint} close={close}
    footer={<><button className="button ghost" onClick={close}>닫기</button>
      <button type="button" className="button ghost" onClick={()=>void runNow()}><Play size={15}/>지금 실행</button>
      <button className="button primary" form="goal-form" disabled={busy}>{busy?'저장 중…':'저장'}</button></>}>
    <form id="goal-form" className="drawer-form" onSubmit={submit}>
      {error&&<ErrorBanner message={error} onClose={()=>setError('')}/>}
      {notice&&<div className="info-box"><Zap size={17}/><div><strong>완료</strong><p>{notice}</p></div></div>}

      <label><span>실행 모드</span>
        <select value={mode} onChange={(e)=>setMode(e.target.value as ExecutionMode)}>
          {EXECUTION_MODES.map((item)=><option key={item.value} value={item.value}>{item.label}</option>)}
        </select>
        <small>{EXECUTION_MODES.find((m)=>m.value===mode)?.hint}</small>
      </label>

      <label><span>목표</span>
        <textarea rows={3} value={goal.description} onChange={(e)=>update({description:e.target.value})}
          placeholder="예) 신규 장애를 분석하고 원인을 찾아 담당자에게 개선안을 보고한다."/>
      </label>
      <label><span>완료 조건 (한 줄에 하나)</span>
        <textarea rows={4} value={criteriaText(goal.successCriteria)} onChange={(e)=>update({successCriteria:parseCriteria(e.target.value)})}
          placeholder={'신규 장애 조회 완료\n관련 로그 분석 완료\n보고서 저장 완료'}/>
      </label>
      <label><span>실패 조건 (한 줄에 하나)</span>
        <textarea rows={2} value={criteriaText(goal.failureCriteria)} onChange={(e)=>update({failureCriteria:parseCriteria(e.target.value)})}
          placeholder={'MCP 조회 3회 연속 실패'}/>
      </label>
      <label><span>제약</span><textarea rows={2} value={goal.constraints} onChange={(e)=>update({constraints:e.target.value})} placeholder="예) 운영 DB에 쓰기 금지"/></label>

      <fieldset><legend>완료 판정</legend>
        <label><span>판정 방식</span>
          <select value={goal.completionStrategy} onChange={(e)=>update({completionStrategy:e.target.value as AgentGoal['completionStrategy']})}>
            <option value="agent">Agent 선언 — 에이전트가 완료라고 하면 완료</option>
            <option value="rule">규칙 — 완료 조건이 기록에서 확인되어야 함</option>
            <option value="judge">LLM 판정 — 별도 모델이 결과를 평가</option>
            <option value="composite">복합 — 규칙과 LLM 판정을 모두 통과</option>
          </select>
          <small>규칙·복합 판정을 쓰려면 완료 조건을 하나 이상 정의해야 합니다.</small>
        </label>
      </fieldset>

      <fieldset><legend>자율성</legend>
        <label><span>계획 수립</span>
          <select value={goal.plannerMode} onChange={(e)=>update({plannerMode:e.target.value as AgentGoal['plannerMode']})}>
            <option value="native">Runtime 위임 — OpenCode·Hermes 자체 계획 사용</option>
            <option value="platform">Platform — AgentHub가 계획을 세움</option>
            <option value="hybrid">Hybrid — 계획 수립 후 Runtime이 세부 수행</option>
            <option value="none">계획 없음</option>
          </select>
          <small>Runtime이 자체 Agent 기능을 가진 경우 Native가 적합합니다.</small>
        </label>
        <label className="toggle-row"><span>상태 변경 작업에 사람 승인 요구</span>
          <input type="checkbox" checked={goal.approvalRequired} onChange={(e)=>update({approvalRequired:e.target.checked})}/><i/></label>
        <label><span>다른 에이전트에 위임 허용 깊이</span>
          <input type="number" min={0} max={5} value={goal.maxDelegationDepth} onChange={(e)=>update({maxDelegationDepth:Number(e.target.value)})}/>
          <small>0이면 위임하지 않습니다. 순환 위임은 자동으로 차단됩니다.</small>
        </label>
      </fieldset>

      <fieldset><legend>실행 한도</legend>
        <div className="form-grid">
          <label><span>최대 단계</span><input type="number" min={1} max={100} value={goal.maxSteps} onChange={(e)=>update({maxSteps:Number(e.target.value)})}/></label>
          <label><span>최대 실행 시간(초)</span><input type="number" min={30} max={86400} value={goal.maxDurationSeconds} onChange={(e)=>update({maxDurationSeconds:Number(e.target.value)})}/></label>
          <label><span>재시도 횟수</span><input type="number" min={0} max={10} value={goal.maxRetries} onChange={(e)=>update({maxRetries:Number(e.target.value)})}/></label>
          <label><span>동시 실행 수</span><input type="number" min={1} max={20} value={goal.maxConcurrentRuns} onChange={(e)=>update({maxConcurrentRuns:Number(e.target.value)})}/></label>
        </div>
        <label><span>중복 실행 정책</span>
          <select value={goal.concurrencyPolicy} onChange={(e)=>update({concurrencyPolicy:e.target.value as AgentGoal['concurrencyPolicy']})}>
            <option value="queue">대기 — 앞선 실행이 끝나면 이어서 실행</option>
            <option value="reject">거부 — 실행 중이면 새 작업을 실패 처리</option>
            <option value="parallel">병렬 — 동시에 실행</option>
            <option value="replace">교체 — 기존 실행을 종료</option>
          </select>
        </label>
      </fieldset>

      <fieldset><legend>Runtime 정책</legend>
        <label className="toggle-row"><span>작업 시 Runtime 자동 확보</span>
          <input type="checkbox" checked={goal.startOnDemand} onChange={(e)=>update({startOnDemand:e.target.checked})}/><i/></label>
        <label className="toggle-row"><span>작업 완료 후 Runtime 중지</span>
          <input type="checkbox" checked={goal.stopAfterTask} onChange={(e)=>update({stopAfterTask:e.target.checked})}/><i/></label>
        <small>이미 사용 중이던 Runtime은 사용자 작업을 방해하지 않도록 중지하지 않습니다.</small>
      </fieldset>
    </form>

    <section className="detail-section"><h4>Trigger</h4>
      {triggers.length===0
        ? <div className="empty-compact">등록된 Trigger가 없습니다. 예약 실행이나 Webhook을 추가해 보세요.</div>
        : <div className="tool-links">{triggers.map((trigger)=><div key={trigger.id} className="trigger-row">
            <ListChecks size={16}/>
            <div><strong>{trigger.name}</strong>
              <small>{trigger.type==='cron'?`${trigger.schedule} · ${trigger.timezone}`:trigger.type==='webhook'?(trigger.hasSecret?'Webhook · 서명 설정됨':'Webhook · 서명 미설정'):'수동'}</small>
              {trigger.nextFireAt&&<small>다음 실행 {new Date(trigger.nextFireAt).toLocaleString('ko-KR')}</small>}
            </div>
            <StatusBadge status={trigger.enabled?'active':'disabled'}/>
            <button className="danger" title="삭제" onClick={()=>void removeTrigger(trigger.id)}><Trash2 size={15}/></button>
          </div>)}</div>}
      <button type="button" className="button ghost" onClick={()=>setAddingTrigger(true)}><Plus size={14}/>Trigger 추가</button>
    </section>

    <section className="detail-section"><h4>기억</h4>
      {memories.length===0
        ? <div className="empty-compact">아직 저장된 기억이 없습니다. 실행 중 에이전트가 남긴 사실이 여기에 쌓입니다.</div>
        : <div className="tool-links">{memories.map((memory)=><div key={memory.id} className="trigger-row">
            <Bot size={16}/>
            <div><strong>{memory.key}</strong><small>{memory.value}</small></div>
            <button className="danger" title="삭제" onClick={()=>void removeMemory(memory.id)}><Trash2 size={15}/></button>
          </div>)}</div>}
      <small>기억은 Runtime 홈이 아니라 플랫폼에 저장되므로 Pod가 사라져도 유지됩니다.</small>
    </section>

    {addingTrigger&&<TriggerDrawer agent={agent} close={()=>setAddingTrigger(false)} done={()=>{setAddingTrigger(false);void load()}}/>}
  </Drawer>
}

function TriggerDrawer({agent,close,done}:{agent:Agent;close:()=>void;done:()=>void}) {
  const [name,setName]=useState('')
  const [type,setType]=useState<'cron'|'webhook'>('cron')
  const [schedule,setSchedule]=useState('0 8 * * *')
  const [timezone,setTimezone]=useState('Asia/Seoul')
  const [taskTitle,setTaskTitle]=useState('')
  const [taskInput,setTaskInput]=useState('')
  const [secret,setSecret]=useState('')
  const [enabled,setEnabled]=useState(true)
  const [busy,setBusy]=useState(false)
  const [error,setError]=useState('')
  const submit=async(event:FormEvent)=>{
    event.preventDefault(); setBusy(true); setError('')
    try{
      await api.post(`/api/v1/agents/${agent.id}/triggers`,{name,type,enabled,schedule:type==='cron'?schedule:'',timezone,taskTitle,taskInput,priority:'normal',secret:type==='webhook'?secret:''})
      done()
    }catch(e){ setError(e instanceof Error?e.message:'Trigger를 저장하지 못했습니다.'); setBusy(false) }
  }
  return <Drawer title="Trigger 추가" subtitle="에이전트가 스스로 작업을 시작하는 조건입니다." close={close}
    footer={<><button className="button ghost" onClick={close}>취소</button>
      <button className="button primary" form="trigger-form" disabled={busy}>{busy?'저장 중…':'저장'}</button></>}>
    <form id="trigger-form" className="drawer-form" onSubmit={submit}>
      {error&&<ErrorBanner message={error} onClose={()=>setError('')}/>}
      <label><span>이름 <b>*</b></span><input required maxLength={80} value={name} onChange={(e)=>setName(e.target.value)} placeholder="매일 아침 장애 점검"/></label>
      <label><span>유형</span>
        <select value={type} onChange={(e)=>setType(e.target.value as 'cron'|'webhook')}>
          <option value="cron">예약 (cron)</option>
          <option value="webhook">Webhook</option>
        </select>
      </label>
      {type==='cron'
        ? <><label><span>일정 <b>*</b></span><input required value={schedule} onChange={(e)=>setSchedule(e.target.value)} placeholder="0 8 * * *"/>
            <small>분 시 일 월 요일. 예) <code>0 8 * * *</code> 매일 08:00, <code>*/30 * * * *</code> 30분마다</small></label>
          <label><span>시간대</span><input value={timezone} onChange={(e)=>setTimezone(e.target.value)} placeholder="Asia/Seoul"/></label></>
        : <label><span>Webhook 서명 키 <b>*</b></span><input required type="password" autoComplete="new-password" value={secret} onChange={(e)=>setSecret(e.target.value)}/>
            <small>호출 측은 본문 HMAC-SHA256을 <code>X-AgentHub-Signature</code> 헤더로 보내야 합니다.</small></label>}
      <label><span>작업 제목</span><input maxLength={200} value={taskTitle} onChange={(e)=>setTaskTitle(e.target.value)} placeholder="비워 두면 Trigger 이름을 사용합니다"/></label>
      <label><span>작업 내용</span><textarea rows={4} value={taskInput} onChange={(e)=>setTaskInput(e.target.value)} placeholder="이 Trigger가 만들 작업의 지시 내용"/></label>
      <label className="toggle-row"><span>활성화</span><input type="checkbox" checked={enabled} onChange={(e)=>setEnabled(e.target.checked)}/><i/></label>
    </form>
  </Drawer>
}
