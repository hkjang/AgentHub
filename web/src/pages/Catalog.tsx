import { FormEvent, useCallback, useEffect, useMemo, useState } from 'react'
import { Bot, Boxes, Check, Cpu, Database, Search, Sparkles, WandSparkles } from 'lucide-react'
import { useNavigate } from 'react-router-dom'
import { api } from '../api'
import { Drawer, ErrorBanner, Loading, PageHeader } from '../components/UI'
import { EXPERIENCE_LABELS, descriptor, runnerSummary, runtimeCode, runtimeDescriptors, runtimeLabel, runtimeLogoClass } from '../runtime'
import type { RuntimeProfile, Template, Workspace } from '../types'

/** What this deployment has seen this runtime type do.
 *
 *  Fifteen types are offered and every one looks equally available, which is only
 *  true where every one has been set up. Somewhere without the image loaded, or
 *  without a cluster, they are not equal — and until this, the person choosing
 *  found out by creating an agent, pressing start and reading a failure.
 *
 *  "안 해 봄" is not a warning. Most deployments will never use most of these; it
 *  is the absence of evidence rather than a mark against the choice, which is why
 *  it is the quietest of the four. */
function ExperienceTag({ type }: { type: string }) {
  const experience = descriptor(type).experience
  if (!experience || experience.verdict === 'untried') return null
  return <span className={`experience-tag ${experience.verdict}`} title={experience.detail}>
    {EXPERIENCE_LABELS[experience.verdict] ?? experience.verdict}
  </span>
}

export function Catalog({builder=false}:{builder?:boolean}){
  const [templates,setTemplates]=useState<Template[]>([]),[profiles,setProfiles]=useState<RuntimeProfile[]>([]),[workspaces,setWorkspaces]=useState<Workspace[]>([]),[models,setModels]=useState<{id:string;name:string;defaultModel:string}[]>([]),[bundles,setBundles]=useState<{id:string;name:string;description:string}[]>([]),[selected,setSelected]=useState<Template|null>(null),[query,setQuery]=useState(''),[activeCategory,setActiveCategory]=useState('전체')
  const load=useCallback(()=>Promise.all([api.get<{items:Template[]}>('/api/v1/templates').then(v=>setTemplates(v.items)),api.get<{items:RuntimeProfile[]}>('/api/v1/runtime-profiles').then(v=>setProfiles(v.items)),api.get<{items:Workspace[]}>('/api/v1/workspaces').then(v=>setWorkspaces(v.items)),api.get<{items:{id:string;name:string;defaultModel:string}[]}>('/api/v1/models').then(v=>setModels(v.items)),api.get<{items:{id:string;name:string;description:string}[]}>('/api/v1/mcp-bundles').then(v=>setBundles(v.items))]),[])
  useEffect(()=>{void load()},[load])
  const filtered=templates.filter(t=>{
    const matchQuery = `${t.name} ${t.description} ${t.category} ${t.runtimeType}`.toLowerCase().includes(query.toLowerCase())
    const matchCat = activeCategory === '전체' || t.category.toLowerCase() === activeCategory.toLowerCase()
    return matchQuery && matchCat
  })
  const [comparing,setComparing]=useState(false)
  return <div className="page"><PageHeader eyebrow={builder?'에이전트 제작':'마켓플레이스'} title={builder?'에이전트 빌더':'에이전트 카탈로그'} description={builder?'템플릿, 런타임 프로파일과 작업공간을 조합해 개인 에이전트를 만듭니다.':'관리자가 검증하고 게시한 에이전트 템플릿으로 빠르게 시작하세요.'}/>
    <RuntimeComparison open={comparing} toggle={()=>setComparing(!comparing)}/>
    <div className="toolbar"><div className="search-box"><Search size={17}/><input value={query} onChange={e=>setQuery(e.target.value)} placeholder="템플릿 또는 런타임 검색"/></div><div className="filter-chips"><button className={activeCategory==='전체'?'selected':''} onClick={()=>setActiveCategory('전체')}>전체</button><button className={activeCategory==='Development'?'selected':''} onClick={()=>setActiveCategory('Development')}>Development</button><button className={activeCategory==='Automation'?'selected':''} onClick={()=>setActiveCategory('Automation')}>Automation</button><button className={activeCategory==='Research'?'selected':''} onClick={()=>setActiveCategory('Research')}>Research</button><button className={activeCategory==='Operations'?'selected':''} onClick={()=>setActiveCategory('Operations')}>Operations</button></div></div>
    {templates.length===0?<Loading/>:filtered.length===0?<div className="empty-compact">검색 조건에 맞는 템플릿이 없습니다.</div>:<div className="catalog-grid">{filtered.map(t=><button className="template-card" key={t.id} onClick={()=>setSelected(t)}><div className="template-top"><div className={runtimeLogoClass(t.runtimeType,'large')}>{runtimeCode(t.runtimeType)}</div><span className="verified"><Check size={12}/>검증됨</span></div><span className="category">{t.category}</span><h3>{t.name}</h3><p>{t.description}</p><div className="template-meta"><span><Bot size={14}/>{runtimeLabel(t.runtimeType)}</span><span><Boxes size={14}/>v{t.version}</span><ExperienceTag type={t.runtimeType}/></div></button>)}</div>}
    {selected&&<CreateDrawer template={selected} profiles={profiles} workspaces={workspaces} models={models} bundles={bundles} close={()=>setSelected(null)}/>}
  </div>
}

