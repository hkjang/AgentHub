import { useEffect, useState } from 'react'
import { Check, ClipboardCheck, X } from 'lucide-react'
import { api } from '../api'
import { Empty, ErrorBanner, Loading, PageHeader, StatusBadge } from '../components/UI'

type ToolPayload={server?:string;tool?:string;arguments?:string}
type Approval={id:string;requesterName:string;resourceType:string;resourceId:string;action:string;reason:string;status:string;createdAt:string;payload?:ToolPayload}

/** A gated tool call cannot be judged from its name alone, so the arguments the
 *  agent wants to send are shown with it. */
function ToolCall({payload}:{payload?:ToolPayload}){
  if(!payload?.tool)return null
  return <div className="approval-tool">
    <span><b>{payload.server}</b> · {payload.tool}</span>
    {payload.arguments&&<pre className="custom-scroll">{payload.arguments}</pre>}
  </div>
}

export function Reviews(){
  const [list,setList]=useState<{items:Approval[];pending:number;hidden:number}>(),[error,setError]=useState('')
  const load=()=>api.get<{items:Approval[];pending:number;hidden:number}>('/api/v1/approvals').then(setList).catch(e=>setError(e.message));useEffect(()=>{void load()},[])
  const decide=async(id:string,decision:'approve'|'reject')=>{setError('');try{await api.post(`/api/v1/approvals/${id}/${decision}`);await load()}catch(e){setError(e instanceof Error?e.message:'처리하지 못했습니다.')}}
  if(!list)return <Loading/>
  const {items}=list
  return <div className="page"><PageHeader eyebrow="거버넌스" title="검토 · 승인" description="팀원이 요청한 Agent Runtime과, 에이전트가 실행하려는 승인 대상 도구 호출을 검토합니다. 승인 전까지 도구는 실행되지 않습니다."/>{error&&<ErrorBanner message={error}/>}{/* The list holds 200. When more are waiting, saying so is the difference between a slow queue and a request nobody will ever see. */}{list.hidden>0&&<div className="notice warning">대기 {list.pending}건 중 {items.length}건만 표시됩니다 — {list.hidden}건은 목록에 들어가지 않았습니다. 오래 기다린 순서로 보여 주므로, 처리하면 나머지가 올라옵니다.</div>} {items.length===0?<Empty icon={<ClipboardCheck/>} title="검토할 요청이 없습니다" description="새 요청이 도착하면 이곳에서 승인 또는 반려할 수 있습니다."/>:<section className="approval-list">{items.map(item=><article key={item.id}><div><StatusBadge status={item.status}/><h3>{item.action}</h3><p>{item.reason}</p>{item.resourceType==='tool'&&<ToolCall payload={item.payload}/>}<span>{item.requesterName} · {item.resourceType} · {new Date(item.createdAt).toLocaleString('ko-KR')}</span></div>{item.status==='pending'&&<div><button className="button danger subtle" onClick={()=>void decide(item.id,'reject')}><X size={16}/>반려</button><button className="button primary" onClick={()=>void decide(item.id,'approve')}><Check size={16}/>승인</button></div>}</article>)}</section>}</div>
}
