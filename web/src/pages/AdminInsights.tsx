import { useCallback, useEffect, useState } from 'react'
import { AlertTriangle, Coins, Download, Gauge, RefreshCw, ShieldAlert, Stethoscope, Users } from 'lucide-react'
import { Link } from 'react-router-dom'
import { api } from '../api'
import { ErrorBanner, Loading, PageHeader, statusLabel } from '../components/UI'

/**
 * What the deployment is doing, in one place.
 *
 * Every figure here was already in the database, but only ever shown as one
 * person's slice of it, so "is the platform healthy, who is spending what, and
 * what is stuck" took five screens and mental arithmetic. Nobody does that at
 * 2am, which is when it matters.
 */

type SpendRow = { id: string; name: string; runs: number; inputTokens: number; outputTokens: number; cost: number; priced: boolean }
type UsagePoint = { day: string; inputTokens: number; outputTokens: number; cost: number }
type Overview = {
  from: string; to: string
  users: { total: number; active: number; admins: number; managers: number; disabled: number; neverUsed: number }
  agents: { total: number; running: number; stopped: number; failed: number; warm: number; autonomy: number; gated: number; unpromoted: number }
  execution: { tasks: Record<string, number>; runs: number; completed: number; failed: number; deadLetter: number; blocked: number; retried: number; successRate: number; medianDurationMs: number; p95DurationMs: number }
  queue: { ready: number; running: number; workers: number; status: Record<string, number> }
  events: { pending: number; retrying: number; deadLetter: number; delivered: number; oldestPendingSeconds: number }
  spend: { currency: string; inputTokens: number; outputTokens: number; cost: number; unpricedTokens: number; runs: number; unmeteredRuns: number; users: SpendRow[]; agents: SpendRow[]; models: SpendRow[]; daily: UsagePoint[] }
  oldestQueuedSeconds: number
  quota: { windowDays: number; maxRunning: number; tokenBudget: number; costBudget: number }
  quotaPressure?: { id: string; name: string; limit: string; used: number; allowed: number; percent: number }[]
  paused: { paused: boolean; reason: string; by: string; at?: string }
  workers: { live: number; stale: number; paused: number; stopped: number; capacity: number }
}

const WINDOWS = [{ days: 1, label: '24시간' }, { days: 7, label: '7일' }, { days: 30, label: '30일' }]

const number = (value: number) => value.toLocaleString('ko-KR')
const money = (value: number, currency: string) => `${value.toLocaleString('ko-KR', { maximumFractionDigits: 2 })} ${currency}`

/** Durations read as time, not as a six-digit millisecond count. */
function duration(ms: number) {
  if (ms <= 0) return '—'
  if (ms < 1000) return `${ms}ms`
  if (ms < 60000) return `${(ms / 1000).toFixed(1)}초`
  return `${Math.floor(ms / 60000)}분 ${Math.round((ms % 60000) / 1000)}초`
}

function waited(seconds: number) {
  if (seconds <= 0) return '없음'
  if (seconds < 60) return `${Math.round(seconds)}초`
  if (seconds < 3600) return `${Math.round(seconds / 60)}분`
  return `${Math.floor(seconds / 3600)}시간 ${Math.round((seconds % 3600) / 60)}분`
}

/**
 * What needs a person, derived rather than configured.
 *
 * An operations screen that only shows totals leaves the reader to notice the
 * one number that is wrong. These are the states that mean somebody has to act,
 * each with the screen that acts on it.
 */
