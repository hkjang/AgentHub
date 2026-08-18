import { FormEvent, useCallback, useEffect, useMemo, useState } from 'react'
import { Bot, ClipboardList, Clock3, Coins, ExternalLink, ListChecks, Play, Plus, Radio, RefreshCw, RotateCcw, Square } from 'lucide-react'
import { api } from '../api'
import { ConfirmDialog, Drawer, Empty, ErrorBanner, Loading, PageHeader, StatusBadge, statusLabel } from '../components/UI'
import { relativeTime, runtimeCode, runtimeLabel, runtimeLogoClass } from '../runtime'
import type { Agent, AgentArtifact, AgentPlan, AgentRun, AgentRunEvent, AgentRunStep, AgentTask, PlatformEvent, QueueSnapshot, UsageReport } from '../types'

/** Statuses that are still moving, and therefore worth polling for. */
const ACTIVE = ['queued', 'planning', 'ready', 'running', 'waiting_tool', 'waiting_approval', 'retrying']

const PRIORITY_LABELS: Record<string, string> = {
  critical: '긴급', high: '높음', normal: '보통', low: '낮음', background: '배경',
}

const SOURCE_LABELS: Record<string, string> = {
  manual: '직접 실행', cron: '예약', webhook: 'Webhook', agent: '다른 에이전트', event: '이벤트',
}

const EVENT_LABELS: Record<string, string> = {
  'task.completed': '작업 완료', 'task.failed': '작업 실패', 'task.dead_lettered': '재시도 소진',
  'approval.decided': '승인 처리', 'runtime.failed': '런타임 장애', 'artifact.created': '산출물 생성',
}

