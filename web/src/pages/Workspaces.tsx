import { FormEvent, useEffect, useState } from 'react'
import { Camera, Database, FolderGit2, HardDrive, Pencil, Plus, Trash2 } from 'lucide-react'
import { useNavigate } from 'react-router-dom'
import { api } from '../api'
import { ConfirmDialog, Drawer, Empty, ErrorBanner, Loading, PageHeader, StatusBadge } from '../components/UI'
import type { Workspace } from '../types'

export function Workspaces(){
  const navigate=useNavigate()
  const [items,setItems]=useState<Workspace[]>(),[open,setOpen]=useState(false),[error,setError]=useState('')
  const [editing,setEditing]=useState<Workspace|null>(null)
  const [removing,setRemoving]=useState<Workspace|null>(null)
  const [removeBusy,setRemoveBusy]=useState(false),[removeError,setRemoveError]=useState('')
  const load=()=>api.get<{items?:Workspace[]}>('/api/v1/workspaces').then(v=>setItems(v.items??[])).catch(()=>{setItems([]);setError('Workspace 목록을 불러오지 못했습니다.')})
  useEffect(()=>{void load()},[])
  const remove=async()=>{
    if(!removing) return
    setRemoveBusy(true); setRemoveError('')
    try { await api.delete(`/api/v1/workspaces/${removing.id}`); setRemoving(null); await load() }
    catch(err){ setRemoveError(err instanceof Error?err.message:'Workspace를 삭제하지 못했습니다.') }
    finally { setRemoveBusy(false) }
  }
  if(!items)return <Loading/>;return <div className="page"><PageHeader eyebrow="PERSISTENT STORAGE" title="My Workspaces" description="Runtime과 독립적으로 유지되는 프로젝트 저장공간을 관리합니다." actions={<button className="button primary" onClick={()=>setOpen(true)}><Plus size={17}/>Workspace 만들기</button>}/>{error&&<ErrorBanner message={error} onClose={()=>setError('')}/>} {items.length===0?<Empty icon={<HardDrive/>} title="Workspace가 없습니다" description="Git Repository 또는 빈 영속 공간을 먼저 만들어 보세요." action={<button className="button primary" onClick={()=>setOpen(true)}>Workspace 만들기</button>}/>:<div className="workspace-grid">{items.map(item=><article className="workspace-card" key={item.id}><div className="workspace-icon">{item.type==='git'?<FolderGit2/>:<Database/>}</div><div className="workspace-title"><div><h3>{item.name}</h3><span>{item.type}</span></div><StatusBadge status={item.status}/></div><div className="capacity"><div><span>Storage</span><strong>{item.sizeGb} GB</strong></div><div className="capacity-track"><span style={{width:'4%'}}/></div></div>{item.repositoryUrl&&<div className="repo-line"><FolderGit2 size={15}/><code>{item.repositoryUrl}</code><span>{item.branch||'default'}</span></div>}<footer><code>{item.pvcName}</code><div className="card-actions"><button title="Snapshot" onClick={()=>navigate(`/workspaces/snapshots?workspace=${item.id}`)}><Camera size={15}/>Snapshot</button><button title="이름 수정" onClick={()=>setEditing(item)}><Pencil size={15}/></button><button className="danger" title="삭제" onClick={()=>{setRemoveError('');setRemoving(item)}}><Trash2 size={15}/></button></div></footer></article>)}</div>}{open&&<WorkspaceDrawer close={()=>setOpen(false)} done={()=>{setOpen(false);void load()}} setError={setError}/>}
{editing&&<RenameWorkspaceDrawer item={editing} close={()=>setEditing(null)} done={()=>{setEditing(null);void load()}}/>}
{removing&&<ConfirmDialog
  title="Workspace를 삭제할까요?"
  message={<><strong>{removing.name}</strong> 기록과 Snapshot 목록이 삭제됩니다.<br/>영속 볼륨 <code>{removing.pvcName}</code>은 보존되므로 저장소 회수는 관리자가 직접 해야 합니다.</>}
  busy={removeBusy} error={removeError}
  onConfirm={()=>void remove()} onCancel={()=>setRemoving(null)}/>}
</div>}

