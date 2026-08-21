import { useCallback, useEffect, useState } from 'react'
import { ClipboardList, RefreshCw, Search } from 'lucide-react'
import { api } from '../api'
import { useAuth } from '../App'
import { Empty, ErrorBanner, Loading, PageHeader, StatusBadge } from '../components/UI'
import { RunDrawer } from './Tasks'
import { useTerms } from '../viewmode'
import { relativeTime } from '../runtime'
import { metering as meteringOf } from '../metering'
import type { Agent, AgentRun } from '../types'

// Every attempt the platform has made, in one place.
//
// Runs could only be reached one at a time, through the task that produced them,
// which answers "how did this task go" and nothing else. The questions an
// operator actually arrives with are about the set: what failed today, which
// agent is spending, which runs nobody counted. Those are filters, and they need
// a list to filter.

const STATUSES: [string, string][] = [['', '전체 상태'], ['completed', '완료'], ['failed', '실패'], ['running', '실행 중'], ['cancelled', '취소']]
const METERINGS: [string, string][] = [['', '전체 계량'], ['unmetered', '집계 안 됨'], ['agent', '에이전트 보고'], ['gateway', '플랫폼 집계']]
const WINDOWS: [string, string][] = [['1', '최근 1일'], ['7', '최근 7일'], ['30', '최근 30일'], ['', '전체 기간']]

export function Runs() {
  const t = useTerms()
  const { user } = useAuth()
  const [items, setItems] = useState<AgentRun[]>()
  const [agents, setAgents] = useState<Agent[]>([])
  const [status, setStatus] = useState('')
  const [meter, setMeter] = useState('')
  const [days, setDays] = useState('7')
  const [agentId, setAgentId] = useState('')
  const [text, setText] = useState('')
  // Typed text is debounced before it becomes a query: reloading on every
  // keystroke would fire a request per character at a table of two hundred rows.
  const [searching, setSearching] = useState('')
  const [everyone, setEveryone] = useState(false)
  const [openRun, setOpenRun] = useState<string | null>(null)
  const [error, setError] = useState('')

  const load = useCallback(async () => {
    setError('')
    const query = new URLSearchParams({ limit: '200' })
    if (status) query.set('status', status)
    if (meter) query.set('metering', meter)
    if (days) query.set('days', days)
    if (agentId) query.set('agentId', agentId)
    if (searching) query.set('q', searching)
    if (everyone) query.set('scope', 'all')
    try {
      const result = await api.get<{ items: AgentRun[] }>(`/api/v1/runs?${query}`)
      setItems(result.items ?? [])
    } catch (e) { setError(e instanceof Error ? e.message : '실행 기록을 불러오지 못했습니다.') }
  }, [status, meter, days, agentId, everyone, searching])
  useEffect(() => {
    const timer = window.setTimeout(() => setSearching(text.trim()), 300)
    return () => window.clearTimeout(timer)
  }, [text])
  useEffect(() => { void load() }, [load])
  useEffect(() => { api.get<{ items?: Agent[] }>('/api/v1/agents').then((v) => setAgents(v.items ?? [])).catch(() => setAgents([])) }, [])

  return <div className="page">
    <PageHeader eyebrow="실행" title={t('runs')} description="모든 시도를 한 화면에서 봅니다. 무엇이 실패했는지, 어느 에이전트가 쓰고 있는지, 어떤 실행이 계량되지 않았는지 걸러 볼 수 있습니다."
      actions={<button className="button ghost" onClick={() => void load()}><RefreshCw size={16} />새로고침</button>} />
    {error && <ErrorBanner message={error} />}
    <div className="filter-row">
      {/* What somebody has in hand when they arrive: an id copied out of a log,
          or a sentence they remember seeing. */}
      <div className="search-box"><Search size={16} /><input value={text} onChange={(e) => setText(e.target.value)} placeholder="Trace ID, 실패 메시지, 에이전트 이름" aria-label="검색" /></div>
      <select value={status} onChange={(e) => setStatus(e.target.value)} aria-label="상태">{STATUSES.map(([v, l]) => <option key={v} value={v}>{l}</option>)}</select>
      <select value={meter} onChange={(e) => setMeter(e.target.value)} aria-label="계량">{METERINGS.map(([v, l]) => <option key={v} value={v}>{l}</option>)}</select>
      <select value={days} onChange={(e) => setDays(e.target.value)} aria-label="기간">{WINDOWS.map(([v, l]) => <option key={v} value={v}>{l}</option>)}</select>
      <select value={agentId} onChange={(e) => setAgentId(e.target.value)} aria-label="에이전트">
        <option value="">전체 {t('agentSingular')}</option>
        {agents.map((agent) => <option key={agent.id} value={agent.id}>{agent.name}</option>)}
      </select>
      {user.role === 'admin' && <label className="filter-toggle"><input type="checkbox" checked={everyone} onChange={(e) => setEveryone(e.target.checked)} />모든 사용자</label>}
    </div>
    {items && items.length > 0 && <RunSummary items={items} />}
    {!items ? <Loading /> : items.length === 0
      ? <Empty icon={<ClipboardList />} title="해당하는 실행이 없습니다" description="조건을 넓히거나 기간을 늘려 보세요." />
      : <section className="table-panel"><div className="table-wrap custom-scroll"><table>
          <thead><tr><th>{t('agentSingular')}</th><th>상태</th><th>시작</th><th>소요</th><th>단계</th><th>도구</th><th>토큰</th><th>결과</th></tr></thead>
          <tbody>{items.map((run) => {
            const meterInfo = meteringOf(run.metering)
            return <tr key={run.id} className="clickable" onClick={() => setOpenRun(run.id)}>
              <td><strong>{run.agentName || run.agentId.slice(0, 8)}</strong><small className="muted-cell"> · 시도 {run.attempt}</small></td>
              <td><StatusBadge status={run.status} /></td>
              <td><span title={new Date(run.startedAt).toLocaleString('ko-KR')}>{relativeTime(run.startedAt)}</span></td>
              <td>{run.durationMs.toLocaleString('ko-KR')}ms</td>
              <td>{run.stepCount}</td>
              <td>{run.toolCalls}</td>
              <td>{run.totalTokens.toLocaleString('ko-KR')}
                {meterInfo && <span className={`metering-tag ${meterInfo.tone}`} title={meterInfo.hint}>{meterInfo.label}</span>}</td>
              <td className="muted-cell">{run.failureReason || run.result || '—'}</td>
            </tr>
          })}</tbody>
        </table></div></section>}
    {openRun && <RunDrawer runId={openRun} close={() => setOpenRun(null)} />}
  </div>
}