function CreateDrawer({template,profiles,workspaces,models,bundles,close}:{template:Template;profiles:RuntimeProfile[];workspaces:Workspace[];models:{id:string;name:string;defaultModel:string}[];bundles:{id:string;name:string;description:string}[];close:()=>void}){
  const navigate=useNavigate();const defaults=useMemo(()=>profiles.find(p=>p.id===template.runtimeProfileId)??profiles.find(p=>p.id==='rp-basic')??profiles[0],[profiles,template]);const [name,setName]=useState(template.name),[profile,setProfile]=useState(defaults?.id??''),[workspace,setWorkspace]=useState(''),[model,setModel]=useState(models[0]?.id??''),[bundle,setBundle]=useState(''),[prompt,setPrompt]=useState(''),[command,setCommand]=useState(''),[customPort,setCustomPort]=useState(''),[autoStart,setAutoStart]=useState(true),[error,setError]=useState(''),[busy,setBusy]=useState(false)
  useEffect(()=>{if(!model && models.length > 0){setModel(models[0].id)}},[models,model])
  const submit=async(e:FormEvent)=>{
    e.preventDefault();setBusy(true);setError('')
    try{
      const created=await api.post<{id?:string;agent?:{id:string}}>('/api/v1/agents',{name,description:template.description,runtimeType:template.runtimeType,runtimeProfileId:profile,workspaceId:workspace,modelEndpointId:model,mcpBundleId:bundle,templateId:template.id,systemPrompt:prompt,
        customCommand:template.runtimeType==='custom'?command.split('\n').map(part=>part.trim()).filter(Boolean):undefined,
        customPort:template.runtimeType==='custom'&&customPort?Number(customPort):undefined})
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
  return <Drawer title="새 에이전트 만들기" subtitle={`${template.name} 템플릿`} close={close} footer={<><button type="button" className="button ghost" onClick={close}>취소</button><button className="button primary" form="create-agent" disabled={busy}><WandSparkles size={17}/>{busy?'생성 중…':autoStart?'에이전트 생성 및 시작':'에이전트 생성'}</button></>}><form id="create-agent" className="drawer-form" onSubmit={submit}>{error&&<ErrorBanner message={error} onClose={()=>setError('')}/>}<section className="selection-summary"><div className={runtimeLogoClass(template.runtimeType,'large')}>{runtimeCode(template.runtimeType)}</div><div><span>{runtimeLabel(template.runtimeType)} 런타임 템플릿</span><strong>{template.name}</strong><small>{template.description}</small></div></section><label><span>에이전트 이름 <b>*</b></span><input required maxLength={80} value={name} onChange={e=>setName(e.target.value)} /></label><label><span>런타임 프로파일 <b>*</b></span><select required value={profile} onChange={e=>setProfile(e.target.value)}>{profiles.map(p=><option value={p.id} key={p.id}>{p.name} · {p.cpuMillis/1000} CPU / {p.memoryMb/1024} GB / {p.storageGb} GB</option>)}</select></label><label><span>모델</span><select value={model} onChange={e=>setModel(e.target.value)}><option value="">나중에 연결</option>{models.map(v=><option value={v.id} key={v.id}>{v.name} · {v.defaultModel}</option>)}</select></label><label><span>MCP 번들</span><select value={bundle} onChange={e=>setBundle(e.target.value)}><option value="">MCP 없이 시작</option>{bundles.map(v=><option value={v.id} key={v.id}>{v.name}</option>)}</select></label><label><span>작업공간</span><select value={workspace} onChange={e=>setWorkspace(e.target.value)}><option value="">작업공간 없이 생성</option>{workspaces.map(w=><option value={w.id} key={w.id}>{w.name} · {w.sizeGb} GB</option>)}</select><small>Pod가 재생성되어도 연결한 작업공간은 유지됩니다.</small></label>{template.runtimeType==='custom'&&<><label><span>시작 명령 <b>*</b></span><textarea rows={4} value={command} onChange={e=>setCommand(e.target.value)} placeholder={'/usr/local/bin/my-agent\nserve\n--port\n9000'}/><small>한 줄에 하나씩 입력하세요. 쉘을 거치지 않으므로 따옴표나 파이프는 쓸 수 없습니다.</small></label><label><span>서비스 포트</span><input type="number" min={1} max={65535} value={customPort} onChange={e=>setCustomPort(e.target.value)} placeholder="4096"/><small>비워 두면 기본 포트를 사용합니다. 런타임이 실제로 듣는 포트와 같아야 준비 상태가 됩니다.</small></label></>}<label><span>추가 지시사항</span><textarea rows={5} value={prompt} onChange={e=>setPrompt(e.target.value)} placeholder="이 에이전트의 역할과 작업 규칙을 입력하세요."/></label><label className="toggle-row"><span>생성 후 런타임 바로 시작</span><input type="checkbox" checked={autoStart} onChange={(e)=>setAutoStart(e.target.checked)}/><i/></label><div className="info-box"><Sparkles size={17}/><div><strong>자동 구성</strong><p>제한된 보안 프로파일, 네트워크 정책과 선택한 MCP·모델 연결이 함께 적용됩니다.</p></div></div><div className="profile-preview"><span><Cpu size={16}/>격리된 자원</span><span><Database size={16}/>영속 작업공간</span><span><Bot size={16}/>사용자 전용 Pod</span></div></form></Drawer>
}

/**
 * What the runtimes actually are, side by side.
 *
 * A template card says "OpenCode" and nothing else, so choosing one meant either
 * knowing the products already or picking the first card. The facts here come from
 * the platform, so they describe the adapters this build runs rather than whatever
 * the console was last told about them.
 */
function RuntimeComparison({open,toggle}:{open:boolean;toggle:()=>void}){
  const items=runtimeDescriptors().filter((item)=>item.type!=='custom')
  if(items.length===0) return null
  return <section className="runtime-compare">
    <button type="button" className="guide-toggle" onClick={toggle} aria-expanded={open}>
      <Cpu size={16}/><strong>런타임 유형 비교</strong>
      <span>{open?'접기':'무엇을 고를지 보기'}</span>
    </button>
    {open&&<div className="runtime-compare-grid">
      {items.map((item)=><article key={item.type}>
        <header><div className={runtimeLogoClass(item.type)}>{item.code}</div><div><h4>{item.label}</h4><small>{item.bestFor}</small></div></header>
        <p>{item.summary}</p>
        <ul className="compare-facts">
          <li><b>작업공간</b> <code>{item.workspace}</code></li>
          <li><b>브라우저 작업</b> {item.browserUi?(item.terminal?'편집기 + 터미널':'편집기'):'없음'}</li>
          <li><b>MCP 도구</b> {item.mcpConfigured?'런타임 설정에 자동 등록':'런타임에 전달되지 않음'}</li>
          <li><b>자동 실행</b> {runnerSummary(item.runners)}</li>
          {item.hostSessionOnly?<li><b>공개 조건</b> Runtime 전용 도메인 필요</li>:null}
        </ul>
        {item.strengths?.length?<ul className="compare-pros">{item.strengths.map((line)=><li key={line}>{line}</li>)}</ul>:null}
        {item.watchouts?.length?<ul className="compare-cons">{item.watchouts.map((line)=><li key={line}>{line}</li>)}</ul>:null}
      </article>)}
    </div>}
  </section>
}
