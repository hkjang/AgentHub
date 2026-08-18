import { FormEvent, useEffect, useMemo, useState } from 'react'
import { CheckCircle2, GitBranch, Pencil, Play, Plus, ShieldCheck, Trash2, Workflow as WorkflowIcon, X } from 'lucide-react'
import { Link } from 'react-router-dom'
import { api } from '../api'
import { ConfirmDialog, Drawer, Empty, ErrorBanner, GuidePanel, Loading, PageHeader, StatusBadge, SuccessBanner } from '../components/UI'
import { statusLabel } from '../components/UI'
import type { Agent, Workflow, WorkflowRunResult } from '../types'

/**
 * What each collaboration mode actually does, in the words of someone deciding
 * which one to pick. The engine's behaviour is the source of these: a bare
 * <select> of English mode names told nobody anything.
 */
const MODES = [
  { value: 'sequential', label: '순차 실행', english: 'Sequential',
    summary: '앞 단계의 답을 다음 단계가 이어받고, 마지막 단계의 결과가 최종 답이 됩니다.',
    when: '조사 → 초안 → 검토처럼 순서가 있는 일',
    sample: '지난주 배포 이후 발생한 오류를 정리하고, 재발 방지 대책을 제안해 주세요.' },
  { value: 'parallel', label: '병렬 실행', english: 'Parallel',
    summary: '모든 단계가 같은 요청을 동시에 받고, 각자의 답을 에이전트 이름과 함께 모아 보여줍니다.',
    when: '같은 자료를 여러 관점에서 동시에 보고 싶을 때',
    sample: '첨부한 설계안을 각자의 전문 분야 관점에서 검토해 주세요.' },
  { value: 'router', label: '라우터', english: 'Router',
    summary: '첫 단계가 어느 분기로 보낼지 고르고, 선택되지 않은 분기는 건너뜁니다.',
    when: '요청 종류에 따라 담당 에이전트가 달라질 때',
    sample: '이 문의를 담당 팀에 배정하고 초안 답변을 작성해 주세요: "결제가 두 번 청구되었습니다."' },
  { value: 'supervisor', label: '감독 검토', english: 'Supervisor',
    summary: '마지막 단계가 앞 결과를 검토해 승인하거나, 지목한 에이전트에게만 보완을 요청합니다(최대 2회).',
    when: '결과 품질을 한 번 걸러야 할 때',
    caution: '모든 단계가 하나의 마지막 단계로 모여야 동작합니다.',
    sample: '분기 실적 보고서를 작성하고, 숫자와 논리를 검토해 승인해 주세요.' },
  { value: 'reviewer', label: '리뷰', english: 'Reviewer',
    summary: '감독 검토와 동일하게 동작합니다. 이전 이름이 남아 있는 것이므로 새로 만들 때는 감독 검토를 고르세요.',
    when: '기존에 만든 리뷰 워크플로를 유지할 때',
    sample: '초안을 검토하고 보완이 필요한 부분을 지적해 주세요.' },
  { value: 'consensus', label: '합의 표결', english: 'Consensus',
    summary: '같은 질문을 각자 독립적으로 받고, 마지막 줄의 VOTE로 표결해 만장일치·다수결·동률을 가립니다.',
    when: '판단이 갈리는 문제를 표로 정할 때',
    caution: '단계 간 연결은 무시되고, 어떤 에이전트도 다른 답을 먼저 보지 않습니다.',
    sample: '이 장애의 원인이 배포인지 인프라인지 판단해 주세요. 근거를 설명한 뒤 결론을 VOTE로 적어 주세요.' },
]
const MODE_BY_VALUE = new Map(MODES.map((mode) => [mode.value, mode]))
export function workflowModeLabel(value: string) { return MODE_BY_VALUE.get(value)?.label ?? value }