/**
 * What the listing adds up to. A page of two hundred rows says a lot happened
 * and nothing about what; the same rows counted say whether one fault is
 * repeating, which is the difference between reading them and fixing them.
 *
 * Counted from what is on screen rather than from a second query, so the summary
 * always describes exactly the rows underneath it.
 */
function RunSummary({ items }: { items: AgentRun[] }) {
  const failed = items.filter((run) => run.status === 'failed')
  const reasons = new Map<string, number>()
  for (const run of failed) {
    const reason = (run.failureReason || '이유 없음').split('\n')[0].slice(0, 80)
    reasons.set(reason, (reasons.get(reason) ?? 0) + 1)
  }
  const top = [...reasons.entries()].sort((a, b) => b[1] - a[1]).slice(0, 3)
  const tokens = items.reduce((sum, run) => sum + run.totalTokens, 0)
  const unmetered = items.filter((run) => run.metering === 'unmetered' || run.metering === 'context_only').length
  return <section className="run-summary">
    <div className="run-summary-counts">
      <span>실행 <b>{items.length.toLocaleString('ko-KR')}</b></span>
      <span>실패 <b>{failed.length.toLocaleString('ko-KR')}</b></span>
      <span>토큰 <b>{tokens.toLocaleString('ko-KR')}</b></span>
      {unmetered > 0 && <span className="warn">집계 안 됨 <b>{unmetered.toLocaleString('ko-KR')}</b></span>}
    </div>
    {top.length > 0 && <ol className="run-summary-reasons">
      {top.map(([reason, count]) => <li key={reason}><b>{count}회</b><span title={reason}>{reason}</span></li>)}
    </ol>}
  </section>
}
