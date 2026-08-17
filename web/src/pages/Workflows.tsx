import { FormEvent, useEffect, useMemo, useState } from 'react'
import { CheckCircle2, GitBranch, Pencil, Play, Plus, ShieldCheck, Trash2, Workflow as WorkflowIcon, X } from 'lucide-react'
import { api } from '../api'
import { ConfirmDialog, Drawer, Empty, ErrorBanner, Loading, PageHeader, StatusBadge, SuccessBanner } from '../components/UI'
import { statusLabel } from '../components/UI'
import type { Agent, Workflow, WorkflowRunResult } from '../types'

export function Workflows() {
  const [items,setItems] = useState<Workflow[]>()
  const [agents,setAgents] = useState<Agent[]>([])
  const [selected,setSelected] = useState<Workflow|null|undefined>()
  const [error,setError] = useState('')
  const [success,setSuccess] = useState('')
  const [removing,setRemoving] = useState<Workflow|null>(null)
  const [removeBusy,setRemoveBusy] = useState(false)
  const [removeError,setRemoveError] = useState('')
  const [running,setRunning] = useState<Workflow|null>(null)
  const names = useMemo(() => new Map(agents.map((item) => [item.id,item.name])),[agents])
  const load = () => Promise.all([api.get<{items?:Workflow[]}>('/api/v1/workflows').then((v)=>setItems(v.items??[])),api.get<{items?:Agent[]}>('/api/v1/agents').then((v)=>setAgents(v.items??[]))]).catch((e)=>{setItems([]);setError(e instanceof Error?e.message:'워크플로 목록을 불러오지 못했습니다.')})
  useEffect(()=>{void load()},[])
  const remove = async () => {
    if(!removing) return
    setRemoveBusy(true); setRemoveError('')
    try { await api.delete(`/api/v1/workflows/${removing.id}`); setRemoving(null); await load() }
    catch(e){ setRemoveError(e instanceof Error?e.message:'워크플로를 삭제하지 못했습니다.') }
    finally { setRemoveBusy(false) }
  }
  if(!items)return <Loading/>
  const validate = async (id:string) => { try { const result=await api.post<{levels:string[][]}>(`/api/v1/workflows/${id}/validate`);setSuccess(`정책·소유권·순환 검사 통과 · 실행 단계 ${result.levels.length}`) } catch(e) { setError(e instanceof Error?e.message:'검증하지 못했습니다.') } }
  return <div className="page"><PageHeader eyebrow="멀티 에이전트" title="에이전트 워크플로" description="에이전트 간 의존관계를 정의하고 실행 전에 깊이·호출량·순환·병렬 한도를 검증합니다." actions={<button className="button primary" disabled={agents.length===0} onClick={()=>setSelected(null)}><Plus size={16}/>워크플로 만들기</button>}/>{error&&<ErrorBanner message={error} onClose={()=>setError('')}/>} {success&&<SuccessBanner message={success}/>} {items.length===0?<Empty icon={<WorkflowIcon/>} title="워크플로가 없습니다" description="에이전트를 먼저 만든 뒤 순차 또는 병렬 실행 그래프를 구성하세요."/>:<section className="workflow-grid">{items.map((item)=><article key={item.id} onClick={()=>setSelected(item)}><header><div className="list-icon"><GitBranch/></div><StatusBadge status={item.enabled?'active':'disabled'}/></header><h3>{item.name}</h3><p>{item.description||'멀티 에이전트 실행 정의'}</p><div className="workflow-chain">{item.definition.steps.map((step,index)=><span key={step.id}>{index>0&&<i>→</i>}<b>{names.get(step.agentId)??'알 수 없는 에이전트'}</b></span>)}</div><footer><span>{item.mode} · 깊이 {item.maxDepth} · 호출 {item.maxAgentCalls}</span><div className="card-actions"><button className="button primary" disabled={!item.enabled} title={item.enabled?undefined:'비활성화된 Workflow는 실행할 수 없습니다.'} onClick={(event)=>{event.stopPropagation();setRunning(item)}}><Play size={14}/>실행</button><button className="button ghost" onClick={(event)=>{event.stopPropagation();void validate(item.id)}}><ShieldCheck size={14}/>검증</button><button title="수정" onClick={(event)=>{event.stopPropagation();setSelected(item)}}><Pencil size={15}/></button><button className="danger" title="삭제" onClick={(event)=>{event.stopPropagation();setRemoveError('');setRemoving(item)}}><Trash2 size={15}/></button></div></footer></article>)}</section>}{selected!==undefined&&<WorkflowDrawer item={selected} agents={agents} close={()=>setSelected(undefined)} done={()=>{setSelected(undefined);void load()}} error={setError}/>}
    {running&&<RunDrawer item={running} close={()=>setRunning(null)}/>}
    {removing&&<ConfirmDialog title="워크플로를 삭제할까요?" message={<><strong>{removing.name}</strong> 실행 그래프 정의가 삭제됩니다. 연결된 에이전트는 그대로 유지됩니다.</>} busy={removeBusy} error={removeError} onConfirm={()=>void remove()} onCancel={()=>setRemoving(null)}/>}</div>
}

