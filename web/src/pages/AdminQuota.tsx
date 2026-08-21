import { FormEvent, useEffect, useState } from 'react'
import { Building2, Plus, Trash2, UserCog } from 'lucide-react'
import { api } from '../api'
import { Drawer, Empty, ErrorBanner, Loading, PageHeader } from '../components/UI'
import type { Department, EffectiveQuota, Limits, ManagedUser, UserQuota } from '../types'
import { LIMIT_FIELDS, limitSummary, quotaSource } from '../quota'

type Tab = 'departments' | 'people'

export function AdminQuota() {
  const [tab, setTab] = useState<Tab>('departments')
  const [departments, setDepartments] = useState<Department[]>()
  const [users, setUsers] = useState<ManagedUser[]>()
  const [overrides, setOverrides] = useState<UserQuota[]>()
  const [department, setDepartment] = useState<Department | null | undefined>()
  const [person, setPerson] = useState<ManagedUser | undefined>()
  const [error, setError] = useState('')
  const load = () => Promise.all([
    api.get<{items:Department[]}>('/api/v1/admin/departments').then((v) => setDepartments(v.items)),
    api.get<{items:ManagedUser[]}>('/api/v1/admin/users').then((v) => setUsers(v.items)),
    api.get<{items:UserQuota[]}>('/api/v1/admin/user-quotas').then((v) => setOverrides(v.items))
  ]).catch((e) => setError(e.message))
  useEffect(() => { void load() }, [])
  if (!departments || !users || !overrides) return <Loading/>
  const remove = async (item:Department) => {
    if (!confirm(`${item.name} 부서를 삭제할까요? 구성원은 플랫폼 기본 Quota로 돌아갑니다.`)) return
    try { await api.delete(`/api/v1/admin/departments/${item.id}`); await load() } catch (e) { setError(e instanceof Error?e.message:'부서를 삭제하지 못했습니다.') }
  }
  return <div className="page">
    <PageHeader eyebrow="관리자 · 거버넌스" title="부서 · 개인 Quota" description="플랫폼 기본값 위에 부서 한도와 개인 예외를 얹습니다. 비워 둔 항목은 상위 설정을 그대로 따릅니다."
      actions={tab==='departments'?<button className="button primary" onClick={() => setDepartment(null)}><Plus size={16}/>새 부서</button>:undefined}/>
    {error&&<ErrorBanner message={error}/>}
    <div className="tabs">
      <button className={tab==='departments'?'active':''} onClick={() => setTab('departments')}><Building2 size={15}/>부서 <span>{departments.length}</span></button>
      <button className={tab==='people'?'active':''} onClick={() => setTab('people')}><UserCog size={15}/>개인 <span>{users.length}</span></button>
    </div>
    {tab==='departments'
      ? departments.length===0
        ? <Empty icon={<Building2/>} title="부서가 없습니다" description="부서를 만들면 구성원 1인 기본 한도와 부서 전체 총량을 함께 정할 수 있습니다."/>
        : <section className="quota-grid">{departments.map((item) => <article key={item.id} className="quota-card">
            <header><div className="list-icon"><Building2/></div><div><h3>{item.name}</h3><p>{item.description||'설명 없음'}</p></div>
              <button className="icon-button" onClick={() => remove(item)} aria-label={`${item.name} 삭제`}><Trash2 size={16}/></button></header>
            <div className="quota-facts">
              <span><small>구성원</small><strong>{item.members}명</strong></span>
              <span><small>1인 기본</small><strong>{limitSummary(item.quota.perMember)}</strong></span>
              <span><small>부서 총량</small><strong>{limitSummary(item.quota.total)}</strong></span>
            </div>
            <Usage limits={item.quota.total} held={item.held}/>
            <button className="button ghost" onClick={() => setDepartment(item)}>한도 수정</button>
          </article>)}</section>
      : <section className="table-panel"><div className="table-wrap custom-scroll"><table>
          <thead><tr><th>사용자</th><th>부서</th><th>개인 예외</th><th>메모</th><th/></tr></thead>
          <tbody>{users.map((item) => { const override = overrides.find((v) => v.ownerId===item.id); return <tr key={item.id}>
            <td><div className="user-cell"><div className="avatar">{item.displayName.slice(0,1)}</div><div><strong>{item.displayName}</strong><span>{item.email||item.username}</span></div></div></td>
            <td>{departments.find((v) => v.id===item.departmentId)?.name||<span className="muted-cell">없음</span>}</td>
            <td>{override?limitSummary(override.quota):<span className="muted-cell">부서·플랫폼 설정을 따름</span>}</td>
            <td className="muted-cell">{override?.note||'—'}</td>
            <td><button className="icon-button" onClick={() => setPerson(item)} aria-label={`${item.displayName} Quota`}><UserCog size={17}/></button></td>
          </tr> })}</tbody>
        </table></div></section>}
    {department!==undefined&&<DepartmentDrawer item={department} close={() => setDepartment(undefined)} done={() => {setDepartment(undefined);void load()}} error={setError}/>}
    {person&&<PersonDrawer person={person} departments={departments} override={overrides.find((v) => v.ownerId===person.id)} close={() => setPerson(undefined)} done={() => {setPerson(undefined);void load()}} error={setError}/>}
  </div>
}

