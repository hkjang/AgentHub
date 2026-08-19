import { FormEvent, useCallback, useEffect, useState } from 'react'
import { Eye, FlaskConical, Save, ShieldAlert } from 'lucide-react'
import { Link } from 'react-router-dom'
import { api } from '../api'
import { ErrorBanner, GuidePanel, Loading, PageHeader, SuccessBanner } from '../components/UI'

/**
 * The content scanner.
 *
 * An agent is a program that reads whatever it is pointed at and then sends it
 * somewhere else — to a model, to a ticket, to a chat message. On an offline site
 * that is the whole risk: the data never had to leave the building until an agent
 * summarised it into a prompt. This screen decides what is looked for and what
 * happens when it is found.
 */

type Detector = { class: string; label: string; description: string; action: string }
type Settings = { enabled: boolean; classes?: Record<string, string>; scanResponses?: boolean; maxBytes?: number }
type Loaded = { settings: Settings; detectors: Detector[]; actions: string[]; defaultMaxBytes: number }
type Finding = { class: string; label: string; count: number; action: string; sample: string }
type ScanResult = { findings?: Finding[]; text: string; blocked?: boolean; reason?: string; truncated?: boolean }

const ACTION_LABELS: Record<string, string> = { off: '검사 안 함', audit: '기록만', redact: '가리고 전송', block: '차단' }
const ACTION_HINTS: Record<string, string> = {
  off: '이 등급은 찾지 않습니다.',
  audit: '찾으면 감사 로그에만 남기고 그대로 보냅니다. 무엇이 오가는지 먼저 파악할 때 씁니다.',
  redact: '찾은 값을 "[등급 삭제됨]"으로 바꿔 보냅니다. 모델 호출에 권장합니다.',
  block: '호출 자체를 거절합니다. 외부 시스템에 쓰는 도구에 권장합니다.',
}

const SAMPLE = '고객 홍길동(900101-1234568), 연락처 010-1234-5678, 카드 4111-1111-1111-1111, 메일 hong@example.co.kr'