export function Tasks() {
  const [tasks, setTasks] = useState<AgentTask[]>()
  const [agents, setAgents] = useState<Agent[]>([])
  const [queue, setQueue] = useState<QueueSnapshot>()
  const [error, setError] = useState('')
  const [filter, setFilter] = useState('')
  const [creating, setCreating] = useState(false)
  const [openRun, setOpenRun] = useState<string | null>(null)
  const [cancelling, setCancelling] = useState<AgentTask | null>(null)
  const [busy, setBusy] = useState(false)

  const load = useCallback(async () => {
    try {
      const [taskResult, agentResult, queueResult] = await Promise.all([
        api.get<{ items?: AgentTask[] }>('/api/v1/tasks'),
        api.get<{ items?: Agent[] }>('/api/v1/agents'),
        api.get<QueueSnapshot>('/api/v1/queue'),
      ])
      setTasks(taskResult.items ?? [])
      setAgents(agentResult.items ?? [])
      setQueue(queueResult)
    } catch (e) {
      setTasks([])
      setError(e instanceof Error ? e.message : 'Task 목록을 불러오지 못했습니다.')
    }
  }, [])

  useEffect(() => {
    void load()
    // Tasks change on their own, so poll — but only while something is actually
    // in flight and the tab is visible.
    const timer = window.setInterval(() => {
      if (document.visibilityState === 'visible') void load()
    }, 5000)
    return () => window.clearInterval(timer)
  }, [load])

  const runtimeByAgent = useMemo(() => new Map(agents.map((agent) => [agent.id, agent.runtimeType])), [agents])
  if (!tasks) return <Loading />

  const visible = filter ? tasks.filter((task) => task.status === filter) : tasks
  const counts = tasks.reduce<Record<string, number>>((all, task) => ({ ...all, [task.status]: (all[task.status] ?? 0) + 1 }), {})
  const active = tasks.filter((task) => ACTIVE.includes(task.status)).length

  const cancel = async () => {
    if (!cancelling) return
    setBusy(true)
    try {
      await api.post(`/api/v1/tasks/${cancelling.id}/cancel`)
      setCancelling(null)
      await load()
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Task를 취소하지 못했습니다.')
    } finally { setBusy(false) }
  }

  const retry = async (task: AgentTask) => {
    setError('')
    try {
      await api.post(`/api/v1/tasks/${task.id}/retry`)
      await load()
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Task를 다시 실행하지 못했습니다.')
    }
  }

  return <div className="page">
    <PageHeader eyebrow="실행 플레인" title="에이전트 작업"
      description="에이전트가 스스로 수행하는 작업의 대기열과 실행 결과입니다."
      actions={<>
        <button className="button ghost" onClick={() => void load()}><RefreshCw size={15} />새로고침</button>
        <button className="button primary" disabled={agents.length === 0} onClick={() => setCreating(true)}><Plus size={16} />새 작업</button>
      </>} />
    {error && <ErrorBanner message={error} onClose={() => setError('')} />}

    {tasks.length > 0 && <div className="toolbar">
      <div className="filter-chips">
        <button className={filter === '' ? 'selected' : ''} onClick={() => setFilter('')}>전체 {tasks.length}</button>
        {['running', 'queued', 'waiting_approval', 'retrying', 'completed', 'failed', 'dead_letter'].filter((status) => counts[status])
          .map((status) => <button key={status} className={filter === status ? 'selected' : ''} onClick={() => setFilter(status)}>
            {statusLabel(status)} {counts[status]}
          </button>)}
      </div>
      <span className="row-time">
        <Clock3 size={14} />
        {queue ? `대기 ${queue.ready} · 실행 ${queue.running} · 워커 ${queue.workers}` : `진행 중 ${active}건`} · 5초마다 갱신
      </span>
    </div>}

    {tasks.length === 0
      ? <Empty icon={<ListChecks />} title="아직 작업이 없습니다"
          description="에이전트에 목표를 설정하고 작업을 맡기면 여기에 실행 기록이 쌓입니다."
          action={agents.length ? <button className="button primary" onClick={() => setCreating(true)}>첫 작업 만들기</button> : undefined} />
      : visible.length === 0
        ? <div className="empty-compact">해당 상태의 작업이 없습니다.</div>
        : <section className="table-panel"><div className="table-wrap custom-scroll"><table>
            <thead><tr><th>작업</th><th>에이전트</th><th>상태</th><th>우선순위</th><th>출처</th><th>시도</th><th>마지막 변경</th><th aria-label="작업" /></tr></thead>
            <tbody>{visible.map((task) => <tr key={task.id}>
              <td>
                <div className="agent-main">
                  <strong>{task.title}</strong>
                  {task.lastError && <span className="task-error" title={task.lastError}>{task.lastError}</span>}
                </div>
              </td>
              <td><div className="task-agent">
                <div className={runtimeLogoClass(runtimeByAgent.get(task.agentId) ?? 'custom')}>{runtimeCode(runtimeByAgent.get(task.agentId) ?? 'custom')}</div>
                <span>{task.agentName ?? runtimeLabel(runtimeByAgent.get(task.agentId) ?? '')}</span>
              </div></td>
              <td><StatusBadge status={task.status} /></td>
              <td>{PRIORITY_LABELS[task.priority] ?? task.priority}</td>
              <td>{SOURCE_LABELS[task.source] ?? task.source}</td>
              <td>{task.attempts}</td>
              <td><span title={new Date(task.updatedAt).toLocaleString('ko-KR')}>{relativeTime(task.updatedAt)}</span></td>
              <td><div className="row-actions">
                {task.status === 'waiting_approval' && <a className="task-approval" title="승인 화면으로" href="/reviews">승인 대기</a>}
                {task.currentRunId && <button title="실행 기록" onClick={() => setOpenRun(task.currentRunId!)}><ExternalLink size={15} /></button>}
                {['failed', 'dead_letter', 'cancelled'].includes(task.status) && <button title="다시 실행" onClick={() => void retry(task)}><RotateCcw size={15} /></button>}
                {ACTIVE.includes(task.status) && <button className="danger" title="취소" onClick={() => setCancelling(task)}><Square size={14} /></button>}
              </div></td>
            </tr>)}</tbody>
          </table></div></section>}

    <UsagePanel />

    <EventFeed />

    {creating && <CreateTaskDrawer agents={agents} close={() => setCreating(false)} done={() => { setCreating(false); void load() }} />}
    {openRun && <RunDrawer runId={openRun} close={() => setOpenRun(null)} />}
    {cancelling && <ConfirmDialog title="작업을 취소할까요?"
      message={<><strong>{cancelling.title}</strong> 작업이 취소됩니다. 이미 실행 중인 단계는 마무리된 뒤 중단됩니다.</>}
      confirmLabel="취소하기" busy={busy}
      onConfirm={() => void cancel()} onCancel={() => setCancelling(null)} />}
  </div>
}