/** Ready-made shapes, so a first workflow does not start from an empty form. */
const PRESETS = [
  { name: '조사 → 초안 → 검토', description: '한 에이전트가 조사하고, 다음이 초안을 쓰고, 마지막이 검토합니다.', mode: 'sequential', steps: 3 },
  { name: '다관점 동시 검토', description: '같은 자료를 여러 에이전트가 동시에 각자의 관점으로 검토합니다.', mode: 'parallel', steps: 3 },
  { name: '표결로 결론내기', description: '같은 질문에 각자 답하고 표결로 결론을 정합니다.', mode: 'consensus', steps: 3 },
]

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
  return <div className="page"><PageHeader eyebrow="멀티 에이전트" title="에이전트 워크플로" description="여러 에이전트가 순서대로, 동시에, 또는 서로 검토하며 하나의 요청을 처리하게 만듭니다." actions={<button className="button primary" disabled={agents.length===0} onClick={()=>setSelected(null)}><Plus size={16}/>워크플로 만들기</button>}/>{error&&<ErrorBanner message={error} onClose={()=>setError('')}/>} {success&&<SuccessBanner message={success}/>}
    <GuidePanel id="workflows" title="워크플로는 이렇게 사용합니다" steps={[
      {title:'에이전트를 준비합니다',body:<>단계 하나가 에이전트 하나입니다. 각 단계는 그 에이전트의 지시문과 <b>연결된 모델</b>로 실행되므로, 모델이 연결된 에이전트가 최소 2개 있어야 협업이 됩니다. <Link to="/catalog">에이전트 카탈로그</Link>에서 만들 수 있습니다.</>},
      {title:'실행 방식을 고릅니다',body:<>순차·병렬·라우터·감독 검토·합의 표결 중에서 고릅니다. 만들기 화면의 카드에 각각 무엇을 하고 언제 쓰는지 적혀 있습니다.</>},
      {title:'단계와 한도를 정합니다',body:<>실행 순서대로 에이전트를 넣고 호출·시간 한도를 정합니다. 저장할 때 순환 참조와 한도를 서버가 다시 검사하므로, 잘못된 그래프는 저장되지 않습니다.</>},
      {title:'실행하고 기록을 봅니다',body:<><b>실행</b>을 누르면 이번에 처리할 요청 내용을 입력받습니다. 실행이 끝나면 단계별 출력·소요 시간과 표결·검토 결과를 그대로 보여줍니다.</>},
    ]} footer={<div className="guide-text">워크플로는 에이전트의 런타임 Pod가 아니라 <b>모델 API로 직접 실행</b>됩니다. 런타임을 켜 두지 않아도 실행되고, 작업공간 파일이나 MCP 도구는 사용하지 않습니다. 파일을 다루거나 도구를 써야 하는 일은 <Link to="/tasks">작업 대기열</Link>로 맡기세요.</div>}/>
    {items.length===0?<Empty icon={<WorkflowIcon/>} title="워크플로가 없습니다" description={agents.length<2?'에이전트가 2개 이상 있어야 협업 그래프를 만들 수 있습니다. 먼저 에이전트를 만들어 주세요.':'예시로 시작하면 실행 방식과 단계가 미리 채워집니다.'} action={agents.length===0?<Link className="button primary" to="/catalog"><Plus size={16}/>에이전트 만들기</Link>:<button className="button primary" onClick={()=>setSelected(null)}><Plus size={16}/>워크플로 만들기</button>}/>:<section className="workflow-grid">{items.map((item)=><article key={item.id} onClick={()=>setSelected(item)}><header><div className="list-icon"><GitBranch/></div><StatusBadge status={item.enabled?'active':'disabled'}/></header><h3>{item.name}</h3><p>{item.description||'멀티 에이전트 실행 정의'}</p><div className="workflow-chain">{item.definition.steps.map((step,index)=><span key={step.id}>{index>0&&<i>→</i>}<b>{names.get(step.agentId)??'알 수 없는 에이전트'}</b></span>)}</div><footer><span title={MODE_BY_VALUE.get(item.mode)?.summary}>{workflowModeLabel(item.mode)} · 단계 {item.definition.steps.length} · 최대 호출 {item.maxAgentCalls}회</span><div className="card-actions"><button className="button primary" disabled={!item.enabled} title={item.enabled?undefined:'비활성화된 Workflow는 실행할 수 없습니다.'} onClick={(event)=>{event.stopPropagation();setRunning(item)}}><Play size={14}/>실행</button><button className="button ghost" onClick={(event)=>{event.stopPropagation();void validate(item.id)}}><ShieldCheck size={14}/>검증</button><button title="수정" onClick={(event)=>{event.stopPropagation();setSelected(item)}}><Pencil size={15}/></button><button className="danger" title="삭제" onClick={(event)=>{event.stopPropagation();setRemoveError('');setRemoving(item)}}><Trash2 size={15}/></button></div></footer></article>)}</section>}{selected!==undefined&&<WorkflowDrawer item={selected} agents={agents} close={()=>setSelected(undefined)} done={()=>{setSelected(undefined);void load()}} error={setError}/>}
    {running&&<RunDrawer item={running} close={()=>setRunning(null)}/>}
    {removing&&<ConfirmDialog title="워크플로를 삭제할까요?" message={<><strong>{removing.name}</strong> 실행 그래프 정의가 삭제됩니다. 연결된 에이전트는 그대로 유지됩니다.</>} busy={removeBusy} error={removeError} onConfirm={()=>void remove()} onCancel={()=>setRemoving(null)}/>}</div>
}