// Usage draws what a limit is already holding. A number on its own does not say
// whether a department is about to run out; the bar next to it does.
function Usage({limits,held}:{limits:Limits;held:{runtimes:number;cpuMillis:number;memoryMb:number;storageGb:number}}) {
  const rows:[string,number,number][] = [['Runtime',held.runtimes,limits.maxRuntimes??0],['CPU',held.cpuMillis,limits.maxCpuMillis??0],['Memory',held.memoryMb,limits.maxMemoryMb??0],['Storage',held.storageGb,limits.maxStorageGb??0]]
  const active = rows.filter(([,,limit]) => limit>0)
  if (active.length===0) return <p className="quota-unlimited">총량 제한이 없습니다.</p>
  return <div className="quota-usage">{active.map(([name,used,limit]) => <div key={name}>
    <span>{name}<b>{used} / {limit}</b></span>
    <i><u style={{width:`${Math.min(100,Math.round((used/limit)*100))}%`}} className={used>=limit?'full':used>=limit*0.8?'warn':''}/></i>
  </div>)}</div>
}

function DepartmentDrawer({item,close,done,error}:{item:Department|null;close:()=>void;done:()=>void;error:(v:string)=>void}) {
  const [name,setName] = useState(item?.name ?? '')
  const [description,setDescription] = useState(item?.description ?? '')
  const [perMember,setPerMember] = useState<Limits>(item?.quota.perMember ?? {})
  const [total,setTotal] = useState<Limits>(item?.quota.total ?? {})
  const [busy,setBusy] = useState(false)
  const submit = async (event:FormEvent) => {
    event.preventDefault(); setBusy(true)
    try { await api.post('/api/v1/admin/departments',{id:item?.id,name,description,quota:{perMember,total}}); done() }
    catch (e) { error(e instanceof Error?e.message:'부서를 저장하지 못했습니다.') } finally { setBusy(false) }
  }
  return <Drawer title={item?`${item.name} 한도`:'새 부서'} close={close} footer={<><button className="button ghost" onClick={close}>취소</button><button className="button primary" form="department-form" disabled={busy}>{busy?'저장 중…':'저장'}</button></>}>
    <form id="department-form" className="drawer-form" onSubmit={submit}>
      <label><span>부서 이름</span><input required maxLength={80} value={name} onChange={(e) => setName(e.target.value)} placeholder="플랫폼팀"/></label>
      <label><span>설명</span><textarea rows={2} maxLength={300} value={description} onChange={(e) => setDescription(e.target.value)}/></label>
      <LimitFields title="구성원 1인 기본" hint="이 부서에 속한 사람 한 명에게 적용됩니다. 비워 두면 플랫폼 기본값을 따릅니다." value={perMember} change={setPerMember}/>
      <LimitFields title="부서 총량" hint="구성원 전체가 함께 쓰는 상한입니다. 한 사람이 자기 한도 안에 있어도 부서가 가득 차면 시작할 수 없습니다." value={total} change={setTotal} onlyResources/>
    </form>
  </Drawer>
}