function WorkflowDrawer({item,agents,close,done,error}:{item:Workflow|null;agents:Agent[];close:()=>void;done:()=>void;error:(value:string)=>void}) {
  const initial=item?.definition.steps.map((step)=>step.agentId)??[agents[0]?.id??'']
  const [name,setName]=useState(item?.name??''),[description,setDescription]=useState(item?.description??''),[mode,setMode]=useState(item?.mode??'sequential'),[agentIds,setAgentIds]=useState(initial),[maxDepth,setMaxDepth]=useState(item?.maxDepth??4),[maxAgentCalls,setMaxAgentCalls]=useState(item?.maxAgentCalls??12),[maxToolCalls,setMaxToolCalls]=useState(item?.maxToolCalls??50),[maxDuration,setMaxDuration]=useState(item?.maxDurationSeconds??900),[maxParallel,setMaxParallel]=useState(item?.maxParallelAgents??3),[busy,setBusy]=useState(false)
  const submit=async(event:FormEvent)=>{event.preventDefault();setBusy(true);try{const steps=agentIds.map((agentId,index)=>({id:`step-${index+1}`,agentId,dependsOn:mode==='parallel'||index===0?[]:[`step-${index}`]}));await api.post('/api/v1/workflows',{id:item?.id,name,description,mode,maxDepth,maxAgentCalls,maxToolCalls,maxDurationSeconds:maxDuration,maxParallelAgents:maxParallel,definition:{steps},enabled:item?.enabled??true});done()}catch(e){error(e instanceof Error?e.message:'Workflow를 저장하지 못했습니다.')}finally{setBusy(false)}}
  return <Drawer title={`워크플로 ${item?'수정':'만들기'}`} subtitle="실행 어댑터에 전달하기 전 컨트롤 플레인이 그래프를 검증합니다." close={close} footer={<><button className="button ghost" onClick={close}>취소</button><button className="button primary" form="workflow-form" disabled={busy}>{busy?'저장 중…':'저장 및 검증'}</button></>}><form id="workflow-form" className="drawer-form" onSubmit={submit}><label><span>이름</span><input required maxLength={80} value={name} onChange={(e)=>setName(e.target.value)}/></label><label><span>설명</span><textarea rows={3} value={description} onChange={(e)=>setDescription(e.target.value)}/></label><label><span>실행 방식</span><select value={mode} onChange={(e)=>setMode(e.target.value)}><option value="sequential">Sequential</option><option value="parallel">Parallel</option><option value="router">Router</option><option value="supervisor">Supervisor</option><option value="reviewer">Reviewer</option><option value="consensus">Consensus</option></select></label><fieldset><legend>에이전트 단계</legend><div className="workflow-step-editor">{agentIds.map((agentId,index)=><div key={index}><span>{index+1}</span><select value={agentId} onChange={(e)=>setAgentIds((values)=>values.map((value,i)=>i===index?e.target.value:value))}>{agents.map((agent)=><option value={agent.id} key={agent.id}>{agent.name} · {agent.runtimeType}</option>)}</select><button type="button" aria-label="단계 제거" disabled={agentIds.length===1} onClick={()=>setAgentIds((values)=>values.filter((_,i)=>i!==index))}><X size={15}/></button></div>)}<button type="button" className="button ghost" onClick={()=>setAgentIds((values)=>[...values,agents[0].id])}><Plus size={14}/>단계 추가</button></div></fieldset><fieldset><legend>실행 한도</legend><div className="form-grid"><NumberField label="최대 깊이" value={maxDepth} set={setMaxDepth} min={1} max={20}/><NumberField label="최대 에이전트 호출" value={maxAgentCalls} set={setMaxAgentCalls} min={1} max={100}/><NumberField label="최대 도구 호출" value={maxToolCalls} set={setMaxToolCalls} min={1} max={1000}/><NumberField label="최대 병렬" value={maxParallel} set={setMaxParallel} min={1} max={20}/></div><NumberField label="최대 실행 시간 (초)" value={maxDuration} set={setMaxDuration} min={10} max={86400}/></fieldset><div className="info-box"><CheckCircle2 size={17}/><div><strong>순환 참조 검사</strong><p>저장 시 소유권, 순환 호출, 최대 깊이와 병렬 실행 한도를 서버에서 다시 검증합니다.</p></div></div></form></Drawer>
}

function NumberField({label,value,set,min,max}:{label:string;value:number;set:(value:number)=>void;min:number;max:number}){return <label><span>{label}</span><input required type="number" min={min} max={max} value={value} onChange={(e)=>set(Number(e.target.value))}/></label>}