/** Only the name is editable: type, size and the bound PVC are fixed at provisioning time. */
function RenameWorkspaceDrawer({item,close,done}:{item:Workspace;close:()=>void;done:()=>void}){
  const [name,setName]=useState(item.name),[busy,setBusy]=useState(false),[error,setError]=useState('')
  const submit=async(e:FormEvent)=>{
    e.preventDefault(); setBusy(true); setError('')
    try { await api.put(`/api/v1/workspaces/${item.id}`,{name}); done() }
    catch(err){ setError(err instanceof Error?err.message:'수정하지 못했습니다.'); setBusy(false) }
  }
  return <Drawer title="Workspace 수정" subtitle={item.pvcName} close={close} footer={<><button className="button ghost" onClick={close}>취소</button><button className="button primary" form="workspace-rename" disabled={busy}>{busy?'저장 중…':'저장'}</button></>}>
    <form className="drawer-form" id="workspace-rename" onSubmit={submit}>
      {error&&<ErrorBanner message={error} onClose={()=>setError('')}/>}
      <label><span>이름 <b>*</b></span><input required maxLength={80} value={name} onChange={e=>setName(e.target.value)}/></label>
      <div className="info-box"><HardDrive size={17}/><div><strong>변경할 수 없는 항목</strong><p>초기화 방식({item.type}), 용량({item.sizeGb} GB)과 연결된 볼륨은 생성 후 바꿀 수 없습니다.</p></div></div>
    </form>
  </Drawer>
}

function WorkspaceDrawer({close,done,setError}:{close:()=>void;done:()=>void;setError:(v:string)=>void}){const [name,setName]=useState(''),[type,setType]=useState('empty'),[size,setSize]=useState(20),[repository,setRepository]=useState(''),[branch,setBranch]=useState(''),[busy,setBusy]=useState(false);const submit=async(e:FormEvent)=>{e.preventDefault();setBusy(true);try{await api.post('/api/v1/workspaces',{name,type,sizeGb:size,repositoryUrl:repository,branch});done()}catch(err){setError(err instanceof Error?err.message:'생성하지 못했습니다.')}finally{setBusy(false)}};return <Drawer title="새 Workspace" subtitle="Agent Pod가 종료되어도 데이터는 유지됩니다." close={close} footer={<><button className="button ghost" onClick={close}>취소</button><button className="button primary" form="workspace-form" disabled={busy}>{busy?'생성 중…':'Workspace 생성'}</button></>}><form className="drawer-form" id="workspace-form" onSubmit={submit}><label><span>이름 <b>*</b></span><input required maxLength={80} value={name} onChange={e=>setName(e.target.value)} placeholder="project-backend"/></label><fieldset><legend>초기화 방식</legend><div className="choice-grid"><button type="button" className={type==='empty'?'selected':''} onClick={()=>setType('empty')}><Database/><strong>Empty</strong><span>빈 작업공간</span></button><button type="button" className={type==='git'?'selected':''} onClick={()=>setType('git')}><FolderGit2/><strong>Git Clone</strong><span>Repository 복제</span></button></div></fieldset>{type==='git'&&<><label><span>Repository URL <b>*</b></span><input required type="url" value={repository} onChange={e=>setRepository(e.target.value)} placeholder="https://git.example.local/team/project.git"/></label><label><span>Branch</span><input value={branch} onChange={e=>setBranch(e.target.value)} placeholder="main"/></label></>}<label><span>Storage</span><div className="range-value"><input type="range" min="5" max="200" step="5" value={size} onChange={e=>setSize(Number(e.target.value))}/><strong>{size} GB</strong></div></label></form></Drawer>}
