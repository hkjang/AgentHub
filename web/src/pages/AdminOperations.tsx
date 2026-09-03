import { useCallback, useEffect, useState } from 'react'
import { Activity, Check, ChevronLeft, ChevronRight, ClipboardCheck, Download, FileClock, RefreshCw, Search, ServerCog, X } from 'lucide-react'
import { api } from '../api'
import { ErrorBanner, Loading, PageHeader, StatusBadge } from '../components/UI'

type LogEntry = { time: string; level: string; message: string; source: string; fields?: Record<string, unknown> }
type Audit = { id: number; occurredAt: string; actor: string; action: string; resourceType: string; resourceId: string; outcome: string; ipAddress: string; details: unknown }
type AuditPage = { items: Audit[]; total: number; limit: number; offset: number; actions: string[] }
type Approval = { id: string; requesterName: string; resourceType: string; resourceId: string; action: string; reason: string; status: string; createdAt: string }

const PAGE_SIZE = 50

/** The trail is searched by who, what and when — the three things an auditor asks. */
type AuditQuery = { actor: string; action: string; outcome: string; from: string; to: string; offset: number }
const EMPTY_QUERY: AuditQuery = { actor: '', action: '', outcome: '', from: '', to: '', offset: 0 }

/** Local datetime-local values are naive; the API works in RFC3339. */
function auditParams(query: AuditQuery, limit: number) {
  const params = new URLSearchParams()
  if (query.actor.trim()) params.set('actor', query.actor.trim())
  if (query.action) params.set('action', query.action)
  if (query.outcome) params.set('outcome', query.outcome)
  if (query.from) params.set('from', new Date(query.from).toISOString())
  if (query.to) params.set('to', new Date(query.to).toISOString())
  if (limit > 0) { params.set('limit', String(limit)); params.set('offset', String(query.offset)) }
  return params
}

