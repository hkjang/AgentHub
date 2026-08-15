import { useEffect, useState } from 'react'
import { Check, ClipboardCheck, X } from 'lucide-react'
import { api } from '../api'
import { Empty, ErrorBanner, Loading, PageHeader, StatusBadge } from '../components/UI'

type Approval={id:string;requesterName:string;resourceType:string;resourceId:string;action:string;reason:string;status:string;createdAt:string}

export function Reviews(){
  const [items,setItems]=useState<Approval[]>(),[error,setError]=useState('')
  const load=()=>api.get<{items:Approval[]}>('/api/v1/approvals').then(v=>setItems(v.items)).catch(e=>setError(e.message));useEffect(()=>{void load()},[])
  const decide=async(id:string,decision:'approve'|'reject')=>{setError('');try{await api.post(`/api/v1/approvals/${id}/${decision}`);await load()}catch(e){setError(e instanceof Error?e.message:'처리하지 못했습니다.')}}
  if(!items)return <Loading/>
  return <div className="page"><PageHeader eyebrow="거버넌스" title="검토 · 승인" description="팀원이 요청한 Agent Runtime과 고위험 Tool 실행을 검토합니다."/>{error&&<ErrorBanner message={error}/>} {items.length===0?<Empty icon={<ClipboardCheck/>} title="검토할 요청이 없습니다" description="새 요청이 도착하면 이곳에서 승인 또는 반려할 수 있습니다."/>:<section className="approval-list">{items.map(item=><article key={item.id}><div><StatusBadge status={item.status}/><h3>{item.action}</h3><p>{item.reason}</p><span>{item.requesterName} · {item.resourceType} · {new Date(item.createdAt).toLocaleString('ko-KR')}</span></div>{item.status==='pending'&&<div><button className="button danger subtle" onClick={()=>void decide(item.id,'reject')}><X size={16}/>반려</button><button className="button primary" onClick={()=>void decide(item.id,'approve')}><Check size={16}/>승인</button></div>}</article>)}</section>}</div>
}
