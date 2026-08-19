import { useCallback, useEffect, useState } from 'react'
import { Check, CircleAlert, Clock3, Plus, RefreshCw, Save, Trash2 } from 'lucide-react'
import { api } from '../api'
import { ErrorBanner, GuidePanel, Loading, PageHeader, SuccessBanner } from '../components/UI'
import { relativeTime, runtimeLabel, runtimeLogoClass } from '../runtime'

/**
 * What every runtime of one type starts with — and proof that it did.
 *
 * The platform generates each runtime's own configuration file. Anything else a
 * site needed had nowhere to go, because a second copy of that file would fight
 * the generated one. These overlays merge into it, are re-applied on every start,
 * and come back reported from inside the Pod: "저장했다" and "런타임이 그 설정으로
 * 돌고 있다" are different claims, and only the second one is worth a screen.
 */

type Suggestion = { target: string; key?: string; label: string; description: string; example?: string; runtimeTypes?: string[]; verified: boolean }
type Profile = { runtimeType: string; config?: Record<string, unknown>; env?: Record<string, string>; description?: string; enabled?: boolean }
type Loaded = { settings: { profiles: Profile[] }; suggestions: Suggestion[]; runtimes: { type: string; label: string; summary: string }[] }
type Report = { fingerprint: string; status: string; detail?: string; file?: string; keys: string[]; reportedAt: string }
type Status = {
  agentId: string; agentName: string; runtimeId: string; runtimeType: string; runtimeStatus: string
  expectedFingerprint: string; reported: boolean; report: Report | null; state: string
}

const STATE_LABELS: Record<string, string> = {
  none: '설정 없음', pending_start: '시작 대기', unverified: '확인 안 됨',
  stale: '이전 설정으로 실행 중', failed: '적용 실패', applied: '적용됨',
}
const STATE_HINTS: Record<string, string> = {
  none: '이 런타임 유형에는 설정이 없습니다.',
  pending_start: '런타임이 꺼져 있습니다. 시작하면 주입되고 결과가 보고됩니다.',
  unverified: '아직 보고가 없습니다. 설정을 저장한 뒤 재시작되지 않았거나, 컨트롤 플레인에 보고가 도달하지 못했습니다.',
  stale: '실행 중인 Pod는 이전 설정으로 시작했습니다. 재시작하면 최신 설정이 적용됩니다.',
  failed: '런타임이 설정 파일을 읽거나 쓰지 못했다고 보고했습니다. 상세를 확인하세요.',
  applied: '실행 중인 Pod가 현재 설정으로 시작했다고 보고했습니다.',
}