export function AdminOperations() {
  const [tab, setTab] = useState<'logs' | 'audit' | 'approvals'>('logs')
  const [logs, setLogs] = useState<LogEntry[]>()
  const [audit, setAudit] = useState<AuditPage>()
  const [approvals, setApprovals] = useState<{ items: Approval[]; pending: number; hidden: number }>()
  const [query, setQuery] = useState('')
  const [level, setLevel] = useState('INFO')
  const [auditQuery, setAuditQuery] = useState<AuditQuery>(EMPTY_QUERY)
  const [error, setError] = useState('')

  const loadAudit = useCallback(async (next: AuditQuery) => {
    try { setAudit(await api.get<AuditPage>(`/api/v1/admin/audit?${auditParams(next, PAGE_SIZE)}`)) }
    catch (e) { setError(e instanceof Error ? e.message : '감사 로그를 불러오지 못했습니다.') }
  }, [])

  const load = useCallback(() => Promise.all([
    api.get<{ items: LogEntry[] }>(`/api/v1/admin/logs?level=${level}&q=${encodeURIComponent(query)}&limit=300`).then((v) => setLogs(v.items)),
    loadAudit(auditQuery),
    api.get<{ items: Approval[]; pending: number; hidden: number }>('/api/v1/admin/approvals').then(setApprovals),
  ]).catch((e) => setError(e instanceof Error ? e.message : '불러오지 못했습니다.')), [level, query, auditQuery, loadAudit])

  useEffect(() => { void load() }, [load])

  const decide = async (id: string, value: 'approve' | 'reject') => {
    try { await api.post(`/api/v1/admin/approvals/${id}/${value}`); void load() }
    catch (e) { setError(e instanceof Error ? e.message : '처리하지 못했습니다.') }
  }
  const search = (patch: Partial<AuditQuery>) => setAuditQuery((current) => ({ ...current, ...patch, offset: patch.offset ?? 0 }))
  const filtered = auditQuery.actor !== '' || auditQuery.action !== '' || auditQuery.outcome !== '' || auditQuery.from !== '' || auditQuery.to !== ''

  if (!logs || !audit || !approvals) return <Loading />
  return <div className="page operations-page">
    <PageHeader eyebrow="런타임 운영" title="로그 · 감사" description="서버 로그, 감사 이벤트와 승인 요청을 Kubernetes 도구 없이 확인합니다."
      actions={<button className="button ghost" onClick={() => void load()}><RefreshCw size={16} />새로고침</button>} />
    {error && <ErrorBanner message={error} onClose={() => setError('')} />}
    <div className="operations-summary">
      <article><ServerCog /><div><strong>컨트롤 플레인</strong><StatusBadge status="online" /></div></article>
      <article><Activity /><div><strong>수집된 로그</strong><span>{logs.length}</span></div></article>
      <article><FileClock /><div><strong>감사 이벤트</strong><span>{audit.total.toLocaleString('ko-KR')}</span></div></article>
      <article><ClipboardCheck /><div><strong>대기</strong><span>{approvals.pending}</span></div></article>
    </div>
    <div className="tabs">
      <button className={tab === 'logs' ? 'active' : ''} onClick={() => setTab('logs')}>서버 로그</button>
      <button className={tab === 'audit' ? 'active' : ''} onClick={() => setTab('audit')}>감사 로그</button>
      <button className={tab === 'approvals' ? 'active' : ''} onClick={() => setTab('approvals')}>승인 대기</button>
    </div>

    {tab === 'logs' && <section className="log-console">
      <header>
        <div className="search-box dark"><Search size={16} /><input value={query} onChange={(e) => setQuery(e.target.value)} onKeyDown={(e) => { if (e.key === 'Enter') void load() }} placeholder="메시지 또는 source 검색" /></div>
        <select value={level} onChange={(e) => setLevel(e.target.value)} aria-label="로그 수준"><option>DEBUG</option><option>INFO</option><option>WARN</option><option>ERROR</option></select>
      </header>
      <div className="log-scroll custom-scroll">
        {logs.map((log, index) => <div className="log-line" key={`${log.time}-${index}`}>
          <time>{new Date(log.time).toLocaleTimeString('ko-KR', { hour12: false })}</time>
          <span className={`log-level ${log.level.toLowerCase()}`}>{log.level}</span>
          <span className="log-source">{log.source}</span>
          <code>{log.message}</code>
          <small>{log.fields ? JSON.stringify(log.fields) : ''}</small>
        </div>)}
        {logs.length === 0 && <div className="empty-compact dark-text">조건에 맞는 로그가 없습니다.</div>}
      </div>
    </section>}

    {tab === 'audit' && <section className="table-panel">
      <div className="audit-filters">
        <label><span>수행자</span><input value={auditQuery.actor} onChange={(e) => search({ actor: e.target.value })} placeholder="사용자 이름 일부" /></label>
        <label><span>동작</span>
          <select value={auditQuery.action} onChange={(e) => search({ action: e.target.value })}>
            <option value="">전체</option>
            {audit.actions.map((action) => <option key={action} value={action}>{action}</option>)}
          </select>
        </label>
        <label><span>결과</span>
          <select value={auditQuery.outcome} onChange={(e) => search({ outcome: e.target.value })}>
            <option value="">전체</option><option value="success">성공</option><option value="failure">실패</option><option value="denied">거부</option>
            {/* What a content scan did: 차단됨 held the call back, 가림 처리됨 rewrote
                the text, 기록만 let it through untouched. */}
            <option value="blocked">차단됨</option><option value="redacted">가림 처리됨</option><option value="audited">기록만</option>
          </select>
        </label>
        <label><span>시작</span><input type="datetime-local" value={auditQuery.from} onChange={(e) => search({ from: e.target.value })} /></label>
        <label><span>종료</span><input type="datetime-local" value={auditQuery.to} onChange={(e) => search({ to: e.target.value })} /></label>
        <div className="audit-filter-actions">
          {filtered && <button className="button ghost" onClick={() => setAuditQuery(EMPTY_QUERY)}><X size={15} />조건 지우기</button>}
          {/* A download is a plain link so the browser saves it with the session
              it already has, rather than fetching it into memory first. */}
          <a className="button ghost" href={`/api/v1/admin/audit/export?${auditParams(auditQuery, 0)}`}><Download size={15} />CSV 내려받기</a>
        </div>
      </div>
      <div className="table-wrap custom-scroll"><table>
        <thead><tr><th>시각</th><th>수행자</th><th>동작</th><th>대상</th><th>결과</th><th>IP</th></tr></thead>
        <tbody>{audit.items.map((item) => <tr key={item.id}>
          <td>{new Date(item.occurredAt).toLocaleString('ko-KR')}</td>
          <td>{item.actor}</td>
          <td><code>{item.action}</code></td>
          <td>{item.resourceType} <code>{item.resourceId.slice(0, 8)}</code></td>
          <td><StatusBadge status={item.outcome} /></td>
          <td><code>{item.ipAddress}</code></td>
        </tr>)}</tbody>
      </table></div>
      {audit.items.length === 0 && <div className="empty-compact">조건에 맞는 감사 이벤트가 없습니다.</div>}
      <div className="audit-pager">
        <span>{audit.total.toLocaleString('ko-KR')}건 중 {audit.total === 0 ? 0 : audit.offset + 1}–{Math.min(audit.offset + audit.limit, audit.total)}</span>
        <button disabled={audit.offset === 0} onClick={() => search({ offset: Math.max(0, audit.offset - PAGE_SIZE) })}><ChevronLeft size={16} />이전</button>
        <button disabled={audit.offset + audit.limit >= audit.total} onClick={() => search({ offset: audit.offset + PAGE_SIZE })}>다음<ChevronRight size={16} /></button>
      </div>
    </section>}

    {tab === 'approvals' && <section className="approval-list">
      {/* A request nobody can see is not a slow decision — it is a task that never
          runs again. The list holds 200; when more are waiting it says how many. */}
      {approvals.hidden > 0 && <div className="notice warning">대기 {approvals.pending}건 중 {approvals.items.length}건만 표시됩니다 — {approvals.hidden}건은 목록에 들어가지 않았습니다. 오래 기다린 순서로 보여 주므로, 처리하면 나머지가 올라옵니다.</div>}
      {approvals.items.length === 0 ? <div className="empty-compact">승인 요청이 없습니다.</div> : approvals.items.map((item) => <article key={item.id}>
        <div>
          <StatusBadge status={item.status} />
          <h3>{item.action}</h3>
          <p>{item.reason}</p>
          <span>{item.requesterName} · {item.resourceType} · {new Date(item.createdAt).toLocaleString('ko-KR')}</span>
        </div>
        {item.status === 'pending' && <div>
          <button className="button danger subtle" onClick={() => void decide(item.id, 'reject')}><X size={16} />반려</button>
          <button className="button primary" onClick={() => void decide(item.id, 'approve')}><Check size={16} />승인</button>
        </div>}
      </article>)}
    </section>}
  </div>
}
