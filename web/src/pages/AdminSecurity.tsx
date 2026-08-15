import { FormEvent, useEffect, useState } from 'react'
import { LockKeyhole, Network, Plus, ShieldCheck } from 'lucide-react'
import { api } from '../api'
import { Drawer, Empty, ErrorBanner, Loading, PageHeader, StatusBadge } from '../components/UI'

type Kind = 'security' | 'network'
type Profile = { id:string; name:string; description:string; spec:Record<string,unknown>; enabled:boolean }

export function AdminSecurity() {
  const [kind, setKind] = useState<Kind>('security')
  const [security, setSecurity] = useState<Profile[]>()
  const [network, setNetwork] = useState<Profile[]>()
  const [selected, setSelected] = useState<Profile | null | undefined>()
  const [error, setError] = useState('')
  const load = () => Promise.all([
    api.get<{items:Profile[]}>('/api/v1/admin/security-profiles').then((v) => setSecurity(v.items)),
    api.get<{items:Profile[]}>('/api/v1/admin/network-profiles').then((v) => setNetwork(v.items))
  ]).catch((e) => setError(e.message))
  useEffect(() => { void load() }, [])
  if (!security || !network) return <Loading/>
  const items = kind === 'security' ? security : network
  const Icon = kind === 'security' ? ShieldCheck : Network
  return <div className="page">
    <PageHeader eyebrow="관리자" title="보안 · 네트워크 프로파일" description="Kubernetes 세부 옵션을 숨기고 승인된 격리·Egress 정책만 Runtime Template에 제공합니다." actions={<button className="button primary" onClick={() => setSelected(null)}><Plus size={16}/>새 Profile</button>}/>
    {error&&<ErrorBanner message={error}/>}
    <div className="tabs"><button className={kind==='security'?'active':''} onClick={() => setKind('security')}><ShieldCheck size={15}/>Security Profiles <span>{security.length}</span></button><button className={kind==='network'?'active':''} onClick={() => setKind('network')}><Network size={15}/>Network Profiles <span>{network.length}</span></button></div>
    {items.length === 0 ? <Empty icon={<Icon/>} title="프로파일이 없습니다" description="첫 운영 표준 Profile을 등록하세요."/> : <section className="policy-grid">{items.map((item) => <button key={item.id} onClick={() => setSelected(item)}><header><div className="list-icon"><Icon/></div><StatusBadge status={item.enabled?'active':'disabled'}/></header><h3>{item.name}</h3><p>{item.description}</p><div className="policy-facts">{facts(kind,item.spec).map(([name,value]) => <span key={name}><small>{name}</small><strong>{value}</strong></span>)}</div></button>)}</section>}
    {selected !== undefined&&<PolicyDrawer kind={kind} item={selected} close={() => setSelected(undefined)} done={() => {setSelected(undefined);void load()}} error={setError}/>}
  </div>
}

function facts(kind:Kind, spec:Record<string,unknown>):[string,string][] {
  if (kind === 'security') return [['Non-root',yes(spec.runAsNonRoot)],['Privilege',spec.allowPrivilegeEscalation?'Allowed':'Blocked'],['K8s Token',spec.automountServiceAccountToken?'Mounted':'Blocked']]
  const destinations = Array.isArray(spec.allowedDestinations) ? spec.allowedDestinations.length : 0
  return [['Default',spec.defaultDeny?'Deny':'Allow'],['DNS',yes(spec.allowDNS)],['Destinations',String(destinations)]]
}

function PolicyDrawer({kind,item,close,done,error}:{kind:Kind;item:Profile|null;close:()=>void;done:()=>void;error:(value:string)=>void}) {
  const defaults = kind === 'security' ? {runAsNonRoot:true,readOnlyRootFilesystem:false,allowPrivilegeEscalation:false,automountServiceAccountToken:false,seccompProfile:'RuntimeDefault'} : {defaultDeny:true,allowDNS:true,allowedDestinations:[]}
  const [name,setName] = useState(item?.name ?? '')
  const [description,setDescription] = useState(item?.description ?? '')
  const [enabled,setEnabled] = useState(item?.enabled ?? true)
  const [spec,setSpec] = useState<Record<string,unknown>>({...defaults,...item?.spec})
  const [busy,setBusy] = useState(false)
  const update = (key:string,value:unknown) => setSpec((current) => ({...current,[key]:value}))
  const submit = async (event:FormEvent) => { event.preventDefault(); setBusy(true); try { await api.post(`/api/v1/admin/${kind}-profiles`,{id:item?.id,name,description,enabled,spec}); done() } catch (e) { error(e instanceof Error?e.message:'Profile을 저장하지 못했습니다.') } finally { setBusy(false) } }
  return <Drawer title={`${item?'수정':'새로 등록'} · ${kind === 'security'?'Security':'Network'} Profile`} close={close} footer={<><button className="button ghost" onClick={close}>취소</button><button className="button primary" form="policy-form" disabled={busy}>{busy?'저장 중…':'저장'}</button></>}><form id="policy-form" className="drawer-form" onSubmit={submit}><label><span>이름</span><input required maxLength={80} value={name} onChange={(e) => setName(e.target.value)}/></label><label><span>설명</span><textarea rows={3} value={description} onChange={(e) => setDescription(e.target.value)}/></label>{kind === 'security'?<><LockedToggle label="비루트(Non-root) 실행" value/><Toggle label="읽기 전용 루트 파일시스템" value={Boolean(spec.readOnlyRootFilesystem)} change={(v) => update('readOnlyRootFilesystem',v)}/><LockedToggle label="권한 상승 차단" value/><LockedToggle label="ServiceAccount 토큰 차단" value/><label><span>Seccomp 프로파일</span><select value="RuntimeDefault" disabled><option>RuntimeDefault</option></select></label><div className="info-box"><LockKeyhole size={17}/><div><strong>보안 불변조건</strong><p>Non-root, 권한 상승·Kubernetes Token 차단, RuntimeDefault seccomp와 Capability 전체 제거는 완화할 수 없습니다.</p></div></div></>:<><Toggle label="기본 차단(Egress)" value={Boolean(spec.defaultDeny)} change={(v) => update('defaultDeny',v)}/><Toggle label="클러스터 DNS 허용" value={Boolean(spec.allowDNS)} change={(v) => update('allowDNS',v)}/><label><span>허용 Destination</span><textarea rows={6} value={(spec.allowedDestinations as string[]??[]).join('\n')} onChange={(e) => update('allowedDestinations',e.target.value.split('\n').map((v) => v.trim()).filter(Boolean))} placeholder={'10.20.0.0/16:443\n[fd00:20::/64]:8080'}/><small>Kubernetes NetworkPolicy가 지원하는 CIDR:port 형식으로 한 줄에 하나씩 입력합니다.</small></label></>}<Toggle label="프로파일 사용" value={enabled} change={setEnabled}/></form></Drawer>
}

function Toggle({label,value,change}:{label:string;value:boolean;change:(value:boolean)=>void}) { return <label className="toggle-row"><span>{label}</span><input type="checkbox" checked={value} onChange={(e) => change(e.target.checked)}/><i/></label> }
function LockedToggle({label,value}:{label:string;value:boolean}) { return <label className="toggle-row"><span>{label}</span><input type="checkbox" checked={value} disabled readOnly/><i/></label> }
function yes(value:unknown) { return value ? 'Enabled' : 'Disabled' }
