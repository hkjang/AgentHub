import { FormEvent, useEffect, useState } from 'react'
import { Activity, Boxes, ExternalLink, FileCog, KeyRound, Network, Plus, Save, Settings, ShieldCheck, Trash2 } from 'lucide-react'
import { subject } from '../korean'
import { api } from '../api'
import { ErrorBanner, Loading, PageHeader, SuccessBanner } from '../components/UI'

type SettingsMap=Record<string,Record<string,unknown>>
const tabs=[{id:'general',label:'General',icon:Settings},{id:'authentication',label:'Authentication',icon:KeyRound},{id:'kubernetes',label:'Kubernetes',icon:Boxes},{id:'runtimeEnvironment',label:'Runtime Environment',icon:FileCog},{id:'sessionGateway',label:'Session Gateway',icon:ExternalLink},{id:'governance',label:'Governance',icon:ShieldCheck},{id:'logging',label:'Logging',icon:Network},{id:'observability',label:'Observability',icon:Activity},{id:'release',label:'Offline & Release',icon:Save}]
export function AdminSettings(){const [settings,setSettings]=useState<SettingsMap>(),[tab,setTab]=useState('general'),[error,setError]=useState(''),[notice,setNotice]=useState(''),[secret,setSecret]=useState('');useEffect(()=>{api.get<SettingsMap>('/api/v1/admin/settings').then(setSettings).catch(e=>setError(e.message))},[]);if(!settings)return <Loading/>;const value=settings[tab]??{};const save=async(e:FormEvent)=>{e.preventDefault();setError('');setNotice('');try{const result=await api.put<{runtimeEnvironment?:{message:string}}>(`/api/v1/admin/settings/${tab}`,{value,secret:secret||undefined});setNotice(result?.runtimeEnvironment?.message??'설정을 저장했습니다. 새 Runtime과 다음 로그인부터 적용됩니다.');setSecret('')}catch(err){setError(err instanceof Error?err.message:'설정을 저장하지 못했습니다.')}};const update=(key:string,next:unknown)=>setSettings(current=>({...current!,[tab]:{...current![tab],[key]:next}}));return <div className="page"><PageHeader eyebrow="관리자" title="시스템 설정" description="배포 후 운영 설정은 환경변수가 아닌 이 화면에서 안전하게 관리합니다."/>{error&&<ErrorBanner message={error}/>} {notice&&<SuccessBanner message={notice}/>}<div className="settings-layout"><nav className="settings-nav">{tabs.map(({id,label,icon:Icon})=><button className={tab===id?'active':''} onClick={()=>{setTab(id);setNotice('');setSecret('')}} key={id}><Icon size={17}/>{label}</button>)}</nav><form className="settings-panel" onSubmit={save}><SettingsForm tab={tab} value={value} update={update} secret={secret} setSecret={setSecret}/><footer><span>비밀값은 저장 후 마스킹되며 API 응답으로 반환되지 않습니다.</span><button className="button primary"><Save size={16}/>변경사항 저장</button></footer></form></div></div>}