/** Token spend over the last 30 days.
 *  Autonomous agents run unattended, which is exactly when a runaway loop costs
 *  money quietly, so the bill belongs next to the queue rather than buried in an
 *  admin screen. */
function UsagePanel() {
  const [report, setReport] = useState<UsageReport>()
  const [open, setOpen] = useState(false)
  const [error, setError] = useState('')
  useEffect(() => {
    if (!open || report) return
    void api.get<UsageReport>('/api/v1/usage')
      .then(setReport)
      .catch((e) => setError(e instanceof Error ? e.message : '사용량을 불러오지 못했습니다.'))
  }, [open, report])

  const money = (value: number, currency: string) =>
    `${value.toLocaleString('ko-KR', { maximumFractionDigits: 2 })} ${currency}`
  const tokens = (value: number) => value.toLocaleString('ko-KR')

  return <section className="event-feed">
    <button className="event-feed-toggle" onClick={() => setOpen(!open)}>
      <Coins size={15} />최근 30일 토큰 사용량 {open ? '숨기기' : '보기'}
    </button>
    {open && (error
      ? <div className="empty-compact">{error}</div>
      : !report
        ? <div className="empty-compact">불러오는 중…</div>
        : report.agents.length === 0
          ? <div className="empty-compact">기록된 실행이 없습니다.</div>
          : <>
              <div className="usage-summary">
                <div><span>입력</span><strong>{tokens(report.inputTokens)}</strong></div>
                <div><span>출력</span><strong>{tokens(report.outputTokens)}</strong></div>
                <div><span>금액</span><strong>{money(report.cost, report.currency)}</strong></div>
                {report.unpricedTokens > 0 && <div className="unpriced">
                  <span>미산정</span><strong>{tokens(report.unpricedTokens)} 토큰</strong>
                </div>}
              </div>
              <div className="table-wrap custom-scroll"><table>
                <thead><tr><th>에이전트</th><th>모델</th><th>실행</th><th>입력</th><th>출력</th><th>금액</th></tr></thead>
                <tbody>{report.agents.map((row) => <tr key={`${row.agentId}-${row.modelName}`}>
                  <td>{row.agentName}</td>
                  <td>{row.modelName || '—'}</td>
                  <td>{row.runs}</td>
                  <td>{tokens(row.inputTokens)}</td>
                  <td>{tokens(row.outputTokens)}</td>
                  <td>{row.priced ? money(row.cost, row.currency) : <span className="row-time">미산정</span>}</td>
                </tr>)}</tbody>
              </table></div>
              {report.unpricedTokens > 0 && <small>단가가 등록되지 않은 모델의 토큰은 금액에 포함되지 않습니다. 관리자 · 리소스 · 모델 엔드포인트에서 단가를 입력하세요.</small>}
            </>)}
  </section>
}

/** What the platform has published lately. An event trigger can only react to
 *  something that actually happens, so this is where an operator checks that it
 *  does before wiring one up. */
