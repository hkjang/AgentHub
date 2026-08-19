import { FormEvent, useCallback, useEffect, useState } from 'react'
import { AlertTriangle, CircleStop, Cpu, Play, RefreshCw, RotateCcw, Send, Trash2 } from 'lucide-react'
import { api } from '../api'
import { ConfirmDialog, ErrorBanner, GuidePanel, Loading, PageHeader, StatusBadge } from '../components/UI'
import { relativeTime } from '../runtime'

/**
 * Operating the execution plane.
 *
 * The overview screen could say a queue had no worker behind it, that tasks had
 * exhausted their retries, that events had not been delivered — and then leave
 * the operator to fix each one somewhere else, one row at a time. These are the
 * actions those findings ask for.
 */

type Worker = { id: string; hostname: string; version: string; concurrency: number; maxConcurrency: number; running: number; status: string; startedAt: string; lastSeenAt: string; stale: boolean }
type DeadEvent = { id: string; type: string; subjectType: string; subjectId: string; attempts: number; lastError: string; createdAt: string }
type Retention = { runDays: number; eventDays: number; taskDays: number; auditDays: number }
type ExecutionState = {
  paused: boolean; reason: string; pausedBy: string; pausedAt?: string
  retention: Retention
  workers: Worker[]; liveWorkers: number
  heartbeatSeconds: number; staleAfterSeconds: number
  deadLetteredEvents: DeadEvent[]
}

const RETENTION_FIELDS: { key: keyof Retention; label: string; hint: string }[] = [
  { key: 'taskDays', label: '작업', hint: '완료·실패·취소된 작업. 최소 7일' },
  { key: 'runDays', label: '실행 기록', hint: '끝난 실행과 단계. 가장 큰 표입니다. 최소 7일' },
  { key: 'eventDays', label: '이벤트', hint: '배달된 이벤트만. 최소 3일' },
  { key: 'auditDays', label: '감사 로그', hint: '되돌릴 수 없습니다. 최소 30일' },
]