function SettingsForm({tab,value,update,secret,setSecret}:{tab:string;value:Record<string,unknown>;update:(key:string,v:unknown)=>void;secret:string;setSecret:(v:string)=>void}){
  if(tab==='general')return <><Section title="서비스" description="사용자에게 표시되는 기본 정보입니다."><Field label="서비스 이름"><input value={String(value.serviceName??'AgentHub')} onChange={e=>update('serviceName',e.target.value)}/></Field><Field label="Public URL" hint="OIDC Callback URL 생성에 사용합니다."><input type="url" value={String(value.publicUrl??'')} onChange={e=>update('publicUrl',e.target.value)} placeholder="https://agenthub.company.local"/></Field><div className="form-grid"><Field label="기본 언어"><select value={String(value.defaultLocale??'ko')} onChange={e=>update('defaultLocale',e.target.value)}><option value="ko">한국어</option><option value="en">English</option></select></Field><Field label="시간대"><input value={String(value.timezone??'Asia/Seoul')} onChange={e=>update('timezone',e.target.value)}/></Field></div></Section></>
  if(tab==='authentication')return <><Section title="로그인 방식" description="Keycloak의 표준 OIDC Discovery 문서를 이용해 자동 연동합니다."><Toggle label="로컬 관리자 로그인" checked={Boolean(value.localLoginEnabled)} change={v=>update('localLoginEnabled',v)}/><Toggle label="Keycloak OIDC 사용" checked={Boolean(value.oidcEnabled)} change={v=>update('oidcEnabled',v)}/></Section><Section title="OIDC Client" description="Keycloak Realm에서 Confidential Client를 만든 뒤 세 항목만 입력하세요."><Field label="Issuer URL"><input type="url" value={String(value.issuerUrl??'')} onChange={e=>update('issuerUrl',e.target.value)} placeholder="https://keycloak.local/realms/company"/></Field><Field label="Client ID"><input value={String(value.clientId??'')} onChange={e=>update('clientId',e.target.value)} placeholder="agenthub"/></Field><Field label="Client Secret" hint={value.clientSecretConfigured?'현재 Secret이 설정되어 있습니다. 변경할 때만 입력하세요.':'Keycloak Client Secret을 입력하세요.'}><input type="password" autoComplete="new-password" value={secret} onChange={e=>setSecret(e.target.value)} placeholder={value.clientSecretConfigured?'••••••••':'Client Secret'}/></Field><div className="form-grid"><Field label="Username Claim"><input value={String(value.usernameClaim??'preferred_username')} onChange={e=>update('usernameClaim',e.target.value)}/></Field><Field label="Groups Claim"><input value={String(value.groupsClaim??'groups')} onChange={e=>update('groupsClaim',e.target.value)}/></Field></div><Field label="Admin Groups" hint="쉼표로 구분합니다."><input value={(value.adminGroups as string[]??[]).join(', ')} onChange={e=>update('adminGroups',e.target.value.split(',').map(v=>v.trim()).filter(Boolean))} placeholder="/agenthub-admins"/></Field><SSOCheck/></Section></>
  if(tab==='kubernetes')return <><Section title="Kubernetes Control Plane" description="AgentRuntime CRD를 생성할 클러스터 연결입니다."><Toggle label="Runtime Spawn 사용" checked={Boolean(value.enabled)} change={v=>update('enabled',v)}/><div className="form-grid"><Field label="연결 방식"><select value={String(value.mode??'inCluster')} onChange={e=>update('mode',e.target.value)}><option value="inCluster">In-cluster ServiceAccount</option><option value="token">External API Token</option></select></Field><Field label="Runtime Namespace"><input value={String(value.namespace??'agent-runtime-dev')} onChange={e=>update('namespace',e.target.value)}/></Field></div>{value.mode==='token'&&<><Field label="API Server"><input type="url" value={String(value.apiServer??'')} onChange={e=>update('apiServer',e.target.value)} placeholder="https://kubernetes.default.svc"/></Field><Field label="Bearer Token" hint="현재 토큰을 유지하려면 비워 두세요."><input type="password" value={secret} onChange={e=>setSecret(e.target.value)} placeholder="••••••••"/></Field></>}<Toggle label="TLS 인증서 검증" checked={Boolean(value.verifyTls)} change={v=>update('verifyTls',v)}/><Toggle label="AgentRuntime CRD 사용" checked={Boolean(value.crdEnabled)} change={v=>update('crdEnabled',v)}/><ClusterCheck/></Section></>
  if(tab==='runtimeEnvironment')return <RuntimeEnvironmentForm value={value} update={update}/>
  if(tab==='sessionGateway')return <><Section title="Runtime Browser Session" description="Runtime Base Domain을 설정하면 Runtime마다 전용 Origin(권장)을 사용하고, 비워 두면 Portal 도메인의 /{runtimeId}/ 경로로 같은 세션을 엽니다."><Toggle label="Runtime 전용 서브도메인 사용 (권장)" checked={Boolean(value.enabled)} change={v=>update('enabled',v)}/><div className="form-grid"><Field label="Scheme"><select value={String(value.scheme??'https')} onChange={e=>update('scheme',e.target.value)}><option value="https">HTTPS</option><option value="http">HTTP (localhost 전용)</option></select></Field><Field label="세션 유효시간"><select value={Number(value.sessionHours??8)} onChange={e=>update('sessionHours',Number(e.target.value))}><option value={1}>1시간</option><option value={4}>4시간</option><option value={8}>8시간</option><option value={12}>12시간</option><option value={24}>24시간</option></select></Field></div><Field label="Runtime Base Domain" hint="Wildcard DNS/TLS와 Ingress가 이 서비스로 연결되어야 합니다. 로컬 예: localhost:8080"><input value={String(value.baseDomain??'')} onChange={e=>update('baseDomain',e.target.value)} placeholder="agents.company.local"/></Field><div className="info-box"><ShieldCheck size={17}/><div><strong>One-time Launch Ticket</strong><p>Portal 인증 후 2분짜리 일회용 URL을 발급하고 Runtime 전용 HttpOnly 세션으로 교환합니다. 두 방식 모두 동일하게 적용됩니다.</p></div></div><div className="info-box"><ExternalLink size={17}/><div><strong>도메인을 비워 두면 경로 방식으로 동작합니다</strong><p>Wildcard DNS·인증서가 없어도 <code>https://포털주소/&#123;runtimeId&#125;/</code>로 작업공간을 열 수 있습니다. 다만 Runtime UI가 Portal과 같은 Origin을 공유하므로, 준비가 되면 전용 서브도메인 방식으로 전환하는 것을 권장합니다.</p></div></div></Section></>
  if(tab==='governance')return <><Section title="검토 및 승인" description="팀장 승인 설정이 꺼져 있으면 생성·검토·승인·반려 흐름 자체가 사용자 화면에서 제외됩니다."><Toggle label="팀장 검토 및 승인 사용" checked={Boolean(value.teamApprovalEnabled)} change={v=>update('teamApprovalEnabled',v)}/><Toggle label="고위험 Tool 실행 승인" checked={Boolean(value.highRiskToolApproval)} change={v=>update('highRiskToolApproval',v)}/></Section><Section title="기본 Quota" description="0은 제한 없음입니다."><div className="form-grid"><NumberField label="사용자당 Runtime" name="maxRuntimesPerUser" value={value} update={update}/><NumberField label="CPU (millicores)" name="maxCpuMillisPerUser" value={value} update={update}/><NumberField label="Memory (MB)" name="maxMemoryMbPerUser" value={value} update={update}/><NumberField label="Storage (GB)" name="maxStorageGbPerUser" value={value} update={update}/><NumberField label="GPU" name="maxGpusPerUser" value={value} update={update}/></div><NumberField label="Idle Timeout (초)" name="defaultIdleTimeoutSeconds" value={value} update={update} hint="런타임 프로파일에 값이 없을 때만 쓰이는 바닥값입니다. 보통은 프로파일마다 정하며, 관리자 ▸ 런타임 프로파일에서 확인·변경합니다."/></Section>
    <Section title="실행 Quota" description="자율 실행이 쓰는 자원과 비용의 한도입니다. 최근 30일 사용량을 기준으로 판단하며 0은 제한 없음입니다.">
      <div className="form-grid">
        <NumberField label="사용자당 동시 실행 작업" name="maxRunningTasksPerUser" value={value} update={update}/>
        <NumberField label="사용자당 토큰 예산 (30일)" name="tokenBudgetPerUser" value={value} update={update}/>
        <NumberField label="사용자당 비용 예산 (30일)" name="costBudgetPerUser" value={value} update={update}/>
      </div>
      <div className="info-box"><ShieldCheck size={17}/><div><strong>한도에 걸리면 어떻게 되나요</strong><p>동시 실행 한도는 <b>대기</b>입니다 — 앞선 작업이 끝나면 재시도 횟수를 쓰지 않고 이어서 실행됩니다. 예산 초과는 <b>실패</b>로 처리하고 소유자에게 알립니다. 며칠 뒤에야 풀리는 한도를 기다리며 워커 자리를 잡고 있지 않기 위해서입니다. 단가가 없는 모델의 토큰은 비용에 잡히지 않으므로 토큰 예산도 함께 설정하세요.</p></div></div>
    </Section></>
  if(tab==='observability')return <Section title="분산 추적 (OpenTelemetry)" description="작업 실행·모델 호출·워크플로 단계를 하나의 Trace로 내보내 어디서 시간이 걸리고 토큰이 쓰였는지 추적합니다.">
    <Toggle label="추적 사용" checked={Boolean(value.enabled)} change={v=>update('enabled',v)}/>
    <Field label="OTLP Collector 주소" hint="OTLP/HTTP 엔드포인트입니다. 예: http://otel-collector.observability.svc:4318">
      <input type="url" value={String(value.endpoint??'')} onChange={e=>update('endpoint',e.target.value)} placeholder="http://otel-collector.observability.svc:4318"/>
    </Field>
    <div className="form-grid">
      <Field label="서비스 이름" hint="수집기에서 스팬을 묶는 이름입니다."><input value={String(value.serviceName??'agenthub')} onChange={e=>update('serviceName',e.target.value)}/></Field>
      <Field label="샘플링 비율" hint="0~1. 수집기가 감당하지 못할 때만 1보다 낮추세요.">
        <input type="number" min={0} max={1} step={0.05} value={Number(value.sampleRatio??1)} onChange={e=>update('sampleRatio',Number(e.target.value))}/>
      </Field>
    </div>
    <div className="info-box"><Activity size={17}/><div><strong>수집기가 없으면 아무 비용도 들지 않습니다</strong><p>주소를 비워 두면 추적이 꺼진 상태로 동작하며 스팬을 만들지도, 버퍼에 쌓지도 않습니다. 설정은 <b>API와 워커를 재시작한 뒤</b> 적용되고, 적용되면 화면·로그·실행 기록에 표시되는 Trace ID로 수집기에서 같은 실행을 찾을 수 있습니다.</p></div></div>
  </Section>
  if(tab==='logging')return <Section title="로그 및 감사" description="서버 로그는 Control Center에서 검색하고 Runtime 로그와 구분해 확인할 수 있습니다."><Field label="로그 레벨"><select value={String(value.level??'info')} onChange={e=>update('level',e.target.value)}><option value="debug">Debug</option><option value="info">Info</option><option value="warn">Warn</option><option value="error">Error</option></select></Field><div className="info-box"><Network size={17}/><div><strong>보관 기간은 실행 제어에서 정합니다</strong><p>감사·실행·이벤트 기록의 보관 기간과 정리 실행은 <b>관리자 ▸ 실행 제어 ▸ 보관 정책</b> 한 곳에서 관리합니다. 같은 값을 두 화면에서 따로 정할 수 있게 두면 어느 쪽이 실제로 적용되는지 알 수 없습니다.</p></div></div><Toggle label="Runtime 로그 조회 사용" checked={value.includeRuntimeLogs!==false} change={v=>update('includeRuntimeLogs',v)}/></Section>
  return <Section title="Offline 운영" description="이 배포는 실행 중 외부로 나가지 않습니다. 켜고 끄는 설정이 아니라 빌드된 방식입니다."><div className="info-box"><ShieldCheck size={17}/><div><strong>업데이트 확인도, CDN도 없습니다</strong><p>컨트롤 플레인은 자기 버전을 확인하러 나가지 않고, 콘솔은 폰트·스크립트를 모두 이미지 안에서 불러옵니다. 예전에는 이 자리에 스위치가 두 개 있었지만 아무 동작도 바꾸지 않았습니다 — 끌 것이 없어서입니다. 런타임이 밖으로 나가는 범위는 <b>Network Profile</b> 로 정하고, 그 정책이 이 클러스터에서 실제로 적용되는지는 <b>보안 · 네트워크</b> 화면에서 확인할 수 있습니다.</p></div></div><div className="info-box"><Boxes size={17}/><div><strong>Release artifact</strong><p>agenthub-v버전.tar.gz와 agenthub-base-v버전.tar.gz 이미지 묶음만 Release에 게시하도록 자동화되어 있습니다.</p></div></div></Section>
}
type ProvisionedFile={path:string;content:string;mode?:string;description?:string;enabled?:boolean}
type ProvisionedVariable={name:string;value:string;description?:string;enabled?:boolean}
const PIP_SAMPLE=`[global]
index-url = https://nexus.company.local/repository/pypi-all/simple
trusted-host = nexus.company.local
timeout = 30`
const CONDA_SAMPLE=`channels:
  - https://nexus.company.local/repository/conda-forge
default_channels:
  - https://nexus.company.local/repository/conda-forge
channel_alias: https://nexus.company.local/repository/conda-proxy
show_channel_urls: true`
const SAMPLES:ProvisionedFile[]=[{path:'/etc/pip.conf',content:PIP_SAMPLE,mode:'0644',description:'사내 PyPI 미러',enabled:true},{path:'/home/agent/.condarc',content:CONDA_SAMPLE,mode:'0644',description:'사내 conda 채널',enabled:true},{path:'/etc/npmrc',content:'registry=https://nexus.company.local/repository/npm-all/\naudit=false\nfund=false',mode:'0644',description:'사내 npm 레지스트리',enabled:true}]