function PersonDrawer({person,departments,override,close,done,error}:{person:ManagedUser;departments:Department[];override?:UserQuota;close:()=>void;done:()=>void;error:(v:string)=>void}) {
  const [departmentId,setDepartmentId] = useState(person.departmentId ?? '')
  const [limits,setLimits] = useState<Limits>(override?.quota ?? {})
  const [note,setNote] = useState(override?.note ?? '')
  const [effective,setEffective] = useState<EffectiveQuota>()
  const [busy,setBusy] = useState(false)
  useEffect(() => { api.get<EffectiveQuota>(`/api/v1/admin/users/${person.id}/quota`).then(setEffective).catch(() => setEffective(undefined)) }, [person.id])
  const submit = async (event:FormEvent) => {
    event.preventDefault(); setBusy(true)
    try {
      if ((person.departmentId ?? '') !== departmentId) await api.post(`/api/v1/admin/users/${person.id}/department`,{departmentId})
      await api.post(`/api/v1/admin/users/${person.id}/quota`,{quota:limits,note})
      done()
    } catch (e) { error(e instanceof Error?e.message:'Quota를 저장하지 못했습니다.') } finally { setBusy(false) }
  }
  return <Drawer title={`${person.displayName} · Quota`} close={close} footer={<><button className="button ghost" onClick={close}>취소</button><button className="button primary" form="person-quota-form" disabled={busy}>{busy?'저장 중…':'저장'}</button></>}>
    <form id="person-quota-form" className="drawer-form" onSubmit={submit}>
      <label><span>부서</span><select value={departmentId} onChange={(e) => setDepartmentId(e.target.value)}><option value="">부서 없음</option>{departments.map((v) => <option key={v.id} value={v.id}>{v.name}</option>)}</select></label>
      <LimitFields title="개인 예외" hint="이 사람에게만 적용되며 부서·플랫폼 설정을 덮어씁니다. 비워 두면 예외가 사라집니다." value={limits} change={setLimits}/>
      <label><span>메모</span><input maxLength={300} value={note} onChange={(e) => setNote(e.target.value)} placeholder="예: 플랫폼 운영자 예외 (2026-09-01 재검토)"/></label>
      {effective&&<EffectiveTable value={effective}/>}
    </form>
  </Drawer>
}

// EffectiveTable answers "what applies to this person, and which level set it".
// The number alone does not tell an administrator what to change.
export function EffectiveTable({value}:{value:EffectiveQuota}) {
  return <div className="quota-effective">
    <h4>실제 적용되는 한도</h4>
    <table><thead><tr><th>항목</th><th>적용값</th><th>출처</th><th>사용 중</th></tr></thead><tbody>
      {LIMIT_FIELDS.map((field) => {
        const applied = value.effective[field.key] ?? 0
        return <tr key={field.key}>
          <td>{field.label}</td>
          <td>{applied>0?`${applied}${field.unit}`:'제한 없음'}</td>
          <td><span className={`quota-source ${quotaSource(value,field.key).tone}`}>{quotaSource(value,field.key).label}</span></td>
          <td className="muted-cell">{field.held?`${value.held[field.held]}${field.unit}`:'—'}</td>
        </tr>
      })}
    </tbody></table>
    {value.department&&<p className="muted-cell">부서 <b>{value.department}</b> 총량 {limitSummary(value.departmentQuota.total)} · 현재 {value.departmentHeld.runtimes}개 Runtime 사용 중</p>}
  </div>
}

function LimitFields({title,hint,value,change,onlyResources}:{title:string;hint:string;value:Limits;change:(v:Limits)=>void;onlyResources?:boolean}) {
  const fields = onlyResources?LIMIT_FIELDS.filter((v) => v.resource):LIMIT_FIELDS
  const update = (key:keyof Limits, raw:string) => { const next = {...value}; const n = Number(raw); if (!raw||n===0||Number.isNaN(n)) delete next[key]; else next[key] = n; change(next) }
  return <fieldset className="quota-fields">
    <legend>{title}</legend>
    <p className="field-hint">{hint}</p>
    <div className="form-grid">{fields.map((field) => <label key={field.key}><span>{field.label}{field.unit&&` (${field.unit.trim()})`}</span>
      <input type="number" min={0} step={field.step??1} value={value[field.key]??''} placeholder="상위 설정" onChange={(e) => update(field.key,e.target.value)}/></label>)}</div>
  </fieldset>
}
