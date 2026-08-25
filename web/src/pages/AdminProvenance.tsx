import { useCallback, useEffect, useState } from 'react'
import { KeyRound, Save, Send, Share2 } from 'lucide-react'
import { Link } from 'react-router-dom'
import { api, ApiError } from '../api'
import { ErrorBanner, GuidePanel, Loading, PageHeader, SuccessBanner } from '../components/UI'

/**
 * Where this deployment sends its account of what it decided.
 *
 * The platform writes down what happened in great detail and can answer
 * questions about one run. The questions that cross runs — which decisions did
 * this policy change touch, what has this agent concluded before — belong to
 * whatever a site already uses for that. So the export is a plain address, and
 * the shape of the record is the platform's own.
 */

type Loaded = { endpoint: string; header: string; tokenConfigured: boolean; events: string[] }

const ENDING_LABELS: Record<string, string> = {
  'task.completed': '완료',
  'task.failed': '실패',
  'task.dead_lettered': '처리 불가 (플랫폼이 포기)',
}

export function AdminProvenance() {
  const [loaded, setLoaded] = useState<Loaded | null>(null)
  const [endpoint, setEndpoint] = useState('')
  const [header, setHeader] = useState('')
  const [token, setToken] = useState('')
  const [dirty, setDirty] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const [sent, setSent] = useState('')

  const load = useCallback(async () => {
    try {
      const value = await api.get<Loaded>('/api/v1/admin/provenance')
      setLoaded(value)
      setEndpoint(value.endpoint)
      setHeader(value.header)
      setToken('')
      setDirty(false)
    } catch (e) { setError(e instanceof Error ? e.message : '설정을 불러오지 못했습니다.') }
  }, [])
  useEffect(() => { void load() }, [load])

  const change = (apply: () => void) => { apply(); setDirty(true); setNotice(''); setSent('') }
  const body = () => ({ endpoint: endpoint.trim(), header: header.trim(), token })

  const save = async () => {
    setBusy(true); setError('')
    try {
      await api.put<{ endpoint: string }>('/api/v1/admin/provenance', body())
      setNotice(endpoint.trim() ? '결정 기록을 이 주소로 보냅니다.' : '결정 기록을 보내지 않습니다.')
      await load()
    } catch (e) { setError(e instanceof Error ? e.message : '설정을 저장하지 못했습니다.') }
    finally { setBusy(false) }
  }

  const test = async () => {
    setBusy(true); setError(''); setSent('')
    try {
      const result = await api.post<{ message: string }>('/api/v1/admin/provenance/test', body())
      setSent(result.message)
    } catch (e) {
      // The receiver's own words, not a generic failure: "HTTP 404" and "no such
      // host" send an operator to different places. The server's sentence already
      // says what failed, so nothing is prepended to it — measured on screen, the
      // banner read "보내지 못했습니다 — 결정 기록을 보내지 못했습니다: no such host".
      setError(e instanceof ApiError || e instanceof Error ? e.message : '보내지 못했습니다.')
    }
    finally { setBusy(false) }
  }

  if (!loaded) return <div className="page">{error ? <ErrorBanner message={error} /> : <Loading />}</div>
  const on = Boolean(loaded.endpoint)
  return <div className="page">
    <PageHeader eyebrow="관리자 · 거버넌스" title="결정 기록 내보내기"
      description="작업이 끝날 때마다 무엇을 왜 그렇게 결정했는지 한 건씩 외부 시스템으로 보냅니다. 지식 그래프, 데이터 웨어하우스, 감사용 보관소 무엇이든 HTTP로 받을 수 있으면 됩니다."
      actions={<>
        <button className="button" disabled={busy || !endpoint.trim()} onClick={() => void test()}><Send size={16} />보내보기</button>
        <button className="button primary" disabled={busy || !dirty} onClick={() => void save()}><Save size={16} />설정 저장</button>
      </>} />
    <GuidePanel id="admin-provenance" title="어떻게 동작하나요" steps={[
      { title: '보내는 시점', body: <>작업이 <b>완료 · 실패 · 처리 불가</b>로 끝날 때 한 건씩 보냅니다. 실패했다가 재시도로 성공한 작업은 <b>두 건</b>이 됩니다 — 시도 하나가 결정 하나이고, 두 건은 같은 <code>taskId</code>로 묶입니다.</> },
      { title: '담기는 내용', body: <>결과·근거·에이전트와 <b>실제로 실행된</b> 버전·모델·런타임 이미지, 그리고 사람이 중간에 승인했다면 그 승인 ID까지. 지금 설정이 아니라 그때 실행된 것을 적습니다.</> },
      { title: '근거의 출처', body: <>근거는 에이전트가 "성공했다"고 말한 문장이 아니라 <Link to="/admin/operations">플랫폼이 판정한 결과</Link>입니다. 둘은 다르고, 이 플랫폼은 그 둘을 구분해서 기록합니다.</> },
      { title: '나가기 전 내용 검사', body: <><Link to="/admin/dlp">내용 검사</Link> 설정이 이 전송에도 적용됩니다. <b>가리고 전송</b>이면 값이 가려진 채로 나가고, <b>차단</b>이면 그 기록은 보내지 않고 <Link to="/admin/operations">감사 로그</Link>에 <code>provenance.withheld</code>로 남습니다(재시도하지 않습니다).</> },
      { title: '받는 쪽이 죽었을 때', body: '전송 실패는 이벤트 재시도 대상이 됩니다. 계속 실패하면 그 이벤트가 처리 불가로 남아 알림이 가고, 작업 자체는 영향을 받지 않습니다.' },
    ]} />
    {error && <ErrorBanner message={error} onClose={() => setError('')} />}
    {notice && <SuccessBanner message={notice} />}
    {sent && <SuccessBanner message={sent} />}

    <section className="panel ops-panel">
      <h3><Share2 size={16} /> 받는 주소</h3>
      <div className="ops-form">
        <label><span>주소</span>
          <input type="url" placeholder="https://graph.example.com/decisions" value={endpoint}
            onChange={(e) => change(() => setEndpoint(e.target.value))} />
          <small>비워 두면 아무것도 보내지 않습니다. 이 주소로 JSON 한 건이 POST 됩니다.</small>
        </label>
      </div>
      <p className="field-hint">
        현재 상태: <b>{on ? '내보내는 중' : '보내지 않음'}</b>
        {on && <> · 대상 결말 {loaded.events.map((event) => ENDING_LABELS[event] ?? event).join(' · ')}</>}
      </p>
    </section>

    <section className="panel ops-panel">
      <h3><KeyRound size={16} /> 인증 헤더 (선택)</h3>
      <div className="ops-form">
        <label><span>헤더 이름</span>
          <input placeholder="Authorization" value={header} onChange={(e) => change(() => setHeader(e.target.value))} />
        </label>
        <label><span>헤더 값</span>
          <input type="password" autoComplete="new-password"
            placeholder={loaded.tokenConfigured ? '저장되어 있습니다 (바꿀 때만 입력)' : 'Bearer …'}
            value={token} onChange={(e) => change(() => setToken(e.target.value))} />
          <small>{loaded.tokenConfigured ? '저장된 값은 다시 보여주지 않습니다. 비워 두면 그대로 유지됩니다.' : '헤더 이름과 값은 함께 지정하거나 함께 비워 주세요.'}</small>
        </label>
      </div>
    </section>

    <section className="panel ops-panel">
      <h3><Send size={16} /> 먼저 확인하기</h3>
      <p className="field-hint">
        <b>보내보기</b>는 화면에 입력한(저장 전) 주소로 <code>outcome: "test"</code>인 샘플 한 건을 지금 보내고, 받는 쪽의 답을 그대로 보여줍니다.
        주소를 잘못 적으면 실제 기록은 조용히 사라지는 게 아니라 이벤트 재시도로 쌓이지만, 그걸 알아차리는 건 한참 뒤입니다. 저장 전에 한 번 눌러 보세요.
      </p>
    </section>
  </div>
}