// 런타임 Pod 전체에 같은 파일과 환경변수를 배포합니다. 값은 ConfigMap으로 전달되므로
// 비밀값은 사용자 Secret이나 MCP Credential로 관리해야 합니다.
function RuntimeEnvironmentForm({value,update}:{value:Record<string,unknown>;update:(key:string,v:unknown)=>void}){
  const files=(value.files as ProvisionedFile[])??[]
  const variables=(value.variables as ProvisionedVariable[])??[]
  const setFiles=(next:ProvisionedFile[])=>update('files',next)
  const setVariables=(next:ProvisionedVariable[])=>update('variables',next)
  const patchFile=(index:number,patch:Partial<ProvisionedFile>)=>setFiles(files.map((file,i)=>i===index?{...file,...patch}:file))
  const patchVariable=(index:number,patch:Partial<ProvisionedVariable>)=>setVariables(variables.map((variable,i)=>i===index?{...variable,...patch}:variable))
  const addSample=(sample:ProvisionedFile)=>setFiles(files.some(file=>file.path===sample.path)?files:[...files,sample])
  return <>
    <Section title="공통 파일" description="활성화된 파일은 모든 Runtime Pod의 모든 컨테이너에 같은 경로로 읽기 전용 마운트됩니다.">
      <div className="info-box"><FileCog size={17}/><div><strong>ConfigMap으로 전달되고, 저장 즉시 반영됩니다</strong><p>비밀값은 넣지 마세요. <b>저장하면 실행 중인 Runtime에도 바로 적용되며, 내용이 바뀐 Pod는 새 설정으로 재시작됩니다.</b> 중지된 Runtime은 다음 시작 때 적용됩니다. /etc/agenthub, /usr/local/bin 등 플랫폼이 쓰는 경로는 사용할 수 없습니다.</p></div></div>
      <div className="provisioning-list">
        {files.map((file,index)=><article key={index}>
          <header>
            <input aria-label="파일 경로" value={file.path??''} onChange={e=>patchFile(index,{path:e.target.value})} placeholder="/etc/pip.conf"/>
            <input aria-label="권한" value={file.mode??''} onChange={e=>patchFile(index,{mode:e.target.value})} placeholder="0644"/>
            <label className="toggle-row"><span>사용</span><input type="checkbox" checked={file.enabled!==false} onChange={e=>patchFile(index,{enabled:e.target.checked})}/><i/></label>
            <button type="button" className="icon-button" aria-label="파일 삭제" onClick={()=>setFiles(files.filter((_,i)=>i!==index))}><Trash2 size={15}/></button>
          </header>
          <input aria-label="설명" value={file.description??''} onChange={e=>patchFile(index,{description:e.target.value})} placeholder="설명 (선택)"/>
          <textarea aria-label="파일 내용" rows={7} value={file.content??''} onChange={e=>patchFile(index,{content:e.target.value})} placeholder={PIP_SAMPLE}/>
        </article>)}
        {files.length===0&&<p className="empty-compact">등록된 공통 파일이 없습니다.</p>}
      </div>
      <div className="provisioning-actions">
        <button type="button" className="button ghost" onClick={()=>setFiles([...files,{path:'',content:'',mode:'0644',enabled:true}])}><Plus size={14}/>파일 추가</button>
        {SAMPLES.map(sample=><button type="button" className="button ghost" key={sample.path} onClick={()=>addSample(sample)}><Plus size={14}/>{sample.path}</button>)}
      </div>
    </Section>
    <Section title="공통 환경변수" description="Runtime Pod의 모든 컨테이너에 주입됩니다. 프록시나 패키지 인덱스 주소처럼 비밀이 아닌 값에만 사용하세요.">
      <div className="provisioning-list">
        {variables.map((variable,index)=><div className="provisioning-row" key={index}>
          <input aria-label="환경변수 이름" value={variable.name??''} onChange={e=>patchVariable(index,{name:e.target.value})} placeholder="PIP_INDEX_URL"/>
          <input aria-label="환경변수 값" value={variable.value??''} onChange={e=>patchVariable(index,{value:e.target.value})} placeholder="https://nexus.company.local/repository/pypi-all/simple"/>
          <label className="toggle-row"><span>사용</span><input type="checkbox" checked={variable.enabled!==false} onChange={e=>patchVariable(index,{enabled:e.target.checked})}/><i/></label>
          <button type="button" className="icon-button" aria-label="환경변수 삭제" onClick={()=>setVariables(variables.filter((_,i)=>i!==index))}><Trash2 size={15}/></button>
        </div>)}
        {variables.length===0&&<p className="empty-compact">등록된 공통 환경변수가 없습니다.</p>}
      </div>
      <div className="provisioning-actions">
        <button type="button" className="button ghost" onClick={()=>setVariables([...variables,{name:'',value:'',enabled:true}])}><Plus size={14}/>환경변수 추가</button>
      </div>
      <div className="info-box"><ShieldCheck size={17}/><div><strong>플랫폼 예약 이름</strong><p>HOME, PATH, OPENAI_*, AGENTHUB_* 등 런타임이 직접 쓰는 변수는 덮어쓸 수 없습니다.</p></div></div>
    </Section>
  </>
}
function Section({title,description='',children}:{title:string;description?:string;children:React.ReactNode}){return <section className="settings-section"><header><h2>{title}</h2><p>{description}</p></header><div className="settings-fields">{children}</div></section>}
function Field({label,hint,children}:{label:string;hint?:string;children:React.ReactNode}){return <label><span>{label}</span>{children}{hint&&<small>{hint}</small>}</label>}
function Toggle({label,checked,change}:{label:string;checked:boolean;change:(v:boolean)=>void}){return <label className="toggle-row"><span>{label}</span><input type="checkbox" checked={checked} onChange={e=>change(e.target.checked)}/><i/></label>}
function NumberField({label,name,value,update,hint}:{label:string;name:string;value:Record<string,unknown>;update:(key:string,v:unknown)=>void;hint?:string}){return <Field label={label} hint={hint}><input type="number" min="0" value={Number(value[name]??0)} onChange={e=>update(name,Number(e.target.value))}/></Field>}

