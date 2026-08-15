import { FormEvent, useCallback, useEffect, useMemo, useState } from 'react'
import { Bot, Boxes, Check, Cpu, Database, Search, Sparkles, WandSparkles } from 'lucide-react'
import { useNavigate } from 'react-router-dom'
import { api } from '../api'
import { Drawer, ErrorBanner, Loading, PageHeader } from '../components/UI'
import { runtimeCode, runtimeLabel, runtimeLogoClass } from '../runtime'
import type { RuntimeProfile, Template, Workspace } from '../types'

export function Catalog({builder=false}:{builder?:boolean}){
  const [templates,setTemplates]=useState<Template[]>([]),[profiles,setProfiles]=useState<RuntimeProfile[]>([]),[workspaces,setWorkspaces]=useState<Workspace[]>([]),[models,setModels]=useState<{id:string;name:string;defaultModel:string}[]>([]),[bundles,setBundles]=useState<{id:string;name:string;description:string}[]>([]),[selected,setSelected]=useState<Template|null>(null),[query,setQuery]=useState(''),[activeCategory,setActiveCategory]=useState('전체')
  const load=useCallback(()=>Promise.all([api.get<{items:Template[]}>('/api/v1/templates').then(v=>setTemplates(v.items)),api.get<{items:RuntimeProfile[]}>('/api/v1/runtime-profiles').then(v=>setProfiles(v.items)),api.get<{items:Workspace[]}>('/api/v1/workspaces').then(v=>setWorkspaces(v.items)),api.get<{items:{id:string;name:string;defaultModel:string}[]}>('/api/v1/models').then(v=>setModels(v.items)),api.get<{items:{id:string;name:string;description:string}[]}>('/api/v1/mcp-bundles').then(v=>setBundles(v.items))]),[])
  useEffect(()=>{void load()},[load])
  const filtered=templates.filter(t=>{
    const matchQuery = `${t.name} ${t.description} ${t.category} ${t.runtimeType}`.toLowerCase().includes(query.toLowerCase())
    const matchCat = activeCategory === '전체' || t.category.toLowerCase() === activeCategory.toLowerCase()
    return matchQuery && matchCat
  })
  return <div className="page"><PageHeader eyebrow={builder?'에이전트 제작':'마켓플레이스'} title={builder?'에이전트 빌더':'에이전트 카탈로그'} description={builder?'템플릿, 런타임 프로파일과 작업공간을 조합해 개인 에이전트를 만듭니다.':'관리자가 검증하고 게시한 에이전트 템플릿으로 빠르게 시작하세요.'}/>
    <div className="toolbar"><div className="search-box"><Search size={17}/><input value={query} onChange={e=>setQuery(e.target.value)} placeholder="템플릿 또는 런타임 검색"/></div><div className="filter-chips"><button className={activeCategory==='전체'?'selected':''} onClick={()=>setActiveCategory('전체')}>전체</button><button className={activeCategory==='Development'?'selected':''} onClick={()=>setActiveCategory('Development')}>Development</button><button className={activeCategory==='Automation'?'selected':''} onClick={()=>setActiveCategory('Automation')}>Automation</button><button className={activeCategory==='Research'?'selected':''} onClick={()=>setActiveCategory('Research')}>Research</button><button className={activeCategory==='Operations'?'selected':''} onClick={()=>setActiveCategory('Operations')}>Operations</button></div></div>
    {templates.length===0?<Loading/>:filtered.length===0?<div className="empty-compact">검색 조건에 맞는 템플릿이 없습니다.</div>:<div className="catalog-grid">{filtered.map(t=><button className="template-card" key={t.id} onClick={()=>setSelected(t)}><div className="template-top"><div className={runtimeLogoClass(t.runtimeType,'large')}>{runtimeCode(t.runtimeType)}</div><span className="verified"><Check size={12}/>검증됨</span></div><span className="category">{t.category}</span><h3>{t.name}</h3><p>{t.description}</p><div className="template-meta"><span><Bot size={14}/>{runtimeLabel(t.runtimeType)}</span><span><Boxes size={14}/>v{t.version}</span></div></button>)}</div>}
    {selected&&<CreateDrawer template={selected} profiles={profiles} workspaces={workspaces} models={models} bundles={bundles} close={()=>setSelected(null)}/>}
  </div>
}

