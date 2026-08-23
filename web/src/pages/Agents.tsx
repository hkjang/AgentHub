import { FormEvent, useCallback, useEffect, useState } from 'react'
import { Activity, Bot, CircleStop, Download, ExternalLink, FileText, History, ListChecks, MoreHorizontal, Pencil, Play, Plus, RefreshCw, RotateCcw, Search, ShieldAlert, ShieldCheck, Target, Trash2, Upload, Zap } from 'lucide-react'
import { Link } from 'react-router-dom'
import { useAuth } from '../App'
import { api } from '../api'
import { ConfirmDialog, Drawer, Empty, ErrorBanner, GuidePanel, Loading, PageHeader, StatusBadge } from '../components/UI'
import { useTerms } from '../viewmode'
import { RUNNER_VERDICT_LABELS, RUNTIME_TYPES, descriptor, relativeTime, runnerExperienceOf, runtimeCode, runtimeLabel, runtimeLogoClass } from '../runtime'
import type { Agent, AgentGoal, AgentMemory, AgentRelease, AgentTrigger, AgentVersion, ExecutionMode, ExternalApp, MCPBundle, MCPServerRef, MCPToolPolicy, ModelEndpoint, RuntimeFlow, RuntimeProfile, TriggerHealth, UsableAgentServer, Workspace } from '../types'

/** What the chosen way of running has done on this deployment.
 *
 *  Nine are offered and they look equally available, which is only true where
 *  every one has been used. On a deployment that has only ever reasoned, choosing
 *  a code review means being the first — worth knowing before rather than after.
 *
 *  "안 해 봄" is deliberately quiet: most deployments will never use most of
 *  these, so it marks the absence of evidence rather than the choice. */
/** What a trigger has produced lately.
 *
 *  It says nothing when a trigger has produced nothing: that is not a failure —
 *  a weekly schedule simply may not have been due — and the "last fired" line
 *  above already answers whether it runs at all. */
function TriggerRecord({ health, days, fired }: { health?: TriggerHealth; days: number; fired?: string }) {
  if (!health || health.tasks === 0) {
    // Enabled, never fired, and nothing to show: worth saying once, because it is
    // the state where somebody thinks automation is running and it is not.
    if (!fired) return <small className="trigger-record">아직 한 번도 실행되지 않았습니다</small>
    return null
  }
  const failed = health.failed ?? 0
  const tone = failed === 0 ? 'ok' : failed >= health.tasks ? 'bad' : 'warn'
  return <small className={`trigger-record ${tone}`}>
    최근 {days}일 {health.tasks}건{failed > 0 ? ` · 실패 ${failed}건` : ''}
    {failed >= health.tasks && health.lastError ? ` · ${health.lastError.slice(0, 60)}` : ''}
  </small>
}

function RunnerVerdict({ runner }: { runner: string }) {
  const experience = runnerExperienceOf(runner)
  if (!experience) return null
  const missing = experience.missing ?? []
  if (missing.length > 0) return <span className="experience-tag missing"
    title={missing.map((piece) => `${piece.what} → ${piece.where}`).join('\n')}>준비 필요</span>
  if (experience.verdict === 'untried') return null
  return <span className={`experience-tag ${experience.verdict === 'failing' ? 'failed' : 'proven'}`} title={experience.detail}>
    {RUNNER_VERDICT_LABELS[experience.verdict] ?? experience.verdict}
  </span>
}

/** What has to change before the chosen way of running can work here.
 *
 *  Beside the picker rather than in a tooltip: this is the moment somebody is
 *  deciding, and a choice that cannot run is worth interrupting for. It says
 *  nothing when nothing is missing. */
function RunnerMissing({ runner }: { runner: string }) {
  const missing = runnerExperienceOf(runner)?.missing ?? []
  if (missing.length === 0) return null
  return <div className="notice missing-notice">
    <strong>이 실행 방식은 아직 이 배포에서 쓸 수 없습니다.</strong>
    <ul>{missing.map((piece) => <li key={piece.what}>{piece.what} <em>{piece.where}</em></li>)}</ul>
  </div>
}