type ClusterAnswer = {
  reachable: boolean
  detail?: string
  missing?: string[]
  check?: {
    serverVersion: string; namespace: string; namespaceFound: boolean
    crdExpected: boolean; crdInstalled: boolean; snapshotsInstalled: boolean; scope: string
    permissions: { what: string; allowed: boolean; reason?: string }[]
  }
}

/**
 * The Kubernetes settings, answered by Kubernetes.
 *
 * Saving the form proves the form was filled in. Whether the address answers,
 * the token is accepted, the namespace exists, the CRD is installed and this
 * account may do what the platform does were five separate questions with one
 * shared answer: a runtime that failed to start, hours later, for somebody else.
 */
function ClusterCheck() {
  const [answer, setAnswer] = useState<ClusterAnswer>()
  const [busy, setBusy] = useState(false)
  // What the last check found, if anyone has ever run one. Shown before the
  // button is pressed, because "nobody has ever checked" is the state this page
  // used to be silent about — and the one where a person debugs a runtime that
  // was never going to start.
  const [last, setLast] = useState<{ checked: boolean; health?: { reachable: boolean; detail?: string; checkedAt: string; missing?: string[] } }>()
  useEffect(() => {
    void api.get<{ checked: boolean; health?: { reachable: boolean; detail?: string; checkedAt: string; missing?: string[] } }>('/api/v1/admin/kubernetes/health')
      .then(setLast).catch(() => setLast(undefined))
  }, [])
  const ask = async () => {
    setBusy(true)
    try { setAnswer(await api.post<ClusterAnswer>('/api/v1/admin/kubernetes/check')) }
    catch (e) { setAnswer({ reachable: false, detail: e instanceof Error ? e.message : '확인하지 못했습니다.' }) }
    finally { setBusy(false) }
  }
  const check = answer?.check
  const missing = answer?.missing ?? []
  const tone = !answer ? '' : !answer.reachable ? 'danger' : missing.length > 0 ? 'warn' : 'ok'
  return <div className={`cluster-check ${tone}`}>
    <div>
      <strong>이 설정으로 실제로 되는지 확인합니다</strong>
      <p>주소가 응답하는지, 토큰이 받아들여지는지, 네임스페이스와 CRD가 있는지, 그리고 이 계정이 플랫폼이 하는 일을 할 수 있는지를 <b>클러스터에게 직접</b> 묻습니다.</p>
      {!answer && last && (last.checked && last.health
        ? <p className="cluster-check-detail">마지막 확인: {new Date(last.health.checkedAt).toLocaleString('ko-KR')} · {
            last.health.reachable
              ? (last.health.missing?.length ? `연결됨, 권한 부족: ${last.health.missing.join(', ')}` : '연결됨')
              : `연결되지 않음${last.health.detail ? ` (${last.health.detail})` : ''}`}</p>
        : <p className="cluster-check-detail">아직 한 번도 확인하지 않았습니다. 설정이 켜져 있다는 것과 클러스터가 응답한다는 것은 다른 이야기입니다.</p>)}
      {answer && !answer.reachable && <p className="cluster-check-detail">연결하지 못했습니다: {answer.detail}</p>}
      {check && <>
        <p className="cluster-check-detail">
          Kubernetes {check.serverVersion} · 네임스페이스 {check.namespace}{check.namespaceFound ? '' : ' (없음)'}
          {check.crdExpected && ` · AgentRuntime CRD ${check.crdInstalled ? '설치됨' : '없음'}`}
          {` · 작업공간 스냅샷 ${check.snapshotsInstalled ? '가능' : '불가 (클러스터에 VolumeSnapshot API가 없습니다)'}`}
        </p>
        {missing.length > 0
          ? <p className="cluster-check-detail">권한이 없습니다: {missing.join(', ')} — {check.scope} 계정의 Role을 확인해 주세요.</p>
          : <p className="cluster-check-detail">{check.scope}{subject(String(check.scope))} 하는 일 {check.permissions.length}가지 모두 허용되어 있습니다.</p>}
        <p className="cluster-check-note">권한은 <b>{check.scope}</b> 계정 기준입니다. Pod·볼륨·네트워크 정책은 오퍼레이터가 자기 계정으로 만들며, 클러스터는 물어본 계정에 대해서만 답합니다.</p>
      </>}
    </div>
    <button type="button" className="button ghost" disabled={busy} onClick={() => void ask()}>
      <Activity size={16}/>{busy ? '확인 중…' : '지금 확인'}
    </button>
  </div>
}