function CreateDrawer({template,profiles,workspaces,models,bundles,close}:{template:Template;profiles:RuntimeProfile[];workspaces:Workspace[];models:{id:string;name:string;defaultModel:string}[];bundles:{id:string;name:string;description:string}[];close:()=>void}){
  const navigate=useNavigate();const defaults=useMemo(()=>profiles.find(p=>p.id===template.runtimeProfileId)??profiles.find(p=>p.id==='rp-basic')??profiles[0],[profiles,template]);const [name,setName]=useState(template.name),[profile,setProfile]=useState(defaults?.id??''),[workspace,setWorkspace]=useState(''),[model,setModel]=useState(models[0]?.id??''),[bundle,setBundle]=useState(''),[prompt,setPrompt]=useState(''),[autoStart,setAutoStart]=useState(true),[error,setError]=useState(''),[busy,setBusy]=useState(false)
  useEffect(()=>{if(!model && models.length > 0){setModel(models[0].id)}},[models,model])
  const submit=async(e:FormEvent)=>{
    e.preventDefault();setBusy(true);setError('')
    try{
      const created=await api.post<{id?:string;agent?:{id:string}}>('/api/v1/agents',{name,description:template.description,runtimeType:template.runtimeType,runtimeProfileId:profile,workspaceId:workspace,modelEndpointId:model,mcpBundleId:bundle,templateId:template.id,systemPrompt:prompt})
      const agentId=created.agent?.id??created.id
      if(autoStart&&agentId){
        try{
          await api.post(`/api/v1/agents/${agentId}/spawn`)
        }catch(spawnErr){
          // The definition was saved; only the first Runtime start failed, so keep
          // the drawer open and say so instead of silently landing on My Agents.
          setError(`에이전트는 저장되었지만 런타임을 시작하지 못했습니다: ${spawnErr instanceof Error?spawnErr.message:'알 수 없는 오류'}`)
          setBusy(false)
          return
        }
      }
      navigate('/agents')
    }catch(err){
      setError(err instanceof Error?err.message:'에이전트를 만들지 못했습니다.')
      setBusy(false)
    }
  }
  return <Drawer title="새 에이전트 만들기" subtitle={`${template.name} 템플릿`} close={close} footer={<><button type="button" className="button ghost" onClick={close}>취소</button><button className="button primary" form="create-agent" disabled={busy}><WandSparkles size={17}/>{busy?'생성 중…':autoStart?'에이전트 생성 및 시작':'에이전트 생성'}</button></>}><form id="create-agent" className="drawer-form" onSubmit={submit}>{error&&<ErrorBanner message={error} onClose={()=>setError('')}/>}<section className="selection-summary"><div className={runtimeLogoClass(template.runtimeType,'large')}>{runtimeCode(template.runtimeType)}</div><div><span>{runtimeLabel(template.runtimeType)} 런타임 템플릿</span><strong>{template.name}</strong><small>{template.description}</small></div></section><label><span>에이전트 이름 <b>*</b></span><input required maxLength={80} value={name} onChange={e=>setName(e.target.value)} /></label><label><span>런타임 프로파일 <b>*</b></span><select required value={profile} onChange={e=>setProfile(e.target.value)}>{profiles.map(p=><option value={p.id} key={p.id}>{p.name} · {p.cpuMillis/1000} CPU / {p.memoryMb/1024} GB / {p.storageGb} GB</option>)}</select></label><label><span>모델</span><select value={model} onChange={e=>setModel(e.target.value)}><option value="">나중에 연결</option>{models.map(v=><option value={v.id} key={v.id}>{v.name} · {v.defaultModel}</option>)}</select></label><label><span>MCP 번들</span><select value={bundle} onChange={e=>setBundle(e.target.value)}><option value="">MCP 없이 시작</option>{bundles.map(v=><option value={v.id} key={v.id}>{v.name}</option>)}</select></label><label><span>작업공간</span><select value={workspace} onChange={e=>setWorkspace(e.target.value)}><option value="">작업공간 없이 생성</option>{workspaces.map(w=><option value={w.id} key={w.id}>{w.name} · {w.sizeGb} GB</option>)}</select><small>Pod가 재생성되어도 연결한 작업공간은 유지됩니다.</small></label><label><span>추가 지시사항</span><textarea rows={5} value={prompt} onChange={e=>setPrompt(e.target.value)} placeholder="이 에이전트의 역할과 작업 규칙을 입력하세요."/></label><label className="toggle-row"><span>생성 후 런타임 바로 시작</span><input type="checkbox" checked={autoStart} onChange={(e)=>setAutoStart(e.target.checked)}/><i/></label><div className="info-box"><Sparkles size={17}/><div><strong>자동 구성</strong><p>제한된 보안 프로파일, 네트워크 정책과 선택한 MCP·모델 연결이 함께 적용됩니다.</p></div></div><div className="profile-preview"><span><Cpu size={16}/>격리된 자원</span><span><Database size={16}/>영속 작업공간</span><span><Bot size={16}/>사용자 전용 Pod</span></div></form></Drawer>
}