function attention(overview: Overview) {
  const items: { text: string; to: string; level: 'warn' | 'danger' }[] = []
  const { execution, events, agents, queue } = overview
  if (execution.blocked > 0) items.push({ level: 'warn', to: '/tasks', text: `작업 ${execution.blocked}건이 운영 승격을 기다리고 있습니다. 정의를 승격하면 자동으로 실행됩니다.` })
  if (execution.deadLetter > 0) items.push({ level: 'danger', to: '/admin/execution', text: `작업 ${execution.deadLetter}건이 재시도를 모두 소진했습니다. 원인을 고친 뒤 일괄 재실행할 수 있습니다.` })
  if (overview.workers.stale > 0) items.push({ level: 'danger', to: '/admin/execution', text: `워커 ${overview.workers.stale}개가 응답하지 않습니다. 들고 있던 작업은 회수 대상입니다.` })
  if (agents.unpromoted > 0) items.push({ level: 'warn', to: '/agents', text: `승격 게이트가 켜진 에이전트 ${agents.unpromoted}개의 현재 정의가 승격되지 않았습니다.` })
  if (events.deadLetter > 0) items.push({ level: 'danger', to: '/admin/execution', text: `이벤트 ${events.deadLetter}건을 배달하지 못했습니다. 재배달할 수 있습니다.` })
  if (events.pending + events.retrying > 20) items.push({ level: 'warn', to: '/admin/operations', text: `배달 대기 이벤트가 ${number(events.pending + events.retrying)}건 쌓였습니다.` })
  // A queue with no worker is the failure that looks like nothing happening.
  // The registry answers this, not the queue: a worker sitting idle is running,
  // and "nobody holds a task" cannot tell the difference.
  if (queue.ready > 0 && overview.workers.live === 0 && !overview.paused.paused) items.push({ level: 'danger', to: '/admin/execution', text: `실행 대기 작업 ${queue.ready}건이 있지만 동작 중인 워커가 없습니다.` })
  else if (overview.oldestQueuedSeconds > 900) items.push({ level: 'warn', to: '/tasks', text: `가장 오래된 대기 작업이 ${waited(overview.oldestQueuedSeconds)}째 기다리고 있습니다.` })
  if (agents.failed > 0) items.push({ level: 'danger', to: '/runtime', text: `런타임 ${agents.failed}개가 실패 상태입니다.` })
  if (overview.spend.unpricedTokens > 0) items.push({ level: 'warn', to: '/admin/models', text: `단가가 없는 모델에서 ${number(overview.spend.unpricedTokens)} 토큰을 사용해 금액에 반영되지 않았습니다.` })
  // A bill is not evidence unless it says what it could not see.
  if (overview.spend.unmeteredRuns > 0) items.push({ level: 'warn', to: '/tasks', text: `실행 ${number(overview.spend.runs)}건 중 ${number(overview.spend.unmeteredRuns)}건은 에이전트가 사용량을 알려주지 않아 이 금액에 들어 있지 않습니다.` })
  // Said before somebody is refused rather than after. The department screen has
  // the same numbers, but only for whoever went looking at it.
  for (const pressure of overview.quotaPressure ?? []) {
    items.push({
      level: pressure.percent >= 100 ? 'danger' : 'warn', to: '/admin/quotas',
      text: `${pressure.name} 부서가 ${pressure.limit} 총량의 ${pressure.percent}%를 쓰고 있습니다 (${number(pressure.used)} / ${number(pressure.allowed)}).`
    })
  }
  return items
}

function SpendTable({ title, rows, currency }: { title: string; rows: SpendRow[]; currency: string }) {
  if (rows.length === 0) return <div className="insight-table"><h4>{title}</h4><div className="empty-compact">기록된 실행이 없습니다.</div></div>
  const top = Math.max(...rows.map((row) => row.inputTokens + row.outputTokens), 1)
  return <div className="insight-table">
    <h4>{title}</h4>
    <table>
      <thead><tr><th>이름</th><th>실행</th><th>토큰</th><th>금액</th></tr></thead>
      <tbody>{rows.map((row) => {
        const total = row.inputTokens + row.outputTokens
        return <tr key={`${title}-${row.id}`}>
          <td title={row.id}>{row.name}</td>
          <td>{number(row.runs)}</td>
          <td><div className="token-bar"><i style={{ width: `${Math.round((total / top) * 100)}%` }} /><span>{number(total)}</span></div></td>
          <td>{row.priced ? money(row.cost, currency) : <span className="muted-cell">미산정</span>}</td>
        </tr>
      })}</tbody>
    </table>
  </div>
}