function WorkflowDrawer({item,agents,close,done,error}:{item:Workflow|null;agents:Agent[];close:()=>void;done:()=>void;error:(value:string)=>void}) {
  const initial=item?.definition.steps.map((step)=>step.agentId)??[agents[0]?.id??'']
  const [name,setName]=useState(item?.name??''),[description,setDescription]=useState(item?.description??''),[mode,setMode]=useState(item?.mode??'sequential'),[agentIds,setAgentIds]=useState(initial),[maxDepth,setMaxDepth]=useState(item?.maxDepth??4),[maxAgentCalls,setMaxAgentCalls]=useState(item?.maxAgentCalls??12),[maxToolCalls,setMaxToolCalls]=useState(item?.maxToolCalls??50),[maxDuration,setMaxDuration]=useState(item?.maxDurationSeconds??900),[maxParallel,setMaxParallel]=useState(item?.maxParallelAgents??3),[busy,setBusy]=useState(false)
  const submit=async(event:FormEvent)=>{event.preventDefault();setBusy(true);try{const steps=agentIds.map((agentId,index)=>({id:`step-${index+1}`,agentId,dependsOn:mode==='parallel'||index===0?[]:[`step-${index}`]}));await api.post('/api/v1/workflows',{id:item?.id,name,description,mode,maxDepth,maxAgentCalls,maxToolCalls,maxDurationSeconds:maxDuration,maxParallelAgents:maxParallel,definition:{steps},enabled:item?.enabled??true});done()}catch(e){error(e instanceof Error?e.message:'Workflow를 저장하지 못했습니다.')}finally{setBusy(false)}}
  // A preset fills the shape, not the content: name, mode and how many steps.
  const applyPreset=(preset:typeof PRESETS[number])=>{
    setName((current)=>current||preset.name)
    setDescription((current)=>current||preset.description)
    setMode(preset.mode)
    setAgentIds((current)=>{
      const next=[...current]
      while(next.length<preset.steps) next.push(agents[next.length%agents.length]?.id??agents[0]?.id??'')
      return next
    })
  }
  const selected=MODE_BY_VALUE.get(mode)
  // What the step order means depends on the mode, and getting that wrong is the
  // most common way a workflow does something other than what was intended.
  const stepHint=mode==='parallel'
    ? '순서는 의미가 없습니다. 모든 단계가 같은 요청을 동시에 받습니다.'
    : mode==='consensus'
      ? '순서는 의미가 없습니다. 모든 단계가 같은 질문을 받고 각자 표를 던집니다. 2개 이상, 홀수로 두면 동률을 피할 수 있습니다.'
      : mode==='supervisor'||mode==='reviewer'
        ? '맨 마지막 단계가 감독자가 되어 앞 단계들의 결과를 검토합니다. 단계가 2개 이상이어야 검토가 동작합니다.'
        : '위에서 아래 순서로 실행되고, 각 단계는 앞 단계의 결과를 입력으로 받습니다.'
  return <Drawer title={`워크플로 ${item?'수정':'만들기'}`} subtitle="저장할 때 소유권·순환 참조·한도를 서버가 다시 검사합니다." close={close} footer={<><button className="button ghost" onClick={close}>취소</button><button className="button primary" form="workflow-form" disabled={busy}>{busy?'저장 중…':'저장 및 검증'}</button></>}>
    <form id="workflow-form" className="drawer-form" onSubmit={submit}>
      {!item&&agents.length>0&&<fieldset><legend>예시로 시작</legend>
        <div className="example-buttons">{PRESETS.map((preset)=><button type="button" key={preset.name} title={preset.description} onClick={()=>applyPreset(preset)}>{preset.name}</button>)}</div>
        <small>이름·실행 방식·단계 수가 채워집니다. 그대로 저장해도 되고 바꿔도 됩니다.</small>
      </fieldset>}
      <label><span>이름 <b>*</b></span><input required maxLength={80} value={name} onChange={(e)=>setName(e.target.value)} placeholder="예) 장애 보고서 작성 및 검토"/></label>
      <label><span>설명</span><textarea rows={2} value={description} onChange={(e)=>setDescription(e.target.value)} placeholder="이 워크플로가 어떤 요청을 처리하는지 적어 두면 목록에서 구분하기 쉽습니다."/></label>
      <fieldset><legend>실행 방식</legend>
        <div className="choice-grid mode-cards">{MODES.map((option)=><button type="button" key={option.value} className={mode===option.value?'selected':''} onClick={()=>setMode(option.value)}>
          <strong>{option.label} <em>{option.english}</em></strong>
          <span>{option.summary}</span>
          <span>이럴 때 · {option.when}</span>
          {option.caution&&<span>⚠ {option.caution}</span>}
        </button>)}</div>
      </fieldset>
      <fieldset><legend>에이전트 단계</legend><div className="workflow-step-editor">{agentIds.map((agentId,index)=><div key={index}><span>{index+1}</span><select value={agentId} onChange={(e)=>setAgentIds((values)=>values.map((value,i)=>i===index?e.target.value:value))}>{agents.map((agent)=><option value={agent.id} key={agent.id}>{agent.name} · {agent.runtimeType}</option>)}</select><button type="button" aria-label="단계 제거" disabled={agentIds.length===1} onClick={()=>setAgentIds((values)=>values.filter((_,i)=>i!==index))}><X size={15}/></button></div>)}<button type="button" className="button ghost" onClick={()=>setAgentIds((values)=>[...values,agents[0].id])}><Plus size={14}/>단계 추가</button></div>
        <small>{stepHint}</small>
      </fieldset>
      <fieldset><legend>실행 한도</legend>
        <div className="form-grid">
          <NumberField label="최대 깊이" value={maxDepth} set={setMaxDepth} min={1} max={20} hint="이어 붙일 수 있는 단계의 최대 층수"/>
          <NumberField label="최대 에이전트 호출" value={maxAgentCalls} set={setMaxAgentCalls} min={1} max={100} hint="재실행·검토 반복까지 포함한 총 모델 호출 수"/>
          <NumberField label="최대 도구 호출" value={maxToolCalls} set={setMaxToolCalls} min={1} max={1000} hint="한 번의 실행에서 허용할 도구 호출 수"/>
          <NumberField label="최대 병렬" value={maxParallel} set={setMaxParallel} min={1} max={20} hint="동시에 실행할 단계 수"/>
        </div>
        <NumberField label="최대 실행 시간 (초)" value={maxDuration} set={setMaxDuration} min={10} max={86400} hint="이 시간을 넘기면 실행을 중단합니다"/>
      </fieldset>
      <div className="info-box"><CheckCircle2 size={17}/><div><strong>{selected?`${selected.label} 방식으로 저장됩니다`:'저장 시 검증'}</strong><p>{selected?selected.summary:''} 저장할 때 소유권·순환 호출·깊이·병렬 한도를 서버가 다시 검사하므로, 잘못된 그래프는 저장 단계에서 거절됩니다.</p></div></div>
    </form>
  </Drawer>
}