function EventFeed() {
  const [events, setEvents] = useState<PlatformEvent[]>([])
  const [open, setOpen] = useState(false)
  useEffect(() => {
    if (!open) return
    void api.get<{ items?: PlatformEvent[] }>('/api/v1/events?limit=20')
      .then((result) => setEvents(result.items ?? []))
      .catch(() => setEvents([]))
  }, [open])
  return <section className="event-feed">
    <button className="event-feed-toggle" onClick={() => setOpen(!open)}>
      <Radio size={15} />최근 플랫폼 이벤트 {open ? '숨기기' : '보기'}
    </button>
    {open && (events.length === 0
      ? <div className="empty-compact">아직 발행된 이벤트가 없습니다.</div>
      : <ul className="event-list">{events.map((event) => <li key={event.id}>
          <span className="event-kind">{EVENT_LABELS[event.type] ?? event.type}</span>
          <span className="event-subject">{event.subjectType} {event.subjectId.slice(0, 8)}</span>
          <span className="row-time" title={new Date(event.createdAt).toLocaleString('ko-KR')}>{relativeTime(event.createdAt)}</span>
          {!event.dispatchedAt && <span className="event-pending">전달 대기</span>}
        </li>)}</ul>)}
  </section>
}

function CreateTaskDrawer({ agents, close, done }: { agents: Agent[]; close: () => void; done: () => void }) {
  const [agentId, setAgentId] = useState(agents[0]?.id ?? '')
  const [title, setTitle] = useState('')
  const [input, setInput] = useState('')
  const [priority, setPriority] = useState('normal')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const submit = async (event: FormEvent) => {
    event.preventDefault(); setBusy(true); setError('')
    try {
      await api.post('/api/v1/tasks', { agentId, title, input, priority })
      done()
    } catch (e) {
      setError(e instanceof Error ? e.message : '작업을 만들지 못했습니다.')
      setBusy(false)
    }
  }
  return <Drawer title="새 작업" subtitle="에이전트가 스스로 수행할 작업을 대기열에 넣습니다." close={close}
    footer={<><button className="button ghost" onClick={close}>취소</button>
      <button className="button primary" form="task-form" disabled={busy}><Play size={15} />{busy ? '등록 중…' : '작업 등록'}</button></>}>
    <form id="task-form" className="drawer-form" onSubmit={submit}>
      {error && <ErrorBanner message={error} onClose={() => setError('')} />}
      <label><span>에이전트 <b>*</b></span>
        <select required value={agentId} onChange={(e) => setAgentId(e.target.value)}>
          {agents.map((agent) => <option key={agent.id} value={agent.id}>{agent.name} · {runtimeLabel(agent.runtimeType)}</option>)}
        </select>
      </label>
      <label><span>작업 제목</span><input maxLength={200} value={title} onChange={(e) => setTitle(e.target.value)} placeholder="비워 두면 에이전트 이름으로 만들어집니다" /></label>
      <label><span>작업 내용</span><textarea rows={6} value={input} onChange={(e) => setInput(e.target.value)} placeholder="에이전트가 수행할 작업을 구체적으로 적으세요." /></label>
      <label><span>우선순위</span>
        <select value={priority} onChange={(e) => setPriority(e.target.value)}>
          {Object.entries(PRIORITY_LABELS).map(([value, label]) => <option key={value} value={value}>{label}</option>)}
        </select>
      </label>
      <div className="info-box"><Bot size={17} /><div><strong>자동 실행</strong>
        <p>Worker가 대기열에서 작업을 가져가 목표와 완료 조건에 따라 수행하고, 필요하면 Runtime을 자동으로 확보합니다.</p></div></div>
    </form>
  </Drawer>
}