/** Runs a saved graph and shows the per-step trace the engine returns. */
function RunDrawer({item,close}:{item:Workflow;close:()=>void}) {
  const [input,setInput] = useState('')
  const [busy,setBusy] = useState(false)
  const [error,setError] = useState('')
  const [result,setResult] = useState<WorkflowRunResult|null>(null)
  const submit = async (event:FormEvent) => {
    event.preventDefault(); setBusy(true); setError(''); setResult(null)
    try {
      const response = await api.post<{status:string;result:WorkflowRunResult}>(`/api/v1/workflows/${item.id}/run`,{input})
      setResult(response.result)
      if (response.status !== 'succeeded') setError('일부 단계가 실패했습니다. 아래 실행 기록을 확인하세요.')
    } catch (e) {
      setError(e instanceof Error?e.message:'Workflow를 실행하지 못했습니다.')
    } finally { setBusy(false) }
  }
  return <Drawer title={`${item.name} 실행`} subtitle={`${item.mode} · 최대 ${item.maxAgentCalls}회 호출 · ${item.maxDurationSeconds}초`} close={close}
    footer={<><button className="button ghost" onClick={close}>닫기</button><button className="button primary" form="workflow-run" disabled={busy}><Play size={15}/>{busy?'실행 중…':'실행'}</button></>}>
    <form id="workflow-run" className="drawer-form" onSubmit={submit}>
      {error&&<ErrorBanner message={error} onClose={()=>setError('')}/>}
      <label><span>요청 내용</span><textarea rows={4} value={input} onChange={(e)=>setInput(e.target.value)} placeholder="에이전트들에게 전달할 작업을 입력하세요."/></label>
      <div className="info-box"><WorkflowIcon size={17}/><div><strong>모델 단위 실행</strong><p>각 단계는 해당 에이전트의 시스템 프롬프트와 연결된 모델로 실행되며, 의존 단계의 결과를 입력으로 받습니다.</p></div></div>
    </form>
    {result&&<>
      {result.consensus&&<section className="detail-section"><h4>표결 결과</h4>
        <div className={`consensus-verdict ${result.consensus.total===0?'none':result.consensus.tie?'tie':result.consensus.unanimous?'unanimous':'majority'}`}>
          <strong>{result.consensus.total===0?'합의 없음':result.consensus.tie?'동률':result.consensus.unanimous?'만장일치':'다수결'}</strong>
          {result.consensus.total>0&&<><span>{result.consensus.winner}</span><small>{result.consensus.agreed}/{result.consensus.total}표</small></>}
        </div>
        <div className="tool-links">{result.consensus.votes.map((vote)=>
          <div key={vote.stepId} className="trace-vote">
            <strong>{vote.agentName||vote.stepId}</strong>
            <span className={vote.abstained?'abstained':''}>{vote.abstained?'기권 (VOTE 없음)':vote.choice}</span>
          </div>)}</div>
        <small>각 에이전트는 서로의 답을 보지 않고 같은 질문에 답한 뒤 표를 던집니다. 집계는 플랫폼이 직접 계산합니다.</small>
      </section>}
      {result.supervision&&<section className="detail-section"><h4>감독 결과</h4>
        <div className={`consensus-verdict ${result.supervision.approved?'unanimous':result.supervision.exhausted?'tie':'none'}`}>
          <strong>{result.supervision.approved?'승인':result.supervision.exhausted?'보완 요청 남음':'승인 표시 없음'}</strong>
          <span>{result.supervision.supervisor}</span>
          <small>검토 {result.supervision.rounds.length}회</small>
        </div>
        {result.supervision.rounds.some((round)=>(round.revisions??[]).length>0)&&<div className="tool-links">
          {result.supervision.rounds.flatMap((round)=>(round.revisions??[]).map((revision,index)=>
            <div key={`${round.round}-${index}`} className="trace-vote">
              <strong>{round.round}차 · {revision.agent}</strong>
              <span>{revision.request||'보완 요청'}</span>
            </div>))}
        </div>}
        <small>감독자가 보완을 요청하면 지목된 에이전트만 그 내용을 받아 다시 실행하고, 감독자가 결과를 다시 검토합니다. 검토는 최대 {2}회로 제한됩니다.</small>
      </section>}
      <section className="detail-section"><h4>실행 결과</h4><pre className="runtime-log-preview custom-scroll">{result.output||'출력이 없습니다.'}</pre></section>
      <section className="detail-section"><h4>단계별 기록 · 호출 {result.agentCalls}회</h4>
        <div className="run-trace">{result.steps.map((step)=>
          <article key={step.id} className={step.status}>
            <header><strong>{step.agentName||step.id}</strong><StatusBadge status={step.status}/><small>L{step.level+1} · {step.durationMs}ms</small></header>
            {step.error?<p className="run-error">{step.error}</p>:step.skipped?<p>{statusLabel('skipped')} — 라우터가 선택하지 않은 분기입니다.</p>:<pre className="custom-scroll">{step.output}</pre>}
          </article>)}
        </div>
      </section>
    </>}
  </Drawer>
}