export function AdminRuntimeSettings() {
  const [loaded, setLoaded] = useState<Loaded | null>(null)
  const [profiles, setProfiles] = useState<Profile[]>([])
  const [tab, setTab] = useState('opencode')
  const [status, setStatus] = useState<Status[]>([])
  const [dirty, setDirty] = useState(false)
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const [busy, setBusy] = useState(false)
  const [configText, setConfigText] = useState('{}')

  const load = useCallback(async () => {
    try {
      const [document, reported] = await Promise.all([
        api.get<Loaded>('/api/v1/admin/runtime-settings'),
        api.get<{ items: Status[] }>('/api/v1/admin/runtime-settings/status'),
      ])
      setLoaded(document)
      setProfiles(document.settings.profiles ?? [])
      setStatus(reported.items ?? [])
      setDirty(false)
    } catch (e) { setError(e instanceof Error ? e.message : '런타임 설정을 불러오지 못했습니다.') }
  }, [])
  useEffect(() => { void load() }, [load])

  const profile = profiles.find((item) => item.runtimeType === tab) ?? { runtimeType: tab }
  useEffect(() => { setConfigText(JSON.stringify(profile.config ?? {}, null, 2)) }, [tab, loaded]) // eslint-disable-line react-hooks/exhaustive-deps

  const change = (next: Profile) => {
    setProfiles((current) => {
      const rest = current.filter((item) => item.runtimeType !== next.runtimeType)
      return next.config || next.env || next.description ? [...rest, next] : rest
    })
    setDirty(true); setNotice('')
  }
  const setEnv = (name: string, value: string | null) => {
    const env = { ...(profile.env ?? {}) }
    if (value === null) delete env[name]
    else env[name] = value
    change({ ...profile, env })
  }
  const applyConfigText = (text: string) => {
    setConfigText(text)
    try {
      const parsed = text.trim() === '' ? {} : JSON.parse(text)
      change({ ...profile, config: parsed })
      setError('')
    } catch { /* left to the save, so typing is not interrupted */ }
  }
  const addSuggestion = (item: Suggestion) => {
    if (!item.key) return
    if (item.target === 'env') { setEnv(item.key, item.example ? JSON.parse(item.example) : ''); return }
    // A dotted key sets one field rather than replacing the section it lives in.
    const value = item.example ? JSON.parse(item.example) : ''
    const config = { ...(profile.config ?? {}) } as Record<string, unknown>
    const parts = item.key.split('.')
    let cursor = config
    parts.forEach((part, index) => {
      if (index === parts.length - 1) { cursor[part] = value; return }
      cursor[part] = typeof cursor[part] === 'object' && cursor[part] ? { ...(cursor[part] as object) } : {}
      cursor = cursor[part] as Record<string, unknown>
    })
    change({ ...profile, config })
    setConfigText(JSON.stringify(config, null, 2))
  }
  const save = async () => {
    setBusy(true); setError('')
    try {
      JSON.parse(configText || '{}')
      const result = await api.put<{ message: string }>('/api/v1/admin/runtime-settings', { profiles })
      setNotice(result.message)
      await load()
    } catch (e) {
      setError(e instanceof SyntaxError ? `설정 JSON을 읽지 못했습니다: ${e.message}` : e instanceof Error ? e.message : '저장하지 못했습니다.')
    } finally { setBusy(false) }
  }

  if (!loaded) return <div className="page">{error ? <ErrorBanner message={error} /> : <Loading />}</div>
  const suggestions = loaded.suggestions.filter((item) => !item.runtimeTypes?.length || item.runtimeTypes.includes(tab))
  const forTab = status.filter((item) => item.runtimeType === tab)
  return <div className="page">
    <PageHeader eyebrow="관리자 · 런타임" title="런타임 설정 주입"
      description="런타임 유형별로 언어·시간대·제품 옵션을 정의하면, 기동·재기동할 때마다 런타임이 읽는 설정 파일에 병합됩니다. 주입 결과는 Pod가 직접 보고합니다."
      actions={<>
        <button className="button ghost" onClick={() => void load()}><RefreshCw size={16} />새로고침</button>
        <button className="button primary" disabled={busy || !dirty} onClick={() => void save()}><Save size={16} />설정 저장</button>
      </>} />
    <GuidePanel id="admin-runtime-settings" title="어떻게 주입되고, 어떻게 확인하나요" steps={[
      { title: '플랫폼이 만드는 설정 위에 얹힙니다', body: '모델 연결·MCP 도구·터미널 경로는 플랫폼이 생성합니다. 여기서 정의한 값은 그 파일에 키 단위로 병합되며, 플랫폼이 소유한 키(model, mcp, provider 등)는 덮어쓸 수 없습니다.' },
      { title: '기동·재기동마다 다시 주입됩니다', body: '설정을 저장하면 실행 중인 런타임의 정의를 다시 쓰고, 내용이 바뀐 Pod가 재시작되면서 초기화 컨테이너가 병합을 다시 수행합니다.' },
      { title: 'Pod가 직접 보고합니다', body: '초기화 컨테이너가 자신이 쓴 파일을 되읽어 어떤 키가 들어갔는지 컨트롤 플레인에 보고합니다. 값은 보고하지 않습니다 — 내부 주소나 라이선스 문자열이 담길 수 있기 때문입니다.' },
      { title: '키를 모르면 넣지 마세요, 대신 확인하세요', body: '아래 제안 중 "확인 필요"로 표시된 것은 런타임 제품 버전마다 키가 달라 플랫폼이 단정할 수 없는 항목입니다. 문서에서 키를 확인해 넣으면, 파일에 반영됐는지는 아래 주입 상태로 확인할 수 있습니다.' },
    ]} />
    {error && <ErrorBanner message={error} onClose={() => setError('')} />}
    {notice && <SuccessBanner message={notice} />}

    <div className="tabs">
      {loaded.runtimes.map((item) => <button key={item.type} className={tab === item.type ? 'active' : ''} onClick={() => setTab(item.type)}>
        {item.label}{profiles.some((p) => p.runtimeType === item.type) && <span>설정됨</span>}
      </button>)}
    </div>

    <section className="panel ops-panel">
      <label className="drawer-form"><span>이 프로파일에 대한 메모</span>
        <input value={profile.description ?? ''} onChange={(e) => change({ ...profile, description: e.target.value })} placeholder="예: 국내 폐쇄망 기본값 — 로케일·시간대·프록시" />
      </label>
      <h3>환경변수</h3>
      <p className="field-hint">{runtimeLabel(tab)} 런타임의 모든 컨테이너에 적용됩니다. 플랫폼이 설정하는 변수(AGENTHUB_*, OPENAI_*, PATH 등)는 덮어쓸 수 없습니다.</p>
      <table className="settings-table">
        <thead><tr><th>이름</th><th>값</th><th /></tr></thead>
        <tbody>
          {Object.entries(profile.env ?? {}).map(([name, value]) => <tr key={name}>
            <td><code>{name}</code></td>
            <td><input value={value} onChange={(e) => setEnv(name, e.target.value)} /></td>
            <td><button className="icon-button danger" title="삭제" onClick={() => setEnv(name, null)}><Trash2 size={15} /></button></td>
          </tr>)}
          <NewEnvRow add={(name, value) => setEnv(name, value)} />
        </tbody>
      </table>
      <h3>설정 파일 병합 (JSON)</h3>
      <p className="field-hint">{runtimeLabel(tab)} 이 읽는 설정 파일에 병합됩니다. 객체는 키 단위로 병합되고, 값·배열은 대체됩니다.</p>
      <textarea className="policy-json" style={{ minHeight: 200 }} value={configText} spellCheck={false} onChange={(e) => applyConfigText(e.target.value)} />
    </section>

    <section className="panel ops-panel">
      <h3>자주 쓰는 설정</h3>
      <div className="suggestion-grid">
        {suggestions.map((item) => <article key={`${item.target}-${item.label}`} className={item.verified ? 'verified' : ''}>
          <header>
            <strong>{item.label}</strong>
            <span className={`version-tag ${item.verified ? 'passed' : 'muted'}`}>{item.verified ? '확인됨' : '확인 필요'}</span>
          </header>
          {item.key && <code>{item.target === 'env' ? item.key : `config.${item.key}`}{item.example ? ` = ${item.example}` : ''}</code>}
          <p>{item.description}</p>
          {item.key && <button className="button ghost" onClick={() => addSuggestion(item)}><Plus size={14} />추가</button>}
        </article>)}
      </div>
    </section>

    <section className="table-panel">
      <div className="audit-filters"><strong>주입 상태 — {runtimeLabel(tab)}</strong>
        <span className="field-hint">Pod가 시작할 때 스스로 보고한 결과입니다. 값은 보고되지 않고 키만 기록됩니다.</span>
      </div>
      {forTab.length === 0
        ? <div className="empty-compact">이 유형으로 실행된 런타임이 없습니다.</div>
        : <div className="table-wrap custom-scroll"><table>
            <thead><tr><th>에이전트</th><th>런타임 상태</th><th>주입 상태</th><th>보고된 키</th><th>보고 시각</th></tr></thead>
            <tbody>{forTab.map((item) => <tr key={item.runtimeId}>
              <td><div className="task-agent"><div className={runtimeLogoClass(item.runtimeType)}>{item.runtimeType.slice(0, 2).toUpperCase()}</div><span>{item.agentName}</span></div></td>
              <td>{item.runtimeStatus || '—'}</td>
              <td>
                <span className={`inject-badge ${item.state}`} title={STATE_HINTS[item.state]}>
                  {item.state === 'applied' ? <Check size={13} /> : item.state === 'failed' ? <CircleAlert size={13} /> : <Clock3 size={13} />}
                  {STATE_LABELS[item.state] ?? item.state}
                </span>
                {item.report?.detail && <small className="field-hint">{item.report.detail}</small>}
              </td>
              <td>{item.report?.keys?.length ? <span className="rule-scope">{item.report.keys.join(', ')}</span> : <span className="muted-cell">—</span>}</td>
              <td>{item.report ? <span title={new Date(item.report.reportedAt).toLocaleString('ko-KR')}>{relativeTime(item.report.reportedAt)}</span> : <span className="muted-cell">보고 없음</span>}</td>
            </tr>)}</tbody>
          </table></div>}
    </section>
  </div>
}

/** Adding a variable is two fields and a button, not a modal. */
function NewEnvRow({ add }: { add: (name: string, value: string) => void }) {
  const [name, setName] = useState('')
  const [value, setValue] = useState('')
  const submit = () => {
    if (!name.trim()) return
    add(name.trim().toUpperCase(), value)
    setName(''); setValue('')
  }
  return <tr>
    <td><input value={name} onChange={(e) => setName(e.target.value)} placeholder="LANG" onKeyDown={(e) => { if (e.key === 'Enter') submit() }} /></td>
    <td><input value={value} onChange={(e) => setValue(e.target.value)} placeholder="ko_KR.UTF-8" onKeyDown={(e) => { if (e.key === 'Enter') submit() }} /></td>
    <td><button className="icon-button" title="추가" onClick={submit}><Plus size={15} /></button></td>
  </tr>
}