export function Agents({runtimeOnly=false}:{runtimeOnly?:boolean}) {
  const t = useTerms()
  const [agents,setAgents]=useState<Agent[]|null>(null)
  const [selectedId,setSelectedId]=useState<string|null>(null)
  const [busy,setBusy]=useState<string|null>(null)
  const [error,setError]=useState('')
  const [editing,setEditing]=useState<Agent|null>(null)
  const [goalFor,setGoalFor]=useState<Agent|null>(null)
  const [versionsFor,setVersionsFor]=useState<Agent|null>(null)
  const [removing,setRemoving]=useState<Agent|null>(null)
  const [removeBusy,setRemoveBusy]=useState(false)
  const [removeError,setRemoveError]=useState('')
  const [query,setQuery]=useState('')
  const [runtimeFilter,setRuntimeFilter]=useState('')
  const [notice,setNotice]=useState('')
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
  // The definition leaves as a file so it can be reviewed in a repository and
  // applied to another cluster; the browser download is the whole point, so the
  // response is fetched and saved rather than navigated to.
  const exportAgent=async(agent:Agent)=>{
    setError(''); setNotice('')
    try {
      const yaml=await api.text(`/api/v1/agents/${agent.id}/export`)
      const url=URL.createObjectURL(new Blob([yaml],{type:'application/yaml'}))
      const link=document.createElement('a')
      link.href=url; link.download=`${agent.name}.yaml`
      document.body.appendChild(link); link.click(); link.remove()
      URL.revokeObjectURL(url)
      setNotice(`${agent.name} 정의를 YAML로 내려받았습니다.`)
    } catch(err) {
      setError(err instanceof Error?err.message:'정의를 내보내지 못했습니다.')
    }
  }
  const importAgent=async(file:File)=>{
    setError(''); setNotice('')
    try {
      const result=await api.postText<{mode?:string;agent?:Agent;execution?:string}>('/api/v1/agents/import',await file.text())
      /* A definition is not a Goal. Creating an agent from a file leaves it on the
         default runner, approval mode and limits, whatever the agent it came from
         was set to do — the part somebody moving a definition between clusters is
         least likely to notice. */
      setNotice(`${result.agent?.name ?? file.name} 정의를 ${result.mode==='updated'?'갱신':'생성'}했습니다.`
        +(result.execution==='defaults'
          ? ' 실행 설정(Goal)은 정의에 포함되지 않으므로 기본값(prose 실행기·기본 승인 모드·기본 한도)으로 시작합니다 — 목표 화면에서 확인해 주세요.'
          : result.execution==='kept' ? ' 기존 실행 설정(Goal)은 그대로 유지했습니다.' : ''))
      await refresh()
    } catch(err) {
      setError(err instanceof Error?err.message:'정의를 가져오지 못했습니다.')
    }
  }
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
    <PageHeader eyebrow={runtimeOnly?'런타임 제어':t('agentsEyebrow')} title={runtimeOnly?t('runtimeTitle'):t('agents')} description={runtimeOnly?'사용자 전용 Kubernetes 런타임의 수명주기와 상태를 관리합니다.':t('agentsHint')} actions={<><label className="button ghost import-agent"><Upload size={16}/>정의 가져오기<input type="file" accept=".yaml,.yml,application/yaml,text/yaml" onChange={(e)=>{const file=e.target.files?.[0]; e.target.value=''; if(file) void importAgent(file)}}/></label><Link className="button primary" to="/catalog"><Plus size={17}/>새 {t('agentSingular')}</Link></>}/>
    {error&&<ErrorBanner message={error} onClose={()=>setError('')}/>}
    {notice&&<div className="notice-banner">{notice}</div>}
    {scoped.length>0&&<div className="toolbar">
      <div className="search-box"><Search size={17}/><input value={query} onChange={(e)=>setQuery(e.target.value)} placeholder="이름, 설명, Pod 이름 검색" aria-label="에이전트 검색"/></div>
      <div className="filter-chips">
        <button className={runtimeFilter===''?'selected':''} onClick={()=>setRuntimeFilter('')}>전체 {scoped.length}</button>
        {present.map((type)=><button key={type} className={runtimeFilter===type?'selected':''} onClick={()=>setRuntimeFilter(type)}>{runtimeLabel(type)} {scoped.filter((a)=>a.runtimeType===type).length}</button>)}
      </div>
    </div>}
    {scoped.length>0&&visible.length===0?<div className="empty-compact">검색 조건에 맞는 {t('agentSingular')}이(가) 없습니다.</div>:visible.length===0?<Empty icon={<Bot/>} title="아직 에이전트가 없습니다" description="카탈로그에서 검증된 템플릿을 선택해 첫 에이전트를 만들어 보세요." action={<Link className="button primary" to="/catalog">카탈로그 열기</Link>}/>:<section className="table-panel"><div className="table-wrap custom-scroll"><table><thead><tr><th>{t('agentSingular')}</th><th>{t('runtimeType')}</th><th>상태</th><th>Pod / 노드</th><th>마지막 변경</th><th aria-label="작업"/></tr></thead><tbody>{visible.map(agent=><tr key={agent.id}><td><button className="agent-cell" onClick={()=>setSelectedId(agent.id)}><div className={runtimeLogoClass(agent.runtimeType)}>{runtimeCode(agent.runtimeType)}</div><div><strong>{agent.name}</strong><span>정의 v{agent.version}</span></div></button></td><td><span className="runtime-name">{runtimeLabel(agent.runtimeType)}</span></td><td><StatusBadge status={agent.runtime?.status??'stopped'}/>{agent.runtime?.failureReason&&<span className="blocked-tag" title={agent.runtime.failureReason}><ShieldAlert size={13}/>막힘</span>}{agent.runtime?.warmUntil&&<span className="warm-tag" title={`예열 유지 ${new Date(agent.runtime.warmUntil).toLocaleString('ko-KR')}까지`}>예열</span>}</td><td><div className="mono-stack"><code>{agent.runtime?.podName||'—'}</code><small>{agent.runtime?.nodeName||'할당 전'}</small></div></td><td><span title={new Date(agent.updatedAt).toLocaleString('ko-KR')}>{relativeTime(agent.updatedAt)}</span></td><td><div className="row-actions">{!agent.runtime||['stopped','failed','crashed'].includes(agent.runtime.status)?<button title="시작" disabled={!!busy} onClick={()=>void act(agent,agent.runtime?'start':'spawn')}><Play size={16}/></button>:<button title="중지" disabled={!!busy} onClick={()=>void act(agent,'stop')}><CircleStop size={16}/></button>}<button title="목표 · 자동화" onClick={()=>setGoalFor(agent)}><Target size={15}/></button><button title="버전 · 운영 승격" onClick={()=>setVersionsFor(agent)}><History size={15}/></button><button title="정의 내보내기 (YAML)" onClick={()=>void exportAgent(agent)}><Download size={15}/></button><button title="수정" onClick={()=>setEditing(agent)}><Pencil size={15}/></button><button className="danger" title="삭제" disabled={!!busy} onClick={()=>{setRemoveError('');setRemoving(agent)}}><Trash2 size={15}/></button><button title="상세" onClick={()=>setSelectedId(agent.id)}><MoreHorizontal size={18}/></button></div></td></tr>)}</tbody></table></div></section>}
    {selected&&<AgentDrawer agent={selected} close={()=>setSelectedId(null)} action={act} busy={!!busy} edit={()=>setEditing(selected)} remove={()=>{setRemoveError('');setRemoving(selected)}}/>}
    {editing&&<AgentEditDrawer agent={editing} close={()=>setEditing(null)} done={()=>{setEditing(null);void refresh()}}/>}
    {goalFor&&<GoalDrawer agent={goalFor} close={()=>setGoalFor(null)}/>}
    {versionsFor&&<VersionsDrawer agent={versionsFor} close={()=>setVersionsFor(null)} done={()=>{setVersionsFor(null);void refresh()}}/>}
    {removing&&<ConfirmDialog
      title="에이전트를 삭제할까요?"
      /* The dialog used to name the definition and the Pod, which is the part
         somebody pictures. The rest of it — every task, run transcript, artifact,
         memory, evaluation and version — went too, unmentioned. */
      message={<><strong>{removing.name}</strong> 정의와 실행 중인 런타임(Pod, Service, NetworkPolicy)이 함께 삭제됩니다.<br/>
        <strong>이 에이전트의 기록도 모두 지워집니다</strong> — 작업과 실행 기록·전사, 산출물, 기억, 사전검증 결과, 정의 버전, Trigger. 되돌릴 수 없습니다.<br/>
        연결된 작업공간 볼륨은 보존됩니다. 진행 중인 작업이 있으면 삭제는 거절됩니다.</>}
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
  // A custom runtime has no adapter, so its definition carries how it starts.
  const [command,setCommand]=useState(()=>{
    const spec=agent.spec as {customCommand?:string[]}|undefined
    return (spec?.customCommand??[]).join('\n')
  })
  const [customPort,setCustomPort]=useState(()=>{
    const spec=agent.spec as {customPort?:number}|undefined
    return spec?.customPort?String(spec.customPort):''
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
      const result=await api.put<{warning?:string}>(`/api/v1/agents/${agent.id}`,{name,description,runtimeProfileId:profile,modelEndpointId:model,mcpBundleId:bundle,workspaceId:workspace,systemPrompt:prompt,
        customCommand:agent.runtimeType==='custom'?command.split('\n').map(part=>part.trim()).filter(Boolean):undefined,
        customPort:agent.runtimeType==='custom'&&customPort?Number(customPort):undefined})
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
      {agent.runtimeType==='custom'&&<>
        <label><span>시작 명령 <b>*</b></span><textarea rows={4} value={command} onChange={e=>setCommand(e.target.value)} placeholder={'/usr/local/bin/my-agent\nserve\n--port\n9000'}/>
          <small>한 줄에 하나씩 입력하세요. 쉘을 거치지 않으므로 따옴표나 파이프는 쓸 수 없습니다.</small></label>
        <label><span>서비스 포트</span><input type="number" min={1} max={65535} value={customPort} onChange={e=>setCustomPort(e.target.value)} placeholder="4096"/>
          <small>비워 두면 기본 포트를 사용합니다. 런타임이 실제로 듣는 포트와 같아야 준비 상태가 됩니다.</small></label>
      </>}
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
    {runtime?.failureReason&&<ErrorBanner message={runtime.failureReason}/>}
    <section className="detail-section"><h4>런타임</h4><dl className="detail-list"><div><dt>인스턴스 ID</dt><dd><code>{runtime?.id??'아직 생성되지 않음'}</code></dd></div><div><dt>목표 상태</dt><dd>{runtime?.desiredState??'stopped'}</dd></div><div><dt>CRD</dt><dd><code>{runtime?.crdName||'—'}</code></dd></div><div><dt>Pod</dt><dd><code>{runtime?.podName||'—'}</code></dd></div><div><dt>노드</dt><dd>{runtime?.nodeName||'할당 전'}</dd></div><div><dt>재시작 횟수</dt><dd>{runtime?.restartCount??0}</dd></div></dl></section>
    <section className="detail-section"><h4>작업공간 도구</h4><div className="tool-links">{runtime&&<button disabled={!ready||busy} onClick={()=>void launch()}><Activity size={17}/>작업공간 열기<ExternalLink size={14}/></button>}<button disabled={!runtime} onClick={()=>void loadLogs()}><FileText size={17}/>Logs</button></div></section>
    {logError&&<ErrorBanner message={logError}/>} {logs&&<section className="detail-section"><h4>런타임 로그</h4><pre className="runtime-log-preview custom-scroll">{logs}</pre></section>}
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
  // What each trigger has actually produced. Without it a schedule that fires
  // every hour into a task that fails every hour reads exactly like one that
  // works.
  const [triggerHealth,setTriggerHealth]=useState<Record<string,TriggerHealth>>({})
  const [triggerWindow,setTriggerWindow]=useState(7)
  const [memories,setMemories]=useState<AgentMemory[]>([])
  const [busy,setBusy]=useState(false)
  const [error,setError]=useState('')
  const [notice,setNotice]=useState('')
  const [addingTrigger,setAddingTrigger]=useState(false)
  const [policies,setPolicies]=useState<MCPToolPolicy[]>([])
  const [mcpServers,setMcpServers]=useState<MCPServerRef[]>([])
  const [flows,setFlows]=useState<RuntimeFlow[]>([])
  const [apps,setApps]=useState<ExternalApp[]>([])
  const [servers,setServers]=useState<UsableAgentServer[]>([])
  const [flowError,setFlowError]=useState('')
  const [flowBusy,setFlowBusy]=useState(false)
  // What this agent's runtime can be handed a task with, straight from the
  // platform's descriptor rather than from a list kept here — adding a backend
  // adds a name there, and this form follows.
  const runners=descriptor(agent.runtimeType).runners??[]

  const load=useCallback(async()=>{
    try{
      const [goalResult,triggerResult,memoryResult,policyResult,appResult,serverResult]=await Promise.all([
        api.get<{goal:AgentGoal;executionMode:ExecutionMode}>(`/api/v1/agents/${agent.id}/goal`),
        api.get<{items?:AgentTrigger[];health?:Record<string,TriggerHealth>;windowDays?:number}>(`/api/v1/agents/${agent.id}/triggers`),
        api.get<{items?:AgentMemory[]}>(`/api/v1/agents/${agent.id}/memories`),
        api.get<{items?:MCPToolPolicy[];servers?:MCPServerRef[]}>(`/api/v1/agents/${agent.id}/mcp-policies`),
        // Offered to every agent: an external application runs somewhere the
        // platform does not, so it does not depend on this agent's runtime.
        api.get<{items?:ExternalApp[]}>('/api/v1/external-apps'),
        // Same reasoning as external apps: an agent server runs the work on a
        // machine this deployment registered rather than starts, so what it can
        // do has nothing to do with this agent's runtime.
        api.get<{items?:UsableAgentServer[]}>('/api/v1/agent-servers'),
      ])
      setGoal(goalResult.goal); setMode(goalResult.executionMode); setTriggers(triggerResult.items??[]); setTriggerHealth(triggerResult.health??{}); setTriggerWindow(triggerResult.windowDays??7); setMemories(memoryResult.items??[])
      setPolicies(policyResult.items??[]); setMcpServers(policyResult.servers??[]); setApps(appResult.items??[])
      setServers(serverResult.items??[])
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
  const savePolicy=async(serverId:string,mode:'allow'|'deny',tools:string[],approvalTools:string[])=>{
    setError(''); setNotice('')
    try{
      const result=await api.put<{warning?:string}>(`/api/v1/agents/${agent.id}/mcp-policies`,{serverId,mode,tools,approvalTools})
      setNotice(result.warning??'도구 정책을 저장했습니다.')
      await load()
    }catch(e){ setError(e instanceof Error?e.message:'도구 정책을 저장하지 못했습니다.') }
  }
  const removePolicy=async(id:string)=>{
    try{ await api.delete(`/api/v1/mcp-policies/${id}`); await load() }
    catch(e){ setError(e instanceof Error?e.message:'도구 정책을 삭제하지 못했습니다.') }
  }
  // The flows live in the runtime, so the list is only available while it runs.
  // A stopped runtime is not an error here: the id can still be typed in.
  const loadFlows=async()=>{
    setFlowBusy(true); setFlowError('')
    try{
      const result=await api.get<{items?:RuntimeFlow[];truncated?:boolean}>(`/api/v1/agents/${agent.id}/flows`)
      setFlows(result.items??[])
      if(result.truncated) setFlowError('흐름이 많아 앞의 200개만 표시합니다. 목록에 없으면 흐름 ID를 직접 입력해 주세요.')
      else if(!result.items?.length) setFlowError('이 Runtime에 저장된 흐름이 없습니다. Runtime을 열어 흐름을 먼저 만들어 주세요.')
    }catch(e){ setFlowError(e instanceof Error?e.message:'흐름 목록을 가져오지 못했습니다.') }
    finally{ setFlowBusy(false) }
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

      {(runners.length>0||apps.length>0||servers.length>0)&&<fieldset><legend>실행 방식</legend>
        <label><span>자동 실행이 하는 일 <RunnerVerdict runner={goal.runner??'prose'}/></span>
          <select value={goal.runner??'prose'} onChange={(e)=>update({runner:e.target.value as AgentGoal['runner']})}>
            <option value="prose">추론 루프 — 모델과 대화하며 진행하고, 런타임 작업은 사람에게 인계</option>
            {runners.includes('flow')&&<option value="flow">흐름 실행 — Runtime에 저장된 Langflow 흐름을 실행하고 결과를 기록</option>}
            {runners.includes('cli')&&<option value="cli">에이전트 실행 — Runtime의 코딩 에이전트가 작업공간에서 직접 수행</option>}
            {runners.includes('acp')&&<option value="acp">ACP 실행 — 같은 에이전트와 프로토콜로 대화하며, 도구 요청마다 플랫폼이 답합니다</option>}
            {runners.includes('investigate')&&<option value="investigate">조사 실행 — 관측 데이터를 조회해 근본 원인을 찾고, 근거를 실행 기록에 남깁니다</option>}
            {runners.includes('rpc')&&<option value="rpc">프로토콜 실행 — 에이전트를 띄워 두고 대화하며 진행합니다</option>}
            {runners.includes('orca')&&<option value="orca">실행 패브릭 — 여러 코딩 에이전트를 각자 격리된 작업 사본에서 조정합니다</option>}
            {runners.includes('review')&&<option value="review">코드 리뷰 — 변경분을 리뷰해 파일·줄 단위 지적을 남기고, 정한 심각도 이상이면 작업을 실패로 판정합니다</option>}
            {servers.length>0&&<option value="agentserver">에이전트 서버 — 등록한 서버가 자기 샌드박스에서 수행하고, 정책·모델·완료 판정은 플랫폼이 갖습니다</option>}
            {apps.length>0&&<option value="dify">외부 앱 실행 — 사내에 이미 있는 앱(Dify 등)에 작업을 맡김</option>}
          </select>
          <small>{goal.runner==='agentserver'
            ?'작업을 등록한 에이전트 서버에 맡깁니다. 이 배포는 컨테이너를 띄우지 않습니다 — 대신 어떤 모델을 부를지, 무엇을 승인할지, 작업이 끝났는지는 그대로 플랫폼이 정합니다. 모델 호출은 이 배포의 게이트웨이로만 나가고, 서버가 보고한 사용량이 그대로 계량됩니다. 아래 자율성에서 사람 승인을 요구하면, 서버는 행동하기 전에 멈추고 이 플랫폼의 승인 대기열이 답합니다.'
            :goal.runner==='dify'
            ?'플랫폼은 이 앱을 실행하지 않고 호출만 합니다. Runtime을 띄우지 않으며, 앱 안에서 일어나는 모델 호출은 그 배포의 몫이라 플랫폼 토큰 집계에 넣지 않습니다.'
            :goal.runner==='flow'
            ?'작업 입력이 흐름의 입력으로 들어가고, 흐름의 출력이 실행 기록과 완료 판정에 사용됩니다. 흐름 안에서 일어나는 모델 호출은 플랫폼이 계량하지 않습니다.'
            :goal.runner==='cli'
            ?'런타임의 에이전트를 헤드리스로 실행합니다. 최대 단계·도구 호출·실행 시간이 에이전트 자체 예산으로 전달되고, 토큰 사용량은 실제 값이 기록됩니다.'
            :goal.runner==='investigate'
            ?'런타임의 조사 에이전트가 알림·메트릭·로그를 직접 조회합니다. 조회 하나하나가 실행 기록의 단계로 남아 결론의 근거를 나중에 확인할 수 있고, 토큰 사용량은 실제 값이 기록됩니다.'
            :goal.runner==='rpc'
            ?'에이전트를 프로세스로 띄워 두고 프로토콜로 대화합니다. 명령을 받아들였는지, 어떤 턴이 끝났는지, 언제 완전히 끝났는지가 에이전트 자신의 이벤트로 기록되고, 토큰 사용량도 그 값이 그대로 계량됩니다. 최대 실행 시간이 대화의 제한 시간으로 전달됩니다.'
            :goal.runner==='orca'
            ?'작업을 실행 패브릭에 넘깁니다. 에이전트마다 별도의 git 작업 사본이 만들어져 서로의 변경을 밟지 않고, 어느 작업이 어느 사본에서 돌았는지가 실행 기록에 남습니다. 정책·쿼터·감사·완료 판정은 그대로 AgentHub가 갖습니다. 패브릭이 시작할 수 있는 에이전트는 그 이미지가 가진 것뿐이고, 토큰은 그 에이전트들의 런타임에서 계량됩니다.'
            :goal.runner==='review'
            ?'런타임의 리뷰 엔진이 변경분을 읽습니다. 어떤 파일을 볼지와 어떤 규칙을 적용할지는 엔진이 정하고, 판단이 필요한 부분만 모델에게 묻습니다. 결과는 요약이 아니라 파일·줄·심각도를 가진 지적 목록으로 남고, 토큰 사용량은 실제 값이 기록됩니다.'
            :goal.runner==='acp'
            ?'같은 에이전트를 Agent Client Protocol로 실행합니다. 에이전트가 도구를 쓰기 전마다 묻고, 승인 모드에 따라 플랫폼이 답한 내용이 실행 기록에 남습니다. 아래 자율성에서 승인을 요구하면 읽기가 아닌 요청은 사람에게 넘어갑니다. 토큰 사용량은 에이전트가 알려줄 때만 집계합니다.'
            :'기존 방식입니다. 파일 편집이나 명령 실행이 필요하면 사람에게 인계합니다.'}</small>
        </label>
        <RunnerMissing runner={goal.runner??'prose'}/>
        {goal.runner==='dify'&&<>
          <label><span>외부 앱</span>
            <select value={goal.externalAppId??''} onChange={(e)=>update({externalAppId:e.target.value})}>
              <option value="">선택하세요</option>
              {apps.map((app)=><option key={app.id} value={app.id}>{app.name} · {app.appKind==='chat'?'Chat':'Workflow'}</option>)}
            </select>
            <small>관리자 ▸ 리소스 ▸ 외부 앱에서 등록한 앱입니다. API 키는 플랫폼이 보관하고 화면에 다시 보여주지 않습니다.</small>
          </label>
          {apps.find((app)=>app.id===goal.externalAppId)?.appKind!=='chat'&&<label><span>입력 변수 이름 (선택)</span>
            <input value={goal.externalInputKey??''} onChange={(e)=>update({externalInputKey:e.target.value.trim()})} placeholder="예) query"/>
            <small>Workflow 앱은 이름 붙은 입력을 받습니다. 앱에서 정의한 변수 이름을 넣으세요. 비우면 <code>input</code> 을 사용합니다.</small>
          </label>}
        </>}
        {goal.runner==='agentserver'&&<>
          <label><span>어디서 실행할지</span>
            <select value={goal.agentServerId?`server:${goal.agentServerId}`:goal.agentServerZone?`zone:${goal.agentServerZone}`:''}
              onChange={(e)=>{const v=e.target.value
                update(v.startsWith('server:')?{agentServerId:v.slice(7),agentServerZone:''}
                  :v.startsWith('zone:')?{agentServerId:'',agentServerZone:v.slice(5)}
                  :{agentServerId:'',agentServerZone:''})}}>
              <option value="">선택하세요</option>
              {[...new Set(servers.map((s)=>s.networkZone).filter(Boolean))].map((zone)=>
                <option key={`zone:${zone}`} value={`zone:${zone}`}>{zone} 네트워크 — 그 안에서 쓸 수 있는 서버를 고릅니다</option>)}
              {servers.map((server)=><option key={server.id} value={`server:${server.id}`}>
                {server.name}{server.networkZone?` · ${server.networkZone}`:''}{server.health==='healthy'?'':' · 연결 확인 필요'}</option>)}
            </select>
            <small>네트워크를 고르면 그 안에서 <b>연결이 확인된 서버</b>를 골라 보냅니다. 서버를 직접 고르면 그 서버에서만 실행되고, 그 서버가 꺼져 있으면 다른 곳으로 돌리지 않고 실패합니다 — 지정에는 이유가 있다고 보기 때문입니다.</small>
          </label>
          <label><span>작업 디렉터리 (선택)</span>
            <input value={goal.agentServerDir??''} onChange={(e)=>update({agentServerDir:e.target.value.trim()})} placeholder="workspace/project"/>
            <small>서버 작업 공간 안의 상대 경로입니다. 비우면 <code>workspace/project</code> 를 씁니다.</small>
          </label>
        </>}
        {goal.runner==='orca'&&<>
          <label><span>동시에 붙일 에이전트</span>
            <input value={goal.orcaAgents??''} onChange={(e)=>update({orcaAgents:e.target.value})} placeholder="예) claude,codex"/>
            <small>쉼표로 나열하면 각 에이전트가 <b>자기 작업 사본</b>에서 같은 일을 합니다. 비워 두면 작업과 작업 사본만 만들고 워커는 붙이지 않습니다 — 런타임 터미널에서 직접 붙일 수 있습니다.</small>
          </label>
          <div className="notice">
            워커를 띄우려면 그 에이전트 계정이 <b>런타임 호스트에 등록</b>돼 있어야 합니다. 벤더 로그인이라 플랫폼이 대신 할 수 없습니다 —
            런타임 터미널에서 <code>orca account add --agent claude</code> 처럼 먼저 로그인해 주세요. 등록돼 있지 않으면 작업이 그 사실을 알려 주며 실패합니다.
          </div>
        </>}
        {goal.runner==='review'&&<>
          <label><span>리뷰 대상</span>
            <select value={goal.reviewMode??'workspace'} onChange={(e)=>update({reviewMode:e.target.value as AgentGoal['reviewMode']})}>
              <option value="workspace">작업공간의 변경분 — 아직 커밋하지 않은 것까지</option>
              <option value="range">브랜치 비교 — 두 브랜치 사이의 변경분</option>
              <option value="commit">커밋 하나</option>
              <option value="scan">저장소 전체 점검 — diff 없이 파일을 그대로</option>
              <option value="trigger">트리거가 지정 — PR/MR 웹훅이 알려주는 변경분</option>
            </select>
            <small>브랜치 비교는 두 브랜치의 공통 조상부터 비교하므로, 그 사이에 base에 들어간 남의 변경은 지적하지 않습니다.</small>
          </label>
          {goal.reviewMode==='range'&&<div className="field-row">
            <label><span>기준 브랜치</span><input value={goal.reviewBaseRef??''} onChange={(e)=>update({reviewBaseRef:e.target.value.trim()})} placeholder="main"/></label>
            <label><span>대상 브랜치</span><input value={goal.reviewHeadRef??''} onChange={(e)=>update({reviewHeadRef:e.target.value.trim()})} placeholder="feature/login"/></label>
          </div>}
          {goal.reviewMode==='commit'&&<label><span>커밋</span>
            <input value={goal.reviewHeadRef??''} onChange={(e)=>update({reviewHeadRef:e.target.value.trim()})} placeholder="예) 9f2c1ab 또는 태그"/>
          </label>}
          {goal.reviewMode==='trigger'&&<div className="notice">
            리뷰할 브랜치를 이 화면에서 정하지 않습니다. 이 에이전트의 <b>웹훅 트리거</b> 로 들어오는 요청 본문이 알려 줍니다 —
            <code>{'{"from":"main","to":"feature/x"}'}</code> 또는 <code>{'{"commit":"<sha>"}'}</code> (<code>base</code>/<code>head</code>/<code>sha</code> 도 받습니다).
            그래서 PR 하나마다 에이전트를 만들 필요가 없습니다. 본문에 대상이 없으면 그 작업은 무엇이 빠졌는지 알려 주며 실패합니다.
          </div>}
          {goal.reviewMode==='scan'&&<label><span>경로 (선택)</span>
            <input value={goal.reviewPath??''} onChange={(e)=>update({reviewPath:e.target.value.trim()})} placeholder="예) internal/auth"/>
            <small>비우면 저장소 전체를 봅니다. 전체 점검은 변경분 리뷰보다 훨씬 많은 토큰을 씁니다.</small>
          </label>}
          <label><span>제외할 경로 (선택)</span>
            <input value={goal.reviewExclude??''} onChange={(e)=>update({reviewExclude:e.target.value})} placeholder="예) **/generated/*,**/testdata/*"/>
            <small>생성된 파일과 고정 데이터에 모델을 쓰지 않기 위한 것입니다. 쉼표로 구분합니다.</small>
          </label>
          <label><span>작업을 실패로 볼 심각도</span>
            <select value={goal.reviewFailOn??''} onChange={(e)=>update({reviewFailOn:e.target.value as AgentGoal['reviewFailOn']})}>
              <option value="">없음 — 지적만 남기고 작업은 성공으로 끝냅니다</option>
              <option value="critical">심각 이상</option>
              <option value="high">높음 이상</option>
              <option value="medium">보통 이상</option>
              <option value="low">낮음 이상 — 지적이 하나라도 있으면 실패</option>
            </select>
            <small>여기서 고른 것이 품질 게이트입니다. 비워 두면 리뷰는 보고만 하고 아무것도 막지 않습니다.</small>
          </label>
        </>}
        {goal.runner==='investigate'&&<>
          <label><span>셸 실행 허용</span>
            <select value={goal.approvalMode??'default'} onChange={(e)=>update({approvalMode:e.target.value as AgentGoal['approvalMode']})}>
              <option value="default">조회만 — 셸 명령은 거절합니다(권장)</option>
              <option value="auto">auto — 조사 중 셸 명령도 허용</option>
              <option value="yolo">yolo — 조사 중 셸 명령도 허용</option>
            </select>
            <small>{['auto','yolo'].includes(goal.approvalMode??'default')
              ?'조사 중 셸 명령이 확인 없이 실행됩니다. 읽기만으로 부족한 조사에 필요할 수 있지만, 작업공간과 네트워크 정책으로 범위를 먼저 좁히세요.'
              :'메트릭·로그·알림 조회만 하고 셸 명령은 거절합니다. 조사는 대개 이것으로 충분합니다.'}</small>
          </label>
          {goal.approvalRequired&&['auto','yolo'].includes(goal.approvalMode??'default')&&<div className="info-box"><ShieldAlert size={17}/><div><strong>같이 켤 수 없습니다</strong><p>사람 승인을 요구하는 목표에서는 셸 실행을 자동 허용할 수 없습니다. 조회만으로 낮추거나 아래 자율성의 승인 요구를 끄세요.</p></div></div>}
        </>}
        {(goal.runner==='cli'||goal.runner==='acp')&&<>
          <label><span>에이전트 승인 모드</span>
            <select value={goal.approvalMode??'default'} onChange={(e)=>update({approvalMode:e.target.value as AgentGoal['approvalMode']})}>
              <option value="plan">plan — 계획만 세우고 바꾸지 않음</option>
              <option value="default">default — 변경 전마다 확인(무인 실행에서는 사실상 멈춤)</option>
              <option value="auto-edit">auto-edit — 파일 편집만 자동 승인</option>
              <option value="auto">auto — 편집과 안전한 명령을 자동 승인</option>
              <option value="yolo">yolo — 모두 자동 승인</option>
            </select>
            <small>{goal.runner==='acp'
              ?goal.approvalMode==='yolo'
                ?'에이전트의 모든 요청을 플랫폼이 승인합니다. 무엇을 승인했는지는 실행 기록에 남지만, 막지는 않습니다.'
                :goal.approvalMode==='auto-edit'
                ?'읽기와 작업공간 파일 편집은 승인하고, 명령 실행·삭제는 거절합니다.'
                :'읽기·검색만 승인하고 바꾸는 요청은 모두 거절합니다. ACP에서는 사람 대신 플랫폼이 답하므로, 확인을 요구하는 모드는 곧 거절입니다.'
              :goal.approvalMode==='yolo'
              ?'모든 도구 실행이 확인 없이 진행됩니다. 작업공간과 MCP 도구 정책으로 범위를 먼저 좁히세요.'
              :goal.approvalMode==='plan'
              ?'파일을 바꾸지 않고 계획만 남깁니다. 무인 실행 결과를 먼저 검토하고 싶을 때 적합합니다.'
              :'무인 실행은 사람이 없으므로, 확인을 요구하는 모드에서는 변경이 필요한 순간 진행되지 않을 수 있습니다.'}</small>
          </label>
          {goal.approvalRequired&&goal.approvalMode==='yolo'&&<div className="info-box"><ShieldAlert size={17}/><div><strong>같이 켤 수 없습니다</strong><p>사람 승인을 요구하는 목표에서는 yolo 를 쓸 수 없습니다. 승인 모드를 낮추거나 아래 자율성의 승인 요구를 끄세요.</p></div></div>}
          {goal.runner==='acp'&&goal.approvalRequired&&<div className="info-box"><ShieldAlert size={17}/><div><strong>사람이 답합니다</strong><p>이 목표는 승인을 요구하므로, 읽기가 아닌 도구 요청은 승인 모드와 상관없이 <b>사람에게 전달</b>됩니다. 에이전트는 답을 기다리고, 실행 시간 안에 답이 없으면 거절로 처리합니다.</p></div></div>}
          {goal.runner==='acp'&&!goal.approvalRequired&&descriptor(agent.runtimeType).coarseToolKinds&&!['auto','yolo'].includes(goal.approvalMode??'default')&&<div className="info-box"><ShieldAlert size={17}/><div><strong>이 모드에서는 거의 아무것도 못 합니다</strong><p>{runtimeLabel(agent.runtimeType)}는 도구의 종류를 <code>other</code> 로 알려주기 때문에, 종류로 판단하는 이 모드에서는 대부분의 요청이 거절됩니다. 무인 실행에는 <b>auto</b> 를 고르세요 — 무엇을 승인했는지는 그대로 기록에 남습니다.</p></div></div>}
          {goal.runner==='acp'&&<>
            <label><span>항상 거절할 도구</span>
              <textarea rows={3} value={(goal.toolPolicy?.deny??[]).join('\n')} onChange={(e)=>update({toolPolicy:{...goal.toolPolicy,deny:e.target.value.split('\n')}})} placeholder={'rm -rf\ngit push\ncurl'}/>
              <small>한 줄에 하나씩. 도구 이름에 이 문구가 들어 있으면 <b>승인 모드와 상관없이</b> 거절합니다. yolo 로도 풀리지 않습니다.</small>
            </label>
            <label><span>묻지 않고 허용할 도구</span>
              <textarea rows={3} value={(goal.toolPolicy?.allow??[]).join('\n')} onChange={(e)=>update({toolPolicy:{...goal.toolPolicy,allow:e.target.value.split('\n')}})} placeholder={'npm test\npytest\ngit status'}/>
              <small>거절 목록에 걸리지 않은 것만 확인합니다. {descriptor(agent.runtimeType).coarseToolKinds?`${runtimeLabel(agent.runtimeType)}처럼 도구 종류를 알려주지 않는 에이전트는 이 목록이 유일하게 세밀한 통제 수단입니다.`:'종류로 판단하기 전에 이름으로 먼저 판단합니다.'}</small>
            </label>
          </>}
        </>}
        {goal.runner==='flow'&&<>
          <label><span>실행할 흐름</span>
            <div className="inline-row">
              <select value={goal.flowId??''} onChange={(e)=>update({flowId:e.target.value})}>
                <option value="">{goal.flowId?`직접 입력: ${goal.flowId}`:'선택하세요'}</option>
                {flows.map((flow)=><option key={flow.id} value={flow.id}>{flow.name}</option>)}
                {goal.flowId&&!flows.some((flow)=>flow.id===goal.flowId)&&<option value={goal.flowId}>{goal.flowId}</option>}
              </select>
              <button type="button" className="button ghost" onClick={()=>void loadFlows()} disabled={flowBusy}>
                <RefreshCw size={14}/>{flowBusy?'불러오는 중…':'목록 불러오기'}</button>
            </div>
            <small>목록은 실행 중인 Runtime에서 읽어옵니다. Runtime이 꺼져 있으면 흐름 ID를 그대로 입력해도 됩니다.</small>
          </label>
          {flowError&&<div className="info-box"><ShieldAlert size={17}/><div><strong>흐름 목록</strong><p>{flowError}</p></div></div>}
          <label><span>흐름 ID 직접 입력</span>
            <input value={goal.flowId??''} onChange={(e)=>update({flowId:e.target.value.trim()})} placeholder="예) ed5d9610-0fd6-465a-b05c-646557c66178"/>
          </label>
          <label><span>출력 컴포넌트 (선택)</span>
            <input value={goal.flowOutputComponent??''} onChange={(e)=>update({flowOutputComponent:e.target.value.trim()})} placeholder="예) ChatOutput-yK0AU"/>
            <small>출력이 여러 개인 흐름에서 어느 결과를 답으로 볼지 지정합니다. 비우면 마지막 출력을 사용합니다.</small>
          </label>
        </>}
      </fieldset>}

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
        <label className="toggle-row"><span>재시도 시 완료한 단계에서 이어서 실행</span>
          <input type="checkbox" checked={goal.resumeFromCheckpoint!==false} onChange={(e)=>update({resumeFromCheckpoint:e.target.checked})}/><i/></label>
        <div className="info-note">켜 두면 재시도와 승인 후 재개가 이미 끝낸 단계를 다시 하지 않고 이어서 진행합니다. 같은 단계를 다시 실행해도 문제가 없어야 하는 에이전트만 끄세요.</div>
      </fieldset>

      <fieldset><legend>Runtime 예열</legend>
        <div className="form-grid">
          <label><span>사전 예열 (초)</span>
            <input type="number" min={0} max={1800} value={goal.warmupSeconds} onChange={(e)=>update({warmupSeconds:Number(e.target.value)})}/>
            <small>예약 실행 시각 이만큼 전에 Runtime을 미리 띄웁니다. 0이면 사용하지 않습니다.</small></label>
          <label><span>작업 후 유지 (초)</span>
            <input type="number" min={0} max={3600} value={goal.keepWarmSeconds} onChange={(e)=>update({keepWarmSeconds:Number(e.target.value)})}/>
            <small>작업이 끝나도 이만큼 켜 둡니다. &apos;작업 후 Runtime 중지&apos;가 켜져 있어야 의미가 있습니다.</small></label>
        </div>
        <div className="info-note">예열된 Runtime은 사람이 시작·재시작하거나 작업공간을 열면 그 즉시 사용자 소유가 되어, 예열 시간이 지나도 자동으로 꺼지지 않습니다.</div>
      </fieldset>

      <fieldset><legend>실행 한도</legend>
        <label><span>토큰 예산 (최근 30일)</span>
          <input type="number" min={0} step={1000} value={goal.tokenBudget??0} onChange={(e)=>update({tokenBudget:Number(e.target.value)})}/>
          <small>0이면 사용자 예산만 적용됩니다. 이 값을 넘기면 이 에이전트의 새 작업은 실행되지 않고 실패로 기록됩니다.</small>
        </label>
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
              <small>{trigger.type==='cron'?`${trigger.schedule} · ${trigger.timezone}`
                :trigger.type==='webhook'?(trigger.hasSecret?'Webhook · 서명 설정됨':'Webhook · 서명 미설정')
                :trigger.type==='event'?`이벤트 · ${EVENT_TYPES.find(([value])=>value===trigger.eventType)?.[1]??trigger.eventType}`
                :'수동'}</small>
              {trigger.nextFireAt&&<small>다음 실행 {new Date(trigger.nextFireAt).toLocaleString('ko-KR')}</small>}
              <TriggerRecord health={triggerHealth[trigger.id]} days={triggerWindow} fired={trigger.lastFiredAt}/>
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

    {mcpServers.length>0&&<section className="detail-section"><h4>MCP 도구 정책</h4>
      <div className="tool-links">{mcpServers.map((server)=>{
        const policy=policies.find((item)=>item.serverId===server.id)
        return <McpPolicyRow key={server.id} server={server} policy={policy}
          save={(mode,tools,approvalTools)=>void savePolicy(server.id,mode,tools,approvalTools)}
          remove={policy?()=>void removePolicy(policy.id):undefined}/>
      })}</div>
      <small>정책이 있는 서버는 Pod 안의 게이트웨이를 통해서만 호출되며, 자격 증명도 에이전트가 아닌 게이트웨이가 보관합니다. <b>승인 필요 도구</b>로 지정하면 게이트웨이가 호출을 붙잡아 두고 검토자가 승인할 때까지 실행하지 않습니다 — 에이전트가 승인을 요청하지 않아도 마찬가지입니다. 정책 변경은 Runtime 재시작 후 적용됩니다.</small>
    </section>}

    {addingTrigger&&<TriggerDrawer agent={agent} close={()=>setAddingTrigger(false)} done={()=>{setAddingTrigger(false);void load()}}/>}
  </Drawer>
}

/** One MCP server's tool policy. Tools are edited as a comma separated list,
 *  which is how an operator reads them out of the server's documentation. */
function McpPolicyRow({server,policy,save,remove}:{server:MCPServerRef;policy?:MCPToolPolicy;save:(mode:'allow'|'deny',tools:string[],approvalTools:string[])=>void;remove?:()=>void}) {
  const [mode,setMode]=useState<'allow'|'deny'>(policy?.mode==='deny'?'deny':'allow')
  const [tools,setTools]=useState((policy?.tools??[]).join(', '))
  const [approval,setApproval]=useState((policy?.approvalTools??[]).join(', '))
  const parse=(value:string)=>value.split(',').map((tool)=>tool.trim()).filter(Boolean)
  const parsed=parse(tools), parsedApproval=parse(approval)
  const dirty=mode!==(policy?.mode??'allow')
    ||parsed.join(',')!==(policy?.tools??[]).join(',')
    ||parsedApproval.join(',')!==(policy?.approvalTools??[]).join(',')
  return <div className="policy-row">
    <div className="policy-head">
      <strong>{server.name}</strong>
      {policy
        ? <span className={`policy-tag ${policy.mode}`}>{policy.mode==='allow'?'허용 목록':'차단 목록'} {policy.tools.length}개</span>
        : <span className="policy-tag none">정책 없음 · 모든 도구 허용</span>}
      {policy&&policy.approvalTools?.length>0&&<span className="policy-tag approval">승인 필요 {policy.approvalTools.length}개</span>}
    </div>
    <div className="policy-edit">
      <select value={mode} onChange={(e)=>setMode(e.target.value as 'allow'|'deny')}>
        <option value="allow">이 도구만 허용</option>
        <option value="deny">이 도구만 차단</option>
      </select>
      <input value={tools} onChange={(e)=>setTools(e.target.value)} placeholder="resolve-library-id, get-library-docs"/>
      <button type="button" className="button ghost" disabled={!dirty} onClick={()=>save(mode,parsed,parsedApproval)}>저장</button>
      {remove&&<button type="button" className="danger" title="정책 삭제" onClick={remove}><Trash2 size={15}/></button>}
    </div>
    <div className="policy-approval">
      <label><ShieldAlert size={14}/>승인 필요 도구</label>
      <input value={approval} onChange={(e)=>setApproval(e.target.value)} placeholder="delete_branch, run_migration"/>
    </div>
    {mode==='allow'&&parsed.length===0&&<small className="policy-warn">허용 목록이 비어 있으면 이 서버의 모든 도구가 차단됩니다.</small>}
    {parsedApproval.length>0&&<small>이 도구를 호출하면 Pod 안의 게이트웨이가 호출을 붙잡고 검토자의 승인을 기다립니다. 승인 전에는 실행되지 않고, 거절되면 에이전트에게 거절 사유가 전달됩니다.</small>}
  </div>
}

/** Platform events an agent can subscribe to, labelled for the console. */
const EVENT_TYPES: [string, string][] = [
  ['task.completed','작업 완료'],
  ['task.failed','작업 실패'],
  ['task.dead_lettered','작업 재시도 소진'],
  ['task.handoff','작업 인계'],
  ['approval.decided','승인 처리됨'],
  ['runtime.failed','런타임 장애'],
  ['artifact.created','산출물 생성'],
]

function TriggerDrawer({agent,close,done}:{agent:Agent;close:()=>void;done:()=>void}) {
  const [name,setName]=useState('')
  const [type,setType]=useState<'cron'|'webhook'|'event'>('cron')
  const [eventType,setEventType]=useState(EVENT_TYPES[0][0])
  const [eventFilter,setEventFilter]=useState('')
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
    // Parsing here keeps a typo out of the request, where it would come back as
    // a generic 400 with no hint about which field was wrong.
    let filter: unknown = undefined
    if(type==='event'&&eventFilter.trim()){
      try{ filter=JSON.parse(eventFilter) }
      catch{ setError('이벤트 필터는 JSON 객체여야 합니다. 예) {"agentId":"…"}'); setBusy(false); return }
    }
    try{
      await api.post(`/api/v1/agents/${agent.id}/triggers`,{name,type,enabled,schedule:type==='cron'?schedule:'',timezone,taskTitle,taskInput,priority:'normal',
        secret:type==='webhook'?secret:'',eventType:type==='event'?eventType:'',eventFilter:filter})
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
        <select value={type} onChange={(e)=>setType(e.target.value as 'cron'|'webhook'|'event')}>
          <option value="cron">예약 (cron)</option>
          <option value="webhook">Webhook</option>
          <option value="event">플랫폼 이벤트</option>
        </select>
      </label>
      {type==='cron'&&<><label><span>일정 <b>*</b></span><input required value={schedule} onChange={(e)=>setSchedule(e.target.value)} placeholder="0 8 * * *"/>
          <small>분 시 일 월 요일. 예) <code>0 8 * * *</code> 매일 08:00, <code>*/30 * * * *</code> 30분마다</small></label>
        <label><span>시간대</span><input value={timezone} onChange={(e)=>setTimezone(e.target.value)} placeholder="Asia/Seoul"/></label></>}
      {type==='webhook'&&<label><span>Webhook 서명 키 <b>*</b></span><input required type="password" autoComplete="new-password" value={secret} onChange={(e)=>setSecret(e.target.value)}/>
          <small>호출 측은 본문 HMAC-SHA256을 <code>X-AgentHub-Signature</code> 헤더로 보내야 합니다.</small></label>}
      {type==='event'&&<><label><span>이벤트 종류 <b>*</b></span>
          <select value={eventType} onChange={(e)=>setEventType(e.target.value)}>
            {EVENT_TYPES.map(([value,label])=><option key={value} value={value}>{label} ({value})</option>)}
          </select></label>
        <label><span>이벤트 필터</span><input value={eventFilter} onChange={(e)=>setEventFilter(e.target.value)} placeholder={'{"agentId":"…"}'}/>
          <small>비워 두면 이 종류의 모든 이벤트에 반응합니다. JSON 객체를 넣으면 해당 값이 일치하는 이벤트에만 반응합니다.</small></label>
        <div className="info-note">이 Trigger가 만든 작업이 다시 같은 Trigger를 깨우지는 않습니다.</div></>}
      <label><span>작업 제목</span><input maxLength={200} value={taskTitle} onChange={(e)=>setTaskTitle(e.target.value)} placeholder="비워 두면 Trigger 이름을 사용합니다"/></label>
      <label><span>작업 내용</span><textarea rows={4} value={taskInput} onChange={(e)=>setTaskInput(e.target.value)} placeholder="이 Trigger가 만들 작업의 지시 내용"/></label>
      <label className="toggle-row"><span>활성화</span><input type="checkbox" checked={enabled} onChange={(e)=>setEnabled(e.target.checked)}/><i/></label>
    </form>
  </Drawer>
}

/**
 * Version history and the promotion gate.
 *
 * Saving used to be the whole release process: the next scheduled run executed
 * whatever the definition said at that moment. This drawer is where an edit
 * becomes a release — see what changed, check what the pre-flight evaluation
 * said about that exact version, promote it, or put the previous one back.
 */
function VersionsDrawer({agent,close,done}:{agent:Agent;close:()=>void;done:()=>void}) {
  const { user } = useAuth()
  const [items,setItems]=useState<AgentVersion[]|null>(null)
  const [release,setRelease]=useState<AgentRelease|null>(null)
  const [error,setError]=useState('')
  const [notice,setNotice]=useState('')
  const [busy,setBusy]=useState(false)
  // Forcing is a separate step on purpose: skipping the evaluation asks for a
  // written reason, and that reason is what the audit log and the next reader see.
  const [forcing,setForcing]=useState<AgentVersion|null>(null)
  const [reason,setReason]=useState('')
  const load=useCallback(async()=>{
    try {
      const result=await api.get<{items:AgentVersion[];release:AgentRelease}>(`/api/v1/agents/${agent.id}/versions`)
      setItems(result.items); setRelease(result.release)
    } catch(e) { setError(e instanceof Error?e.message:'버전 목록을 불러오지 못했습니다.') }
  },[agent.id])
  useEffect(()=>{void load()},[load])
  const call=async(run:()=>Promise<string>)=>{
    setBusy(true); setError(''); setNotice('')
    try { setNotice(await run()); await load() }
    catch(e) { setError(e instanceof Error?e.message:'요청을 처리하지 못했습니다.') }
    finally { setBusy(false) }
  }
  const promote=(version:number,force=false,note='')=>call(async()=>{
    const result=await api.post<{releasedTasks?:number}>(`/api/v1/agents/${agent.id}/promote`,{version,force,note})
    setForcing(null); setReason('')
    // A gate holds tasks rather than failing them, so a promotion usually starts
    // work that was already waiting. Saying so is the difference between "saved"
    // and "the nightly run you were missing is running now".
    return `v${version}을(를) 운영 승격했습니다.${result.releasedTasks?` 대기 중이던 작업 ${result.releasedTasks}건이 다시 실행됩니다.`:''}`
  })
  const restore=(version:number)=>call(async()=>{
    const result=await api.post<{warning?:string;releasedTasks?:number}>(`/api/v1/agents/${agent.id}/versions/${version}/restore`)
    done()
    return `${result.warning||`v${version} 정의를 복원했습니다.`}${result.releasedTasks?` 대기 중이던 작업 ${result.releasedTasks}건이 다시 실행됩니다.`:''}`
  })
  const gate=(required:boolean)=>call(async()=>{
    await api.post(`/api/v1/agents/${agent.id}/promote`,{requirePromotion:required})
    return required?'운영 승격을 거쳐야만 실행되도록 설정했습니다.':'운영 승격 없이도 실행되도록 설정했습니다.'
  })
  return <Drawer title={`${agent.name} · 버전`} subtitle={`현재 정의 v${release?.currentVersion??agent.version}${release?.promotedVersion?` · 운영 승격 v${release.promotedVersion}`:' · 승격된 버전 없음'}`} close={close}
    footer={<button className="button ghost" onClick={close}>닫기</button>}>
    <GuidePanel id="agent-versions" title="버전과 운영 승격은 이렇게 씁니다" steps={[
      {title:'1. 저장하면 버전이 남습니다',body:'에이전트를 수정하거나 YAML을 가져올 때마다 그 시점의 정의가 버전으로 보존됩니다.'},
      {title:'2. 사전검증을 실행합니다',body:<>사전검증 결과는 실행한 시점의 버전에 붙습니다. <Link to="/evaluation">사전검증</Link>에서 실행하세요.</>},
      {title:'3. 통과한 버전을 승격합니다',body:'승격된 버전이 "운영에서 검증된 정의"입니다. 통과 결과가 없으면 관리자가 사유를 적어야만 승격할 수 있습니다.'},
      {title:'4. 게이트를 켜면 승격본만 실행됩니다',body:'게이트를 켠 뒤 현재 정의가 승격본과 다르면 새 작업은 즉시 거절되고, 이미 예약돼 있던 작업은 실패가 아니라 “차단됨”으로 보류됩니다(정책이 막은 작업도 같은 상태이며, 이유는 작업 옆에 표시됩니다). 승격하거나 이전 버전을 복원하면 보류된 작업이 자동으로 다시 실행됩니다.'},
    ]}/>
    {error&&<ErrorBanner message={error} onClose={()=>setError('')}/>}
    {notice&&<div className="notice-banner">{notice}</div>}
    {release&&<section className="detail-section"><h4>운영 승격</h4>
      <label className="toggle-row"><span>승격된 정의만 실행</span><input type="checkbox" checked={release.requirePromotion} disabled={busy} onChange={(e)=>void gate(e.target.checked)}/><i/></label>
      <p className="field-hint">{release.requirePromotion?'현재 정의가 승격본과 다르면 새 작업은 거절되고, 예약된 작업은 승격될 때까지 보류됩니다.':'끄면 저장한 최신 정의가 곧바로 실행됩니다.'}</p>
      <dl className="detail-list"><div><dt>운영 승격</dt><dd>{release.promotedVersion?`v${release.promotedVersion}`:'없음'}</dd></div><div><dt>승격 시각</dt><dd>{release.promotedAt?new Date(release.promotedAt).toLocaleString('ko-KR'):'—'}</dd></div><div><dt>사유</dt><dd>{release.promotionNote||'—'}</dd></div></dl>
      {release.requirePromotion&&release.promotedVersion!==release.currentVersion&&<ErrorBanner message={`현재 정의 v${release.currentVersion}는 승격되지 않아 지금은 작업이 실행되지 않습니다.`}/>}
    </section>}
    {!items?<Loading/>:items.length===0?<Empty icon={<History/>} title="저장된 버전이 없습니다" description="에이전트를 한 번 수정하면 그 시점의 정의가 버전으로 남습니다."/>:
    <section className="detail-section"><h4>버전 기록 {items.length}개</h4><ul className="version-list">{items.map((item)=>{
      const passed=item.evaluationStatus==='passed'
      return <li key={item.version} className={item.promoted?'promoted':''}>
        <div className="version-head"><strong>v{item.version}</strong>{item.promoted&&<span className="version-tag promoted"><ShieldCheck size={13}/>운영 승격</span>}{item.version===release?.currentVersion&&<span className="version-tag current">현재 정의</span>}{item.evaluationStatus?<span className={`version-tag ${passed?'passed':'failed'}`}>사전검증 {passed?'통과':'실패'} {item.evaluationScore}점</span>:<span className="version-tag muted">사전검증 없음</span>}</div>
        <p>{item.note||'변경 사유 없음'}</p>
        <small>{new Date(item.createdAt).toLocaleString('ko-KR')} · {item.name}</small>
        <div className="version-actions">
          {!item.promoted&&<button disabled={busy} onClick={()=>{ if(passed) void promote(item.version); else { setForcing(item); setReason('') } }}><ShieldCheck size={14}/>운영 승격</button>}
          {item.version!==release?.currentVersion&&<button disabled={busy} onClick={()=>void restore(item.version)}><RotateCcw size={14}/>이 정의로 복원</button>}
        </div>
      </li>
    })}</ul></section>}
    {forcing&&<ConfirmDialog title={`사전검증 없이 v${forcing.version}을 승격할까요?`}
      message={user.role==='admin'
        ?<><strong>v{forcing.version}</strong>에는 통과한 사전검증 결과가 없습니다. 사유를 적으면 승격되며, 사유는 감사 로그와 승격 기록에 남습니다.<label className="confirm-field"><span>승격 사유</span><input value={reason} onChange={(e)=>setReason(e.target.value)} placeholder="예: 긴급 문구 수정, 사전검증 환경 점검 중"/></label></>
        :<><strong>v{forcing.version}</strong>에는 통과한 사전검증 결과가 없습니다. 사전검증을 먼저 실행하거나, 관리자에게 승격을 요청하세요.</>}
      busy={busy} confirmLabel="승격" disableConfirm={user.role!=='admin'||!reason.trim()}
      onConfirm={()=>void promote(forcing.version,true,reason.trim())} onCancel={()=>setForcing(null)}/>}
  </Drawer>
}