export function AdminExecution() {
  const [state, setState] = useState<ExecutionState | null>(null)
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const [busy, setBusy] = useState(false)
  const [reason, setReason] = useState('')
  const [retention, setRetention] = useState<Retention>({ runDays: 0, eventDays: 0, taskDays: 0, auditDays: 0 })
  const [preview, setPreview] = useState<Record<string, number> | null>(null)
  const [confirmCleanup, setConfirmCleanup] = useState(false)
  const [requeue, setRequeue] = useState({ status: 'dead_letter', sinceHours: 24, limit: 200 })

  const load = useCallback(async () => {
    try {
      const result = await api.get<ExecutionState>('/api/v1/admin/execution')
      setState(result)
      setRetention(result.retention)
      setError('')
    } catch (e) { setError(e instanceof Error ? e.message : '실행 상태를 불러오지 못했습니다.') }
  }, [])
  useEffect(() => { void load() }, [load])
  // Workers report every ten seconds, so the list is only useful if it follows.
  useEffect(() => {
    const timer = setInterval(() => { void load() }, 10000)
    return () => clearInterval(timer)
  }, [load])

  const act = async (run: () => Promise<string>) => {
    setBusy(true); setError(''); setNotice('')
    try { setNotice(await run()); await load() }
    catch (e) { setError(e instanceof Error ? e.message : '요청을 처리하지 못했습니다.') }
    finally { setBusy(false) }
  }
  const pause = (paused: boolean) => act(async () => {
    const result = await api.post<{ appliesInSeconds: number }>('/api/v1/admin/execution/pause', { paused, reason })
    setReason('')
    return paused
      ? `실행을 중지했습니다. 워커는 최대 ${result.appliesInSeconds}초 안에 새 작업 가져오기를 멈춥니다. 실행 중인 작업은 끝까지 진행됩니다.`
      : '실행을 재개했습니다. 대기 중이던 작업이 곧 시작됩니다.'
  })
  const reclaim = () => act(async () => {
    const result = await api.post<{ reclaimed: number }>('/api/v1/admin/execution/reclaim', {})
    return result.reclaimed > 0 ? `작업 ${result.reclaimed}건을 회수해 다시 대기열에 넣었습니다.` : '회수할 작업이 없습니다.'
  })
  const runRequeue = (event: FormEvent) => {
    event.preventDefault()
    void act(async () => {
      const result = await api.post<{ requeued: number }>('/api/v1/admin/execution/requeue', requeue)
      return result.requeued > 0 ? `작업 ${result.requeued}건을 다시 실행 대기로 옮겼습니다.` : '조건에 맞는 작업이 없습니다.'
    })
  }
  const redeliver = (id?: string) => act(async () => {
    const path = id ? `/api/v1/admin/execution/events/${id}/redeliver` : '/api/v1/admin/execution/events/redeliver'
    const result = await api.post<{ redelivered: number }>(path, {})
    return result.redelivered > 0 ? `이벤트 ${result.redelivered}건을 다시 배달 대기열에 넣었습니다.` : '재배달할 이벤트가 없습니다.'
  })
  const saveRetention = () => act(async () => {
    await api.put('/api/v1/admin/execution/retention', retention)
    setPreview(null)
    return '보관 기간을 저장했습니다. 워커가 매시간 기준을 넘긴 기록을 정리합니다.'
  })
  const previewCleanup = () => act(async () => {
    const result = await api.post<{ counts: Record<string, number> }>('/api/v1/admin/execution/cleanup', retention)
    setPreview(result.counts)
    const total = Object.values(result.counts).reduce((sum, value) => sum + value, 0)
    return total > 0 ? `지금 정리하면 ${total.toLocaleString('ko-KR')}건이 삭제됩니다.` : '삭제 대상이 없습니다.'
  })
  const applyCleanup = () => act(async () => {
    const result = await api.post<{ counts: Record<string, number> }>('/api/v1/admin/execution/cleanup', { ...retention, apply: true })
    setConfirmCleanup(false); setPreview(null)
    const total = Object.values(result.counts).reduce((sum, value) => sum + value, 0)
    return `${total.toLocaleString('ko-KR')}건을 삭제했습니다.`
  })

  if (!state) return <div className="page">{error ? <ErrorBanner message={error} /> : <Loading />}</div>
  const stale = state.workers.filter((worker) => worker.stale)
  const capacity = state.workers.filter((w) => !w.stale && w.status !== 'stopped').reduce((sum, w) => sum + w.maxConcurrency, 0)

  return <div className="page">
    <PageHeader eyebrow="관리자 · 운영" title="실행 제어"
      description="실행 중지·재개, 워커 상태, 멈춘 작업 회수와 기록 보관을 관리합니다."
      actions={<button className="button ghost" disabled={busy} onClick={() => void load()}><RefreshCw size={16} />새로고침</button>} />
    <GuidePanel id="admin-execution" title="이 화면은 이럴 때 씁니다" steps={[
      { title: '업그레이드하거나 사고가 났을 때', body: '실행을 중지하면 워커는 새 작업을 가져가지 않고, 실행 중인 작업은 끝까지 진행합니다. 작업 등록은 계속 되므로 재개하면 밀린 일이 이어서 실행됩니다.' },
      { title: '워커가 죽었을 때', body: '워커가 들고 있던 작업은 리스가 만료되면 자동으로 회수되어 대기열로 돌아갑니다(30초 주기). 바로 처리하려면 "지금 회수"를 누르세요.' },
      { title: '일시적 장애로 작업이 무더기로 실패했을 때', body: '원인을 고친 뒤 재실행 조건을 정해 한 번에 되돌립니다. 재시도 횟수는 0으로 초기화됩니다.' },
      { title: '디스크가 차오를 때', body: '보관 기간을 정하면 워커가 매시간 오래된 기록을 정리합니다. 먼저 "미리보기"로 몇 건이 지워지는지 확인하세요.' },
    ]} />
    {error && <ErrorBanner message={error} onClose={() => setError('')} />}
    {notice && <div className="notice-banner">{notice}</div>}

    <section className={`panel switch-panel ${state.paused ? 'paused' : ''}`}>
      <div>
        <h3>{state.paused ? '실행 중지됨' : '실행 중'}</h3>
        {state.paused
          ? <p>{state.reason || '사유가 기록되지 않았습니다.'} · {state.pausedBy || '알 수 없음'}{state.pausedAt ? ` · ${new Date(state.pausedAt).toLocaleString('ko-KR')}` : ''}</p>
          : <p>워커 {state.liveWorkers}개가 대기열에서 작업을 가져가고 있습니다. 동시 실행 용량 {capacity}.</p>}
      </div>
      {state.paused
        ? <button className="button primary" disabled={busy} onClick={() => void pause(false)}><Play size={16} />실행 재개</button>
        : <div className="switch-form">
            <input value={reason} onChange={(e) => setReason(e.target.value)} placeholder="중지 사유 (예: 컨트롤 플레인 업그레이드)" />
            <button className="button danger" disabled={busy} onClick={() => void pause(true)}><CircleStop size={16} />실행 중지</button>
          </div>}
    </section>

    <section className="panel">
      <header className="insight-header">
        <div><Cpu size={17} /><h3>워커 {state.workers.length}개</h3><small>{state.heartbeatSeconds}초마다 보고 · {state.staleAfterSeconds}초 이상 조용하면 응답 없음</small></div>
        <button className="button ghost" disabled={busy} onClick={() => void reclaim()}><RotateCcw size={15} />지금 회수</button>
      </header>
      {stale.length > 0 && <div className="attention-item danger" style={{ marginBottom: 10 }}>
        <AlertTriangle size={16} /><span>워커 {stale.length}개가 응답하지 않습니다. 들고 있던 작업은 회수 대상입니다.</span>
      </div>}
      {state.workers.length === 0
        ? <div className="empty-compact">등록된 워커가 없습니다. 워커 프로세스가 실행 중인지 확인하세요.</div>
        : <div className="table-wrap custom-scroll"><table>
            <thead><tr><th>워커</th><th>상태</th><th>실행 중</th><th>동시 실행</th><th>버전</th><th>마지막 보고</th></tr></thead>
            <tbody>{state.workers.map((worker) => <tr key={worker.id}>
              <td><div className="mono-stack"><code>{worker.id}</code><small>{worker.hostname}</small></div></td>
              <td><StatusBadge status={worker.stale ? 'error' : worker.status} /></td>
              <td>{worker.running}</td>
              <td>{worker.concurrency}{worker.maxConcurrency > worker.concurrency ? ` ~ ${worker.maxConcurrency}` : ''}</td>
              <td>{worker.version || '—'}</td>
              <td><span title={new Date(worker.lastSeenAt).toLocaleString('ko-KR')}>{relativeTime(worker.lastSeenAt)}</span></td>
            </tr>)}</tbody>
          </table></div>}
    </section>

    <section className="panel ops-panel">
      <h3>작업 일괄 재실행</h3>
      <p className="field-hint">고쳐 놓은 뒤 한 번에 되돌립니다. 재시도 횟수는 0으로 초기화되고, 대상은 최신 순으로 제한 개수만큼 처리됩니다.</p>
      <form className="ops-form" onSubmit={runRequeue}>
        <label><span>상태</span>
          <select value={requeue.status} onChange={(e) => setRequeue({ ...requeue, status: e.target.value })}>
            <option value="dead_letter">처리 불가 (재시도 소진)</option>
            <option value="failed">실패</option>
          </select>
        </label>
        <label><span>최근 시간</span><input type="number" min={1} max={8760} value={requeue.sinceHours} onChange={(e) => setRequeue({ ...requeue, sinceHours: Number(e.target.value) })} /></label>
        <label><span>최대 건수</span><input type="number" min={1} max={1000} value={requeue.limit} onChange={(e) => setRequeue({ ...requeue, limit: Number(e.target.value) })} /></label>
        <button className="button primary" disabled={busy}><RotateCcw size={15} />재실행</button>
      </form>
    </section>

    <section className="panel">
      <header className="insight-header">
        <div><Send size={17} /><h3>배달 실패 이벤트 {state.deadLetteredEvents.length}건</h3><small>구독 중인 Trigger에 다시 배달합니다. 이미 성공한 배달은 반복되지 않습니다.</small></div>
        {state.deadLetteredEvents.length > 0 && <button className="button ghost" disabled={busy} onClick={() => void redeliver()}><Send size={15} />전체 재배달</button>}
      </header>
      {state.deadLetteredEvents.length === 0
        ? <div className="empty-compact">배달하지 못한 이벤트가 없습니다.</div>
        : <div className="table-wrap custom-scroll"><table>
            <thead><tr><th>이벤트</th><th>대상</th><th>시도</th><th>마지막 오류</th><th>발생</th><th /></tr></thead>
            <tbody>{state.deadLetteredEvents.map((event) => <tr key={event.id}>
              <td><code>{event.type}</code></td>
              <td>{event.subjectType} <code>{event.subjectId.slice(0, 8)}</code></td>
              <td>{event.attempts}</td>
              <td className="muted-cell">{event.lastError || '—'}</td>
              <td>{relativeTime(event.createdAt)}</td>
              <td><button className="icon-button" disabled={busy} title="재배달" onClick={() => void redeliver(event.id)}><Send size={15} /></button></td>
            </tr>)}</tbody>
          </table></div>}
    </section>

    <section className="panel ops-panel retention-panel">
      <h3>기록 보관 기간</h3>
      <p className="field-hint">0이면 보관합니다(삭제하지 않음). 저장하면 워커가 매시간 기준을 넘긴 기록을 정리합니다.</p>
      <div className="ops-form">
        {RETENTION_FIELDS.map((field) => <label key={field.key}>
          <span>{field.label}</span>
          <input type="number" min={0} max={3650} value={retention[field.key]}
            onChange={(e) => { setPreview(null); setRetention({ ...retention, [field.key]: Number(e.target.value) }) }} />
          <small>{field.hint}</small>
        </label>)}
      </div>
      {preview && <div className="usage-budget">
        {Object.entries(preview).map(([name, count]) => <span key={name}>{name} <b>{count.toLocaleString('ko-KR')}</b>건</span>)}
        <small>미리보기입니다. 아직 아무것도 삭제되지 않았습니다.</small>
      </div>}
      <div className="ops-actions">
        <button className="button ghost" disabled={busy} onClick={() => void previewCleanup()}>미리보기</button>
        <button className="button ghost danger-text" disabled={busy} onClick={() => setConfirmCleanup(true)}><Trash2 size={15} />지금 정리</button>
        <button className="button primary" disabled={busy} onClick={() => void saveRetention()}>보관 기간 저장</button>
      </div>
    </section>

    {confirmCleanup && <ConfirmDialog title="기록을 지금 정리할까요?"
      message={<>설정한 기간을 넘긴 작업·실행·이벤트·감사 기록이 <strong>삭제됩니다</strong>. 되돌릴 수 없습니다.{preview && <> 미리보기 기준 {Object.values(preview).reduce((sum, value) => sum + value, 0).toLocaleString('ko-KR')}건입니다.</>}</>}
      confirmLabel="삭제" busy={busy} onConfirm={() => void applyCleanup()} onCancel={() => setConfirmCleanup(false)} />}
  </div>
}