export function AdminInsights() {
  const [days, setDays] = useState(7)
  const [overview, setOverview] = useState<Overview | null>(null)
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(false)
  const load = useCallback(async () => {
    setLoading(true)
    try { setOverview(await api.get<Overview>(`/api/v1/admin/overview?days=${days}`)); setError('') }
    catch (e) { setError(e instanceof Error ? e.message : '운영 현황을 불러오지 못했습니다.') }
    finally { setLoading(false) }
  }, [days])
  useEffect(() => { void load() }, [load])

  if (!overview) return <div className="page">{error ? <ErrorBanner message={error} /> : <Loading />}</div>
  const { users, agents, execution, queue, events, spend, quota } = overview
  const alerts = attention(overview)
  const peak = Math.max(...spend.daily.map((point) => point.inputTokens + point.outputTokens), 1)

  return <div className="page">
    <PageHeader eyebrow="관리자 · 운영" title="운영 현황" description="실행 성공률, 대기열, 이벤트 배달, 토큰 사용량을 하나의 창에서 확인합니다. 모든 수치는 실행 기록에서 그대로 계산됩니다."
      actions={<>
        <div className="filter-chips">{WINDOWS.map((window) => (
          <button key={window.days} className={days === window.days ? 'selected' : ''} onClick={() => setDays(window.days)}>{window.label}</button>
        ))}</div>
        <button className="button ghost" disabled={loading} onClick={() => void load()}><RefreshCw size={16} />새로고침</button>
      </>} />
    {error && <ErrorBanner message={error} onClose={() => setError('')} />}
    {overview.paused.paused && <Link to="/admin/execution" className="attention-item danger" style={{ marginBottom: 14 }}>
      <ShieldAlert size={16} /><span>실행이 중지되어 있습니다{overview.paused.reason ? ` — ${overview.paused.reason}` : ''}{overview.paused.by ? ` (${overview.paused.by})` : ''}. 대기 중인 작업은 재개할 때까지 실행되지 않습니다.</span>
    </Link>}

    <Readiness />

    <section className="kpi-row">
      <article><span>실행 성공률</span><strong>{execution.completed + execution.failed + execution.deadLetter > 0 ? `${execution.successRate.toFixed(1)}%` : '—'}</strong><small>완료 {number(execution.completed)} · 실패 {number(execution.failed + execution.deadLetter)}</small></article>
      <article><span>실행 소요</span><strong>{duration(execution.medianDurationMs)}</strong><small>중앙값 · p95 {duration(execution.p95DurationMs)}</small></article>
      <article><span>대기열</span><strong>{number(queue.ready)}</strong><small>실행 중 {number(queue.running)} · 워커 {number(overview.workers.live)}{overview.workers.stale > 0 ? ` (응답 없음 ${number(overview.workers.stale)})` : ''}</small></article>
      <article><span>대기 시간</span><strong>{waited(overview.oldestQueuedSeconds)}</strong><small>가장 오래 기다린 작업</small></article>
      <article><span>토큰</span><strong>{number(spend.inputTokens + spend.outputTokens)}</strong><small>입력 {number(spend.inputTokens)} · 출력 {number(spend.outputTokens)}</small></article>
      <article><span>비용</span><strong>{money(spend.cost, spend.currency)}</strong><small>{spend.unpricedTokens > 0 ? `미산정 ${number(spend.unpricedTokens)} 토큰` : spend.unmeteredRuns > 0 ? `집계 안 된 실행 ${number(spend.unmeteredRuns)}건` : '전 구간 단가 적용'}</small></article>
    </section>

    {alerts.length > 0 && <section className="attention-list">
      {alerts.map((item) => <Link key={item.text} to={item.to} className={`attention-item ${item.level}`}>
        {item.level === 'danger' ? <ShieldAlert size={16} /> : <AlertTriangle size={16} />}<span>{item.text}</span>
      </Link>)}
    </section>}

    <section className="insight-grid">
      <article>
        <header><Gauge size={16} /><h3>실행</h3></header>
        <dl className="detail-list">
          <div><dt>실행 수</dt><dd>{number(execution.runs)}</dd></div>
          <div><dt>재시도한 작업</dt><dd>{number(execution.retried)}</dd></div>
          <div><dt>승격 대기</dt><dd>{number(execution.blocked)}</dd></div>
          <div><dt>처리 불가</dt><dd>{number(execution.deadLetter)}</dd></div>
        </dl>
        <div className="chip-row">{Object.entries(execution.tasks).sort((a, b) => b[1] - a[1]).map(([status, count]) => (
          <span key={status} className="count-chip">{statusLabel(status)} <b>{number(count)}</b></span>
        ))}</div>
      </article>
      <article>
        <header><Users size={16} /><h3>사용자</h3></header>
        <dl className="detail-list">
          <div><dt>전체</dt><dd>{number(users.total)}</dd></div>
          <div><dt>기간 내 로그인</dt><dd>{number(users.active)}</dd></div>
          <div><dt>관리자 · 팀장</dt><dd>{number(users.admins)} · {number(users.managers)}</dd></div>
          <div><dt>비활성 · 미사용</dt><dd>{number(users.disabled)} · {number(users.neverUsed)}</dd></div>
        </dl>
        <Link className="insight-link" to="/admin/users">사용자 관리 열기</Link>
      </article>
      <article>
        <header><Gauge size={16} /><h3>에이전트 · 런타임</h3></header>
        <dl className="detail-list">
          <div><dt>정의</dt><dd>{number(agents.total)}</dd></div>
          <div><dt>실행 중 · 예열</dt><dd>{number(agents.running)} · {number(agents.warm)}</dd></div>
          <div><dt>자율 실행</dt><dd>{number(agents.autonomy)}</dd></div>
          <div><dt>승격 게이트 · 미승격</dt><dd>{number(agents.gated)} · {number(agents.unpromoted)}</dd></div>
        </dl>
        <Link className="insight-link" to="/runtime">런타임 열기</Link>
      </article>
      <article>
        <header><Gauge size={16} /><h3>이벤트 배달</h3></header>
        <dl className="detail-list">
          <div><dt>기간 내 배달</dt><dd>{number(events.delivered)}</dd></div>
          <div><dt>대기 · 재시도</dt><dd>{number(events.pending)} · {number(events.retrying)}</dd></div>
          <div><dt>배달 실패</dt><dd>{number(events.deadLetter)}</dd></div>
          <div><dt>가장 오래 대기</dt><dd>{waited(events.oldestPendingSeconds)}</dd></div>
        </dl>
        <Link className="insight-link" to="/admin/execution">재배달 · 실행 제어 열기</Link>
      </article>
    </section>

    <section className="panel insight-spend">
      <header className="insight-header">
        <div><Coins size={17} /><h3>토큰 사용량</h3><small>{new Date(overview.from).toLocaleDateString('ko-KR')} ~ {new Date(overview.to).toLocaleDateString('ko-KR')}</small></div>
        <a className="button ghost" href={`/api/v1/admin/usage/export?days=${days}`}><Download size={15} />CSV 내려받기</a>
      </header>
      {(quota.tokenBudget > 0 || quota.costBudget > 0 || quota.maxRunning > 0) && <div className="usage-budget">
        {quota.tokenBudget > 0 && <span>사용자당 토큰 예산 <b>{number(quota.tokenBudget)}</b> (최근 {quota.windowDays}일)</span>}
        {quota.costBudget > 0 && <span>사용자당 비용 예산 <b>{money(quota.costBudget, spend.currency)}</b></span>}
        {quota.maxRunning > 0 && <span>사용자당 동시 실행 <b>{number(quota.maxRunning)}</b></span>}
        <small>예산과 동시 실행 한도는 시스템 설정 · 거버넌스에서 조정합니다.</small>
      </div>}
      {spend.daily.length === 0 ? <div className="empty-compact">이 기간에 기록된 실행이 없습니다.</div> : <div className="day-bars">
        {spend.daily.map((point) => {
          const total = point.inputTokens + point.outputTokens
          return <div key={point.day} title={`${new Date(point.day).toLocaleDateString('ko-KR')} · ${number(total)} 토큰 · ${money(point.cost, spend.currency)}`}>
            <i style={{ height: `${Math.max(4, Math.round((total / peak) * 100))}%` }} />
            <span>{new Date(point.day).getDate()}</span>
          </div>
        })}
      </div>}
      <div className="insight-tables">
        <SpendTable title="사용자별" rows={spend.users} currency={spend.currency} />
        <SpendTable title="에이전트별" rows={spend.agents} currency={spend.currency} />
        <SpendTable title="모델별" rows={spend.models} currency={spend.currency} />
      </div>
    </section>
  </div>
}