/** The full picture of one attempt: timeline, per-step reasoning and artifacts. */
export function RunDrawer({ runId, close }: { runId: string; close: () => void }) {
  const [data, setData] = useState<{ run: AgentRun; steps: AgentRunStep[]; events: AgentRunEvent[]; artifacts: AgentArtifact[]; plan?: AgentPlan }>()
  const [error, setError] = useState('')
  const load = useCallback(async () => {
    try {
      setData(await api.get(`/api/v1/runs/${runId}`))
    } catch (e) {
      setError(e instanceof Error ? e.message : '실행 기록을 불러오지 못했습니다.')
    }
  }, [runId])
  useEffect(() => { void load() }, [load])
  // A finished run cannot change, so polling it forever was a request every four
  // seconds for nothing — in a background tab too.
  const finished = Boolean(data?.run.finishedAt)
  useEffect(() => {
    if (finished) return
    const timer = window.setInterval(() => {
      if (document.visibilityState === 'visible') void load()
    }, 4000)
    return () => window.clearInterval(timer)
  }, [load, finished])

  if (error) return <Drawer title="실행 기록" close={close}><ErrorBanner message={error} /></Drawer>
  if (!data) return <Drawer title="실행 기록" close={close}><Loading /></Drawer>

  const { run, steps, events, artifacts, plan } = data
  const verdict = run.completion ?? {}
  return <Drawer title={`실행 기록 #${run.id.slice(0, 8)}`} subtitle={`시도 ${run.attempt} · ${run.modelName || '모델 미지정'}`} close={close}>
    <div className="detail-hero">
      <div className="runtime-logo xlarge custom"><ClipboardList size={22} /></div>
      <div><StatusBadge status={run.status} /><h3>{run.durationMs}ms · {run.stepCount}단계</h3>
        <p>토큰 {run.totalTokens.toLocaleString('ko-KR')} · trace <code>{run.traceId || '—'}</code></p></div>
    </div>

    {run.failureReason && <ErrorBanner message={run.failureReason} />}

    {verdict.strategy && <section className="detail-section"><h4>완료 판정</h4>
      <div className={`verdict ${verdict.passed ? 'passed' : 'failed'}`}>
        <StatusBadge status={verdict.passed ? 'succeeded' : 'failed'} />
        <div><strong>{verdict.strategy}</strong><p>{verdict.reason}</p>
          {verdict.unmet && verdict.unmet.length > 0 && <ul>{verdict.unmet.map((item) => <li key={item}>미충족: {item}</li>)}</ul>}
        </div>
      </div>
    </section>}

    {plan && <section className="detail-section"><h4>실행 계획 <small>{plan.mode}</small></h4>
      <pre className="runtime-log-preview custom-scroll">{JSON.stringify(plan.steps, null, 1)}</pre>
    </section>}

    <section className="detail-section"><h4>타임라인</h4>
      <ol className="run-timeline">{events.map((event) => <li key={event.id} className={event.type.replace('.', '-')}>
        <time>{new Date(event.occurredAt).toLocaleTimeString('ko-KR')}</time>
        <div><strong>{event.type}</strong>{event.message && <p>{event.message}</p>}</div>
      </li>)}</ol>
    </section>

    {artifacts.length > 0 && <section className="detail-section"><h4>산출물</h4>
      <div className="tool-links">{artifacts.map((artifact) => <a key={artifact.id} className="artifact-link"
        href={`/api/v1/artifacts/${artifact.id}/content`} target="_blank" rel="noopener noreferrer">
        <ClipboardList size={17} />{artifact.name}<small>{artifact.type} · {artifact.sizeBytes.toLocaleString('ko-KR')}B</small>
      </a>)}</div>
    </section>}

    <section className="detail-section"><h4>단계별 기록</h4>
      <div className="run-trace">{steps.map((step) => <article key={step.id} className={step.status}>
        <header><strong>{step.title || step.type}</strong><StatusBadge status={step.status} />
          <small>{step.durationMs}ms · {step.promptTokens + step.completionTokens} 토큰</small></header>
        {step.error ? <p className="run-error">{step.error}</p> : <pre className="custom-scroll">{step.output}</pre>}
      </article>)}</div>
    </section>
  </Drawer>
}