function NumberField({label,value,set,min,max,hint}:{label:string;value:number;set:(value:number)=>void;min:number;max:number;hint?:string}){return <label><span>{label}</span><input required type="number" min={min} max={max} value={value} onChange={(e)=>set(Number(e.target.value))}/>{hint&&<small>{hint}</small>}</label>}

/** stepName turns a step id into the agent's name, which is what the operator
 *  recognises in the routing record. */
function stepName(result:WorkflowRunResult,id:string){
  return result.steps.find((step)=>step.id===id)?.agentName||id
}

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
  const selected = MODE_BY_VALUE.get(item.mode)
  return <Drawer title={`${item.name} 실행`} subtitle={`${workflowModeLabel(item.mode)} · 최대 ${item.maxAgentCalls}회 호출 · ${item.maxDurationSeconds}초`} close={close}
    footer={<><button className="button ghost" onClick={close}>닫기</button><button className="button primary" form="workflow-run" disabled={busy}><Play size={15}/>{busy?'실행 중…':'실행'}</button></>}>
    <form id="workflow-run" className="drawer-form" onSubmit={submit}>
      {error&&<ErrorBanner message={error} onClose={()=>setError('')}/>}
      <label><span>요청 내용 <b>*</b></span>
        <textarea required rows={4} value={input} onChange={(e)=>setInput(e.target.value)} placeholder={selected?.sample??'에이전트들에게 전달할 작업을 입력하세요.'}/>
        <small>{item.mode==='consensus'||item.mode==='parallel'?'모든 단계가 이 내용을 그대로 같이 받습니다.':'첫 단계가 이 내용을 받고, 다음 단계는 앞 단계의 결과를 이어받습니다.'} 에이전트마다 설정된 지시문은 그대로 유지됩니다.</small>
      </label>
      {selected?.sample&&<div className="example-buttons"><button type="button" onClick={()=>setInput(selected.sample)}>예시 요청 넣기</button></div>}
      <div className="info-box"><WorkflowIcon size={17}/><div><strong>{selected?selected.label:'실행'} · 이렇게 진행됩니다</strong><p>{selected?selected.summary:''} 실행은 런타임 Pod가 아니라 각 에이전트에 연결된 모델 API로 이루어지므로, 작업공간 파일이나 MCP 도구는 사용하지 않습니다.</p></div></div>
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
      {result.routing&&<section className="detail-section"><h4>분기 결정</h4>
        <div className={`consensus-verdict ${result.routing.fellBack?'none':'unanimous'}`}>
          <strong>{result.routing.fellBack?'결정 없음 · 전체 실행':`선택: ${result.routing.chosen.map((id)=>stepName(result,id)).join(', ')}`}</strong>
          {result.routing.reason&&<span>{result.routing.reason}</span>}
          <small>{result.routing.validated?'스키마를 지원하는 게이트웨이로 요청했습니다':'스키마 미지원 게이트웨이 · 프롬프트로 요청했습니다'} · 응답은 후보 id로 검증됩니다</small>
        </div>
        <small>{result.routing.fellBack
          ? `라우터가 실행 가능한 분기를 지정하지 않아 모든 분기를 실행했습니다. ${result.routing.note??''}`
          : '라우터는 후보 분기의 id 중에서만 선택할 수 있고, 선택되지 않은 분기는 건너뜁니다.'}</small>
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
      <section className="detail-section"><h4>실행 결과</h4><pre className="runtime-log-preview custom-scroll">{result.output||'출력이 없습니다.'}</pre>
        <small>{item.mode==='sequential'||item.mode==='router'
          ? '마지막으로 성공한 단계의 답이 최종 결과입니다.'
          : item.mode==='supervisor'||item.mode==='reviewer'
            ? '감독자의 결론과 그가 검토한 결과를 합쳐 보여줍니다.'
            : item.mode==='consensus'
              ? '표결로 정해진 결론입니다. 표 분포는 위의 표결 결과에서 확인하세요.'
              : '각 단계의 답을 에이전트 이름과 함께 모아 보여줍니다.'}</small>
      </section>
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