export function AdminDLP() {
  const [loaded, setLoaded] = useState<Loaded | null>(null)
  const [settings, setSettings] = useState<Settings>({ enabled: false, classes: {} })
  const [dirty, setDirty] = useState(false)
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const [busy, setBusy] = useState(false)
  const [sample, setSample] = useState(SAMPLE)
  const [result, setResult] = useState<ScanResult | null>(null)

  const load = useCallback(async () => {
    try {
      const value = await api.get<Loaded>('/api/v1/admin/dlp')
      setLoaded(value)
      setSettings({ enabled: value.settings.enabled, classes: { ...(value.settings.classes ?? {}) }, scanResponses: value.settings.scanResponses, maxBytes: value.settings.maxBytes })
      setDirty(false)
    } catch (e) { setError(e instanceof Error ? e.message : '설정을 불러오지 못했습니다.') }
  }, [])
  useEffect(() => { void load() }, [load])

  const change = (next: Settings) => { setSettings(next); setDirty(true); setNotice('') }
  const setAction = (className: string, action: string) => {
    const classes = { ...(settings.classes ?? {}) }
    if (action === 'off') delete classes[className]
    else classes[className] = action
    change({ ...settings, classes })
  }
  const save = async () => {
    setBusy(true); setError('')
    try {
      const saved = await api.put<{ message: string }>('/api/v1/admin/dlp', settings)
      setNotice(saved.message); setDirty(false); await load()
    } catch (e) { setError(e instanceof Error ? e.message : '설정을 저장하지 못했습니다.') }
    finally { setBusy(false) }
  }
  const scan = async (event: FormEvent) => {
    event.preventDefault(); setError('')
    try { setResult(await api.post<ScanResult>('/api/v1/admin/dlp/scan', { text: sample, settings })) }
    catch (e) { setError(e instanceof Error ? e.message : '검사하지 못했습니다.'); setResult(null) }
  }

  if (!loaded) return <div className="page">{error ? <ErrorBanner message={error} /> : <Loading />}</div>
  const configured = Object.keys(settings.classes ?? {}).length
  return <div className="page">
    <PageHeader eyebrow="관리자 · 거버넌스" title="내용 검사 (DLP)"
      description="모델 호출과 MCP 도구 호출에 포함된 개인정보·신용정보·자격증명을 찾아 가리거나 차단합니다. 찾은 값은 어디에도 저장하지 않습니다."
      actions={<button className="button primary" disabled={busy || !dirty} onClick={() => void save()}><Save size={16} />설정 저장</button>} />
    <GuidePanel id="admin-dlp" title="어떻게 동작하나요" steps={[
      { title: '검사 지점', body: <>모델로 나가는 프롬프트는 컨트롤 플레인이, MCP 도구 호출은 Pod 안 게이트웨이가 검사합니다. 도구 호출은 컨트롤 플레인을 거치지 않기 때문에, 에이전트가 우회할 수 없는 자리에서 검사해야 의미가 있습니다.</> },
      { title: '오탐을 줄이는 방식', body: '주민등록번호·사업자등록번호는 체크섬, 카드번호는 Luhn 검증을 통과한 값만 보고합니다. 자꾸 헛경보를 내는 검사기는 일주일 안에 꺼지기 때문입니다.' },
      { title: '기록되는 것', body: '감사 로그에는 등급·건수·마스킹된 예시만 남습니다. 찾은 값 자체는 저장하지도, 로그에 쓰지도 않습니다.' },
      { title: '정책과 함께 쓰기', body: <>특정 에이전트·역할만 더 엄격하게 막으려면 <Link to="/admin/policy">정책</Link>에서 데이터 등급 조건(<code>rrn</code> 등)으로 규칙을 만드세요. 등급을 찾으면 그 규칙이 함께 판정합니다.</> },
    ]} />
    {error && <ErrorBanner message={error} onClose={() => setError('')} />}
    {notice && <SuccessBanner message={notice} />}

    <section className="panel ops-panel">
      <label className="toggle-row"><span>내용 검사 사용</span><input type="checkbox" checked={settings.enabled} onChange={(e) => change({ ...settings, enabled: e.target.checked })} /><i /></label>
      <p className="field-hint">껐을 때는 아무 텍스트도 검사하지 않습니다. 등급 설정은 유지되므로 다시 켜면 그대로 적용됩니다. 현재 {configured}개 등급이 설정되어 있습니다.</p>
      <div className="ops-form">
        <label className="toggle-row"><span>응답도 검사</span><input type="checkbox" checked={Boolean(settings.scanResponses)} onChange={(e) => change({ ...settings, scanResponses: e.target.checked })} /><i /></label>
        <label><span>검사 크기 상한 (바이트)</span>
          <input type="number" min={0} max={4194304} value={settings.maxBytes ?? 0} onChange={(e) => change({ ...settings, maxBytes: Number(e.target.value) })} />
          <small>0이면 기본값 {loaded.defaultMaxBytes.toLocaleString('ko-KR')} 바이트</small>
        </label>
      </div>
      <p className="field-hint">응답 검사는 모델·도구가 되돌려 준 내용까지 확인합니다. 비용이 큰 쪽이라 기본은 꺼져 있습니다.</p>
    </section>

    <section className="table-panel">
      <div className="table-wrap custom-scroll"><table>
        <thead><tr><th>데이터 등급</th><th>탐지 방식</th><th>처리</th></tr></thead>
        <tbody>{loaded.detectors.map((detector) => {
          const action = (settings.classes ?? {})[detector.class] ?? 'off'
          return <tr key={detector.class}>
            <td><div className="mono-stack"><strong>{detector.label}</strong><small><code>{detector.class}</code></small></div></td>
            <td><span className="rule-scope">{detector.description}</span></td>
            <td>
              <select value={action} onChange={(e) => setAction(detector.class, e.target.value)} aria-label={`${detector.label} 처리 방식`}>
                {loaded.actions.map((value) => <option key={value} value={value}>{ACTION_LABELS[value] ?? value}</option>)}
              </select>
              <small className="field-hint">{ACTION_HINTS[action]}</small>
            </td>
          </tr>
        })}</tbody>
      </table></div>
    </section>

    <section className="panel ops-panel">
      <h3><FlaskConical size={16} /> 샘플로 확인</h3>
      <p className="field-hint">"이런 값도 잡히나요"를 직접 확인합니다. 지금 화면의(저장 전) 설정으로 검사하며, 붙여넣은 내용은 저장하지 않습니다.</p>
      <form onSubmit={scan}>
        <textarea className="policy-json" style={{ minHeight: 120 }} value={sample} onChange={(e) => setSample(e.target.value)} />
        <div className="ops-actions"><button className="button primary"><Eye size={15} />검사</button></div>
      </form>
      {result && <div className={`simulation ${result.blocked ? 'deny' : (result.findings?.length ? 'require_approval' : 'allow')}`}>
        <strong>{result.blocked ? '차단' : result.findings?.length ? `${result.findings.length}개 등급 발견` : '발견된 민감정보 없음'}</strong>
        {result.reason && <span>{result.reason}</span>}
        {result.findings?.length ? <ul className="finding-list">{result.findings.map((finding) => <li key={finding.class}>
          <span className={`effect-badge ${finding.action === 'block' ? 'deny' : finding.action === 'redact' ? 'require_approval' : 'allow'}`}>{ACTION_LABELS[finding.action] ?? finding.action}</span>
          <b>{finding.label}</b> {finding.count}건 · 예시 <code>{finding.sample}</code>
        </li>)}</ul> : null}
        {result.truncated && <small>검사 크기 상한을 넘어 앞부분만 검사했습니다.</small>}
        {!result.blocked && result.findings?.length ? <>
          <small>전송될 내용:</small>
          <pre className="runtime-log-preview custom-scroll">{result.text}</pre>
        </> : null}
      </div>}
    </section>

    <section className="panel ops-panel">
      <h3><ShieldAlert size={16} /> 적용 시점</h3>
      <p className="field-hint">
        모델 호출 검사는 저장 후 <b>5초 안에</b> 적용됩니다. 도구 호출 검사는 설정이 Pod로 전달되어야 하므로, 저장 시 실행 중인 런타임 정의를 다시 쓰고 해당 Pod가 재시작된 뒤 적용됩니다.
        발견 기록은 <Link to="/admin/operations">로그 · 감사</Link>에서 <code>dlp.model</code>, <code>dlp.tool</code> 동작으로 검색할 수 있습니다.
      </p>
    </section>
  </div>
}
