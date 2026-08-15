import { FormEvent, useCallback, useEffect, useMemo, useState } from 'react'
import { Bot, Boxes, Check, Cpu, Database, Search, Sparkles, WandSparkles } from 'lucide-react'
import { useNavigate } from 'react-router-dom'
import { api } from '../api'
import { Drawer, ErrorBanner, Loading, PageHeader } from '../components/UI'
import type { RuntimeProfile, Template, Workspace } from '../types'

export function Catalog({builder=false}:{builder?:boolean}){
  const [templates,setTemplates]=useState<Template[]>([]),[profiles,setProfiles]=useState<RuntimeProfile[]>([]),[workspaces,setWorkspaces]=useState<Workspace[]>([]),[models,setModels]=useState<{id:string;name:string;defaultModel:string}[]>([]),[bundles,setBundles]=useState<{id:string;name:string;description:string}[]>([]),[selected,setSelected]=useState<Template|null>(null),[query,setQuery]=useState('')
  const load=useCallback(()=>Promise.all([api.get<{items:Template[]}>('/api/v1/templates').then(v=>setTemplates(v.items)),api.get<{items:RuntimeProfile[]}>('/api/v1/runtime-profiles').then(v=>setProfiles(v.items)),api.get<{items:Workspace[]}>('/api/v1/workspaces').then(v=>setWorkspaces(v.items)),api.get<{items:{id:string;name:string;defaultModel:string}[]}>('/api/v1/models').then(v=>setModels(v.items)),api.get<{items:{id:string;name:string;description:string}[]}>('/api/v1/mcp-bundles').then(v=>setBundles(v.items))]),[])
  useEffect(()=>{void load()},[load])
  const filtered=templates.filter(t=>`${t.name} ${t.description} ${t.category}`.toLowerCase().includes(query.toLowerCase()))
  return <div className="page"><PageHeader eyebrow={builder?'AGENT FACTORY':'MARKETPLACE'} title={builder?'Agent Builder':'Agent Catalog'} description={builder?'Template, Runtime Profile과 Workspace를 조합해 개인 Agent를 만듭니다.':'관리자가 검증하고 게시한 Agent Template으로 빠르게 시작하세요.'}/>
    <div className="toolbar"><div className="search-box"><Search size={17}/><input value={query} onChange={e=>setQuery(e.target.value)} placeholder="Template 검색"/></div><div className="filter-chips"><button className="selected">전체</button><button>Development</button><button>Research</button><button>Operations</button></div></div>
    {templates.length===0?<Loading/>:<div className="catalog-grid">{filtered.map(t=><button className="template-card" key={t.id} onClick={()=>setSelected(t)}><div className="template-top"><div className={`runtime-logo large ${t.runtimeType}`}>{t.runtimeType==='opencode'?'OC':'H'}</div><span className="verified"><Check size={12}/>Verified</span></div><span className="category">{t.category}</span><h3>{t.name}</h3><p>{t.description}</p><div className="template-meta"><span><Bot size={14}/>{t.runtimeType}</span><span><Boxes size={14}/>v{t.version}</span></div></button>)}</div>}
    {selected&&<CreateDrawer template={selected} profiles={profiles} workspaces={workspaces} models={models} bundles={bundles} close={()=>setSelected(null)}/>}
  </div>
}

function CreateDrawer({template,profiles,workspaces,models,bundles,close}:{template:Template;profiles:RuntimeProfile[];workspaces:Workspace[];models:{id:string;name:string;defaultModel:string}[];bundles:{id:string;name:string;description:string}[];close:()=>void}){
  const navigate=useNavigate();const defaults=useMemo(()=>profiles.find(p=>p.id===template.runtimeProfileId)??profiles.find(p=>p.id==='rp-basic')??profiles[0],[profiles,template]);const [name,setName]=useState(template.name),[profile,setProfile]=useState(defaults?.id??''),[workspace,setWorkspace]=useState(''),[model,setModel]=useState(models[0]?.id??''),[bundle,setBundle]=useState(''),[prompt,setPrompt]=useState(''),[autoStart,setAutoStart]=useState(true),[error,setError]=useState(''),[busy,setBusy]=useState(false)
  const submit=async(e:FormEvent)=>{
    e.preventDefault();setBusy(true);setError('')
    try{
      const created=await api.post<{agent: {id: string}} | {id: string}>('/api/v1/agents',{name,description:template.description,runtimeType:template.runtimeType,runtimeProfileId:profile,workspaceId:workspace,modelEndpointId:model,mcpBundleId:bundle,templateId:template.id,systemPrompt:prompt})
      const agentId = (created as any)?.agent?.id || (created as any)?.id
      if (autoStart && agentId) {
        try {
          await api.post(`/api/v1/agents/${agentId}/spawn`)
        } catch (spawnErr) {
          console.warn('Auto-spawn warning:', spawnErr)
        }
      }
      navigate('/agents')
    }catch(err){
      setError(err instanceof Error?err.message:'Agent를 만들지 못했습니다.')
    }finally{
      setBusy(false)
    }
  }
  return <Drawer title="새 Agent 만들기" subtitle={`${template.name} Template`} close={close} footer={<><button type="button" className="button ghost" onClick={close}>취소</button><button className="button primary" form="create-agent" disabled={busy}><WandSparkles size={17}/>{busy?'생성 중…':'Agent 생성 및 시작'}</button></>}><form id="create-agent" className="drawer-form" onSubmit={submit}>{error&&<ErrorBanner message={error}/>}<section className="selection-summary"><div className={`runtime-logo large ${template.runtimeType}`}>{template.runtimeType==='opencode'?'OC':'H'}</div><div><span>Runtime Template</span><strong>{template.name}</strong><small>{template.description}</small></div></section><label><span>Agent 이름 <b>*</b></span><input required maxLength={80} value={name} onChange={e=>setName(e.target.value)} /></label><label><span>Runtime Profile <b>*</b></span><select required value={profile} onChange={e=>setProfile(e.target.value)}>{profiles.map(p=><option value={p.id} key={p.id}>{p.name} · {p.cpuMillis/1000} CPU / {p.memoryMb/1024} GB / {p.storageGb} GB</option>)}</select></label><label><span>Model</span><select value={model} onChange={e=>setModel(e.target.value)}><option value="">나중에 연결</option>{models.map(v=><option value={v.id} key={v.id}>{v.name} · {v.defaultModel}</option>)}</select></label><label><span>MCP Bundle</span><select value={bundle} onChange={e=>setBundle(e.target.value)}><option value="">MCP 없이 시작</option>{bundles.map(v=><option value={v.id} key={v.id}>{v.name}</option>)}</select></label><label><span>Workspace</span><select value={workspace} onChange={e=>setWorkspace(e.target.value)}><option value="">Workspace 없이 생성</option>{workspaces.map(w=><option value={w.id} key={w.id}>{w.name} · {w.sizeGb} GB</option>)}</select><small>Pod가 재생성되어도 연결한 Workspace는 유지됩니다.</small></label><label><span>추가 지시사항</span><textarea rows={5} value={prompt} onChange={e=>setPrompt(e.target.value)} placeholder="이 Agent의 역할과 작업 규칙을 입력하세요."/></label><div className="info-box"><Sparkles size={17}/><div><strong>자동 구성</strong><p>Restricted Security Profile, Network Policy와 선택한 MCP/Model Binding이 함께 적용됩니다.</p></div></div><div className="profile-preview"><span><Cpu size={16}/>격리된 자원</span><span><Database size={16}/>영속 Workspace</span><span><Bot size={16}/>사용자 전용 Pod</span></div></form></Drawer>
}