type ReadinessItem = { area: string; name: string; verdict: string; detail: string; fix: string }

/**
 * Everything this deployment depends on, asked at once.
 *
 * Each of these can be asked on the screen where it is configured, which is the
 * right place to fix one and the wrong place to discover that three are broken.
 * It runs on a button rather than on arrival: every row is a network call to
 * somebody else's service, and a screen that quietly probes five external
 * systems on every load is a screen that gets blamed for their outages.
 */
function Readiness() {
  const [items, setItems] = useState<ReadinessItem[]>()
  const [problems, setProblems] = useState(0)
  const [busy, setBusy] = useState(false)
  const [failed, setFailed] = useState('')
  const ask = async () => {
    setBusy(true); setFailed('')
    try {
      const result = await api.post<{ items: ReadinessItem[]; problems: number }>('/api/v1/admin/readiness')
      setItems(result.items); setProblems(result.problems)
    } catch (e) { setFailed(e instanceof Error ? e.message : '점검하지 못했습니다.') }
    finally { setBusy(false) }
  }
  return <section className="panel readiness">
    <div className="panel-header">
      <div><h2>배포 점검</h2><p>클러스터, SSO, 모델 엔드포인트, MCP 서버에게 지금 동작하는지 직접 물어봅니다.</p></div>
      <button className="button ghost" disabled={busy} onClick={() => void ask()}>
        <Stethoscope size={16} />{busy ? '점검 중…' : '지금 점검'}
      </button>
    </div>
    {failed && <p className="readiness-empty">{failed}</p>}
    {items && (items.length === 0
      ? <p className="readiness-empty">점검할 의존성이 아직 없습니다. Kubernetes 연결과 모델 엔드포인트를 먼저 등록하세요.</p>
      : <>
        <p className="readiness-summary">{problems === 0 ? `${items.length}가지 모두 정상입니다.` : `${items.length}가지 중 ${problems}가지에 문제가 있습니다.`}</p>
        <ul className="readiness-list">{items.map((item) => (
          <li key={`${item.area}-${item.name}`} className={item.verdict === 'ok' ? 'ok' : 'bad'}>
            <Link to={item.fix}><b>{item.area}</b> {item.name}</Link>
            <span>{item.detail}</span>
          </li>
        ))}</ul>
      </>)}
  </section>
}