/**
 * Whether single sign-on will actually let anybody in.
 *
 * This is the setting that can lock a deployment out of itself: point the
 * platform at an issuer, turn local login off, save, and find out whether the
 * issuer was right by trying to log in from an account with no other way back.
 * The platform refuses to turn local login off until this check passes; the
 * button is here so somebody can see the answer before they get there.
 */
function SSOCheck() {
  const [answer, setAnswer] = useState<{ verdict: string; detail: string; client?: string }>()
  const [busy, setBusy] = useState(false)
  const ask = async () => {
    setBusy(true)
    try { setAnswer(await api.post('/api/v1/admin/authentication/check')) }
    catch (e) { setAnswer({ verdict: 'error', detail: e instanceof Error ? e.message : '확인하지 못했습니다.' }) }
    finally { setBusy(false) }
  }
  const tone = !answer ? '' : answer.verdict === 'ok' ? 'ok' : answer.verdict === 'unconfigured' ? 'warn' : 'danger'
  return <div className={`cluster-check ${tone}`}>
    <div>
      <strong>SSO가 실제로 로그인시켜 주는지 확인합니다</strong>
      <p>Discovery 문서를 읽어 Issuer가 스스로 밝히는 주소와 일치하는지 보고, Client Secret이 저장돼 있으면 제공자가 그 자격을 인정하는지까지 확인합니다. <b>저장된 설정</b> 기준이므로 방금 입력한 값은 먼저 저장한 뒤 확인하세요.</p>
      {answer && <p className="cluster-check-detail">{answer.detail}{answer.client ? ` (Client: ${answer.client})` : ''}</p>}
      <p className="cluster-check-note">로컬 로그인을 끄려면 이 확인이 통과해야 합니다 — OIDC를 켰다는 것과 OIDC가 동작한다는 것은 다르고, 둘을 혼동하면 아무도 들어올 수 없게 됩니다.</p>
    </div>
    <button type="button" className="button ghost" disabled={busy} onClick={() => void ask()}>
      <KeyRound size={16}/>{busy ? '확인 중…' : '지금 확인'}
    </button>
  </div>
}
