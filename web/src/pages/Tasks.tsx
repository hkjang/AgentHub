import { FormEvent, useCallback, useEffect, useMemo, useState } from 'react'
import { AlertTriangle, Bot, Check, ClipboardList, Clock3, Coins, ExternalLink, ListChecks, Play, Plus, Radio, RefreshCw, RotateCcw, Square, Terminal } from 'lucide-react'
import { api } from '../api'
import { ReviewFindings } from '../components/ReviewFindings'
import { RunDirectives } from '../components/RunDirectives'
import { useAuth } from '../App'
import { Link } from 'react-router-dom'
import { ConfirmDialog, Drawer, Empty, ErrorBanner, GuideLegend, GuidePanel, Loading, PageHeader, StatusBadge, SuccessBanner, statusLabel, useEscape } from '../components/UI'
import { useTerms } from '../viewmode'
import { relativeTime, runtimeCode, runtimeLabel, runtimeLogoClass } from '../runtime'
import { metering as meteringOf } from '../metering'
import type { Agent, AgentArtifact, AgentPlan, AgentRun, AgentRunEvent, AgentRunStep, AgentTask, PlatformEvent, QueueSnapshot, UsageBudget, UsageReport } from '../types'

/** Statuses that are still moving, and therefore worth polling for. */
const ACTIVE = ['queued', 'planning', 'ready', 'running', 'waiting_tool', 'waiting_approval', 'retrying', 'blocked', 'handoff']

const PRIORITY_LABELS: Record<string, string> = {
  critical: '긴급', high: '높음', normal: '보통', low: '낮음', background: '배경',
}

const SOURCE_LABELS: Record<string, string> = {
  manual: '직접 실행', cron: '예약', webhook: 'Webhook', agent: '다른 에이전트', event: '이벤트', mcp: '외부 에이전트 (MCP)',
}

/** What a status means for the person reading it, not what the worker calls it. */
const STATUS_MEANINGS: Record<string, string> = {
  queued: '워커가 가져가기를 기다립니다',
  planning: '실행 계획을 세우는 중입니다',
  running: '에이전트가 수행하는 중입니다',
  waiting_tool: '도구 응답을 기다립니다',
  waiting_approval: '사람이 승인해야 이어집니다',
  blocked: '에이전트 정의가 운영 승격되면 자동으로 실행됩니다',
  handoff: '에이전트가 할 수 없는 일이 남아 사람에게 넘겼습니다 · 런타임에서 이어받으세요',
  retrying: '실패 후 자동으로 다시 시도합니다',
  completed: '완료 조건을 충족하고 끝났습니다',
  failed: '실패했습니다 · 다시 실행할 수 있습니다',
  dead_letter: '재시도를 모두 소진했습니다',
  cancelled: '사람이 취소했습니다',
}

/** Example task inputs, so the first task is not written from a blank page. */
const TASK_EXAMPLES = [
  { label: '저장소 정리', input: '작업공간의 오래된 브랜치와 사용하지 않는 의존성을 찾아 정리 계획을 정리하고, 실제로 적용할 변경은 목록으로 남겨 주세요.' },
  { label: '테스트 보강', input: '테스트가 없는 모듈을 찾아 실패 케이스부터 테스트를 추가하고, 결과를 요약해 주세요.' },
  { label: '로그 조사', input: '어제 발생한 오류 로그를 조사해 원인 후보와 근거, 확인 방법을 정리해 주세요.' },
]

const EVENT_LABELS: Record<string, string> = {
  'task.completed': '작업 완료', 'task.failed': '작업 실패', 'task.dead_lettered': '재시도 소진',
  'task.handoff': '작업 인계',
  'approval.decided': '승인 처리', 'runtime.failed': '런타임 장애', 'artifact.created': '산출물 생성',
}

export function Tasks() {
  const t = useTerms()
  const { capabilities } = useAuth()
  const [tasks, setTasks] = useState<AgentTask[]>()
  const [counts, setCounts] = useState<Record<string, number>>({})
  const [agents, setAgents] = useState<Agent[]>([])
  const [queue, setQueue] = useState<QueueSnapshot>()
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const [filter, setFilter] = useState('')
  const [creating, setCreating] = useState(false)
  const [openRun, setOpenRun] = useState<string | null>(null)
  const [cancelling, setCancelling] = useState<AgentTask | null>(null)
  const [retrying, setRetrying] = useState<AgentTask | null>(null)
  const [resolving, setResolving] = useState<AgentTask | null>(null)
  const [busy, setBusy] = useState(false)

  const load = useCallback(async () => {
    try {
      const [taskResult, agentResult, queueResult] = await Promise.all([
        api.get<{ items?: AgentTask[]; counts?: Record<string, number> }>('/api/v1/tasks'),
        api.get<{ items?: Agent[] }>('/api/v1/agents'),
        api.get<QueueSnapshot>('/api/v1/queue'),
      ])
      setTasks(taskResult.items ?? [])
      setCounts(taskResult.counts ?? {})
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
  // Counted in the database. Counting the page said "승인 대기 0" while work sat
  // waiting: the page holds the newest hundred tasks, and the thing waiting for a
  // person is the thing that gets old.
  const active = ACTIVE.reduce((total, status) => total + (counts[status] ?? 0), 0)

  const cancel = async () => {
    if (!cancelling) return
    setBusy(true)
    try {
      const result = await api.post<{ delegated?: number }>(`/api/v1/tasks/${cancelling.id}/cancel`)
      setCancelling(null)
      if (result?.delegated) setNotice(`위임한 작업 ${result.delegated}건도 함께 취소했습니다.`)
      await load()
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Task를 취소하지 못했습니다.')
    } finally { setBusy(false) }
  }

  const retry = async (task: AgentTask, fresh: boolean) => {
    setError('')
    setRetrying(null)
    try {
      await api.post(`/api/v1/tasks/${task.id}/retry`, { fresh })
      await load()
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Task를 다시 실행하지 못했습니다.')
    }
  }

  /**
   * Opens the runtime for a handed-off task, in the same workspace the agent left
   * its work in. It starts the runtime when it is not running: somebody told to
   * take over should not have to find the runtime screen first.
   */
  const openRuntime = async (task: AgentTask) => {
    setError('')
    try {
      const agent = agents.find((item) => item.id === task.agentId)
      const runtimeId = agent?.runtime?.id
      if (!runtimeId || ['stopped', 'failed', 'crashed'].includes(agent?.runtime?.status ?? '')) {
        await api.post(`/api/v1/agents/${task.agentId}/${runtimeId ? 'start' : 'spawn'}`, {})
        setError('런타임을 시작했습니다. 준비되면 다시 눌러 작업공간을 여세요.')
        await load()
        return
      }
      const session = await api.post<{ url: string }>(`/api/v1/runtimes/${runtimeId}/launch`, {})
      window.open(session.url, '_blank', 'noopener,noreferrer')
    } catch (e) {
      setError(e instanceof Error ? e.message : '런타임을 열지 못했습니다.')
    }
  }

  const resolve = async (task: AgentTask, status: 'completed' | 'cancelled', note: string) => {
    setError('')
    setResolving(null)
    try {
      await api.post(`/api/v1/tasks/${task.id}/resolve`, { status, note })
      await load()
    } catch (e) {
      setError(e instanceof Error ? e.message : '작업을 마무리하지 못했습니다.')
    }
  }

  return <div className="page">
    <PageHeader eyebrow="실행 플레인" title={t('tasks')}
      description="에이전트에게 맡긴 일이 대기열에 쌓이고, 워커가 가져가 스스로 수행한 결과가 여기에 남습니다."
      actions={<>
        <button className="button ghost" onClick={() => void load()}><RefreshCw size={15} />새로고침</button>
        <button className="button primary" disabled={agents.length === 0} onClick={() => setCreating(true)}><Plus size={16} />새 작업</button>
      </>} />
    {error && <ErrorBanner message={error} onClose={() => setError('')} />}
    {notice && <SuccessBanner message={notice} />}
    {/* A queue that stops moving with no explanation is indistinguishable from a
        broken one, and the people waiting are not administrators. */}
    {capabilities.executionPaused && <div className="alert warning">
      <AlertTriangle size={18} />
      <span>관리자가 실행을 일시 중지했습니다{capabilities.executionPausedReason ? ` — ${capabilities.executionPausedReason}` : ''}. 새 작업은 등록되지만 재개될 때까지 실행되지 않습니다.</span>
    </div>}

    <GuidePanel id="tasks" title="작업 대기열은 이렇게 사용합니다" steps={[
      { title: '작업을 맡깁니다', body: <><b>새 작업</b>에서 에이전트와 할 일을 적어 대기열에 넣습니다. 사람이 지켜보지 않아도 진행되며, 예약·Webhook·플랫폼 이벤트로 자동 등록되게 하려면 <Link to="/agents">내 에이전트</Link> 상세의 <b>목표·Trigger</b>에서 설정합니다.</> },
      { title: '워커가 가져갑니다', body: <>대기 중인 작업은 워커가 우선순위 순서로 가져가고, 필요하면 그 에이전트의 런타임을 자동으로 띄웁니다. 아래 <b>대기 · 실행 · 워커</b> 숫자가 지금 상태이고, 대기열이 밀리면 워커 수는 스스로 늘어납니다.</> },
      { title: '진행을 따라갑니다', body: <>상태 칩으로 걸러 보고, 각 행의 <b>실행 기록</b>에서 계획·단계별 수행 내역·산출물·토큰 사용량까지 확인합니다. 목록은 5초마다 자동 갱신됩니다.</> },
      { title: '막힌 작업을 처리합니다', body: <><b>승인 대기</b>는 <Link to="/reviews">검토 · 승인</Link>에서 승인하면 이어서 진행되고, <b>실패</b>·<b>처리 불가</b>는 원인을 고친 뒤 다시 실행할 수 있습니다.</> },
      { title: '런타임에서 이어받습니다', body: <>자동 실행은 모델과 글로만 주고받는 루프여서 파일 편집·명령 실행을 하지 못합니다. 그런 일이 남으면 에이전트가 <b>런타임 인계</b> 상태로 넘기고, 각 행의 <b>런타임 열기</b>로 같은 작업공간을 열어 직접 마무리한 뒤 <b>완료 처리</b>를 누르면 됩니다.</> },
    ]} footer={<GuideLegend items={['queued','running','waiting_approval','handoff','blocked','retrying','completed','failed','dead_letter'].map((status) => ({ label: <StatusBadge status={status} />, meaning: STATUS_MEANINGS[status] ?? '' }))} />} />

    {tasks.length > 0 && <div className="toolbar">
      <div className="filter-chips">
        <button className={filter === '' ? 'selected' : ''} onClick={() => setFilter('')}>전체 {tasks.length}</button>
        {['running', 'queued', 'waiting_approval', 'handoff', 'blocked', 'retrying', 'completed', 'failed', 'dead_letter'].filter((status) => counts[status])
          .map((status) => <button key={status} title={STATUS_MEANINGS[status]} className={filter === status ? 'selected' : ''} onClick={() => setFilter(status)}>
            {statusLabel(status)} {counts[status]}
          </button>)}
      </div>
      <span className="row-time" title="대기: 아직 가져가지 않은 작업 · 실행: 지금 수행 중인 작업 · 워커: 동시에 실행할 수 있는 수(대기열에 따라 자동 조절)">
        <Clock3 size={14} />
        {queue ? `대기 ${queue.ready} · 실행 ${queue.running} · 워커 ${queue.workers}` : `진행 중 ${active}건`} · 5초마다 갱신
      </span>
    </div>}

    {tasks.length === 0
      ? <Empty icon={<ListChecks />} title="아직 작업이 없습니다"
          description={agents.length
            ? '에이전트에게 할 일을 맡기면 워커가 가져가 수행하고, 그 기록이 여기에 남습니다.'
            : '먼저 에이전트를 만들어야 작업을 맡길 수 있습니다.'}
          action={agents.length
            ? <button className="button primary" onClick={() => setCreating(true)}><Plus size={16} />첫 작업 만들기</button>
            : <Link className="button primary" to="/catalog"><Plus size={16} />에이전트 만들기</Link>} />
      : visible.length === 0
        ? <div className="empty-compact">해당 상태의 작업이 없습니다.</div>
        : <section className="table-panel"><div className="table-wrap custom-scroll"><table>
            <thead><tr><th>작업</th><th>에이전트</th><th>상태</th><th title="워커가 가져가는 순서">우선순위</th><th title="이 작업이 어떻게 등록되었는지 (직접 실행 · 예약 · Webhook · 이벤트 · 다른 에이전트)">출처</th><th title="지금까지 실행을 시도한 횟수">시도</th><th>마지막 변경</th><th aria-label="작업" /></tr></thead>
            <tbody>{visible.map((task) => <tr key={task.id}>
              <td>
                <div className="agent-main">
                  <strong>{task.title}</strong>
                  {/* Waiting and failing read differently because they are
                      different: one clears by itself, the other needs somebody. */}
                  {task.waitingReason && <span className="task-waiting" title={task.waitingReason}>{task.waitingReason}</span>}
                  {/* A blocked task did not fail: something is holding it — a
                      promotion gate, a policy rule — and it runs when that is
                      lifted. Painting the reason in the failure colour said the
                      opposite of what the platform will do next. */}
                  {task.lastError && <span className={task.status === 'blocked' ? 'task-waiting' : 'task-error'} title={task.lastError}>{task.lastError}</span>}
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
                {task.status === 'handoff' && <>
                  <button title="런타임 열기 — 같은 작업공간에서 이어서 작업합니다" onClick={() => void openRuntime(task)}><Terminal size={15} /></button>
                  <button title="완료 처리 — 사람이 마무리했음을 기록합니다" onClick={() => setResolving(task)}><Check size={15} /></button>
                </>}
                {task.currentRunId && <button title={t('runs')} onClick={() => setOpenRun(task.currentRunId!)}><ExternalLink size={15} /></button>}
                {/* A blocked task is here too: the block is lifted by fixing what
                    caused it, and until now only a promotion had a way back —
                    which is no help at all to a task a policy rule stopped. */}
                {['failed', 'dead_letter', 'cancelled', 'blocked'].includes(task.status) && <button title={task.status === 'blocked' ? '막은 것을 해제한 뒤 다시 실행' : '다시 실행'} onClick={() => setRetrying(task)}><RotateCcw size={15} /></button>}
                {ACTIVE.includes(task.status) && <button className="danger" title="취소" onClick={() => setCancelling(task)}><Square size={14} /></button>}
              </div></td>
            </tr>)}</tbody>
          </table></div></section>}

    <UsagePanel />

    <EventFeed />

    {retrying && <RetryDialog task={retrying} close={() => setRetrying(null)} retry={(fresh) => void retry(retrying, fresh)} />}
    {resolving && <ResolveDialog task={resolving} busy={busy} close={() => setResolving(null)} resolve={(task, status, note) => void resolve(task, status, note)} />}
    {creating && <CreateTaskDrawer agents={agents} close={() => setCreating(false)} done={() => { setCreating(false); void load() }} />}
    {openRun && <RunDrawer runId={openRun} close={() => setOpenRun(null)} />}
    {cancelling && <ConfirmDialog title="작업을 취소할까요?"
      /* It used to say the step finishes and the task stops, and stopped there.
         Two things were missing: the run is actually interrupted now, and the
         sub-tasks this one handed to other agents stop with it. */
      message={<><strong>{cancelling.title}</strong> 작업이 취소됩니다. 실행 중이면 워커가 몇 초 안에 알아채고 중단하며, 이미 시작된 모델 호출이나 도구 실행은 그 호출이 끝나는 시점까지 진행될 수 있습니다.<br/>
        이 작업이 다른 에이전트에게 위임한 작업 중 <strong>아직 끝나지 않은 것도 함께 취소</strong> 됩니다.</>}
      confirmLabel="취소하기" busy={busy}
      onConfirm={() => void cancel()} onCancel={() => setCancelling(null)} />}
  </div>
}

/**
 * Retrying asks which kind of retry: continuing from the steps already completed,
 * or starting the task over. The count comes from the server, so the choice is
 * made with the amount of reusable work in view rather than as a guess.
 */
export function RetryDialog({ task, close, retry }: { task: AgentTask; close: () => void; retry: (fresh: boolean) => void }) {
  const [checkpoint, setCheckpoint] = useState<{ steps: number; enabled: boolean }>()
  const [error, setError] = useState('')
  // Every other overlay closes on Escape; this one is a dialog too.
  useEscape(close)
  useEffect(() => {
    void api.get<{ steps: number; enabled: boolean }>(`/api/v1/tasks/${task.id}/checkpoint`)
      .then(setCheckpoint)
      .catch((e) => setError(e instanceof Error ? e.message : '이어서 실행할 단계를 확인하지 못했습니다.'))
  }, [task.id])
  const steps = checkpoint?.steps ?? 0
  const resumable = steps > 0 && checkpoint?.enabled !== false
  return <div className="drawer-layer">
    <button className="drawer-scrim" onClick={close} aria-label="닫기" />
    <div className="confirm-dialog" role="alertdialog" aria-modal="true" aria-label="작업을 다시 실행">
      <div className="confirm-icon"><RotateCcw size={22} /></div>
      <h3>작업을 다시 실행할까요?</h3>
      <div className="confirm-body">
        <strong>{task.title}</strong>
        {error
          ? <p>{error}</p>
          : !checkpoint
            ? <p>이어서 실행할 수 있는 단계를 확인하고 있습니다…</p>
            : resumable
              ? <p>이미 완료한 <b>{steps}단계</b>가 있습니다. 이어서 실행하면 그 단계를 다시 수행하지 않고 다음 단계부터 진행합니다.</p>
              : <p>{checkpoint.enabled === false
                  ? '이 에이전트는 이어서 실행이 꺼져 있어 처음부터 다시 실행합니다.'
                  : '완료한 단계가 없어 처음부터 실행합니다.'}</p>}
      </div>
      <div className="confirm-actions">
        <button className="button ghost" onClick={close}>취소</button>
        <button className="button ghost" onClick={() => retry(true)}>처음부터</button>
        <button className="button primary" disabled={!resumable} onClick={() => retry(false)} autoFocus>이어서 실행</button>
      </div>
    </div>
  </div>
}

/** Token spend over the last 30 days.
 *  Autonomous agents run unattended, which is exactly when a runaway loop costs
 *  money quietly, so the bill belongs next to the queue rather than buried in an
 *  admin screen. */
function UsagePanel() {
  const [report, setReport] = useState<UsageReport>()
  const [budget, setBudget] = useState<UsageBudget | null>(null)
  const [open, setOpen] = useState(false)
  const [error, setError] = useState('')
  useEffect(() => {
    if (!open || report) return
    void api.get<UsageReport & { budget?: UsageBudget }>('/api/v1/usage')
      .then((result) => { setReport(result); setBudget(result.budget ?? null) })
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
              {budget && <div className="usage-budget">
                {budget.tokenBudget > 0 && <span>토큰 예산 <b>{tokens(budget.tokensUsed)} / {tokens(budget.tokenBudget)}</b> (최근 {budget.windowDays}일)</span>}
                {budget.costBudget > 0 && <span>비용 예산 <b>{money(budget.costUsed, budget.currency)} / {money(budget.costBudget, budget.currency)}</b></span>}
                {budget.maxRunning > 0 && <span>동시 실행 <b>{budget.runningNow} / {budget.maxRunning}</b></span>}
                {budget.department && <>
                  {budget.department.tokenBudget > 0 && <span>{budget.department.name} 토큰 <b>{tokens(budget.department.tokensUsed)} / {tokens(budget.department.tokenBudget)}</b></span>}
                  {budget.department.costBudget > 0 && <span>{budget.department.name} 비용 <b>{money(budget.department.costUsed, budget.currency)} / {money(budget.department.costBudget, budget.currency)}</b></span>}
                  {budget.department.maxRunning > 0 && <span>{budget.department.name} 동시 실행 <b>{budget.department.runningNow} / {budget.department.maxRunning}</b></span>}
                </>}
                <small>예산을 모두 쓰면 새 작업은 대기가 아니라 실패로 처리되고 알림이 갑니다.</small>
              </div>}
              <div className="usage-summary">
                <div><span>입력</span><strong>{tokens(report.inputTokens)}</strong></div>
                <div><span>출력</span><strong>{tokens(report.outputTokens)}</strong></div>
                <div><span>금액</span><strong>{money(report.cost, report.currency)}</strong></div>
                {report.unpricedTokens > 0 && <div className="unpriced">
                  <span>미산정</span><strong>{tokens(report.unpricedTokens)} 토큰</strong>
                </div>}
                {report.unmeteredRuns > 0 && <div className="unpriced">
                  <span>집계 안 된 실행</span><strong>{report.unmeteredRuns} / {report.runs}건</strong>
                </div>}
              </div>
              <div className="table-wrap custom-scroll"><table>
                <thead><tr><th>에이전트</th><th>모델</th><th>실행</th><th>입력</th><th>출력</th><th>금액</th><th title="에이전트가 사용량을 알려주지 않아 왼쪽 숫자에 들어 있지 않은 실행">집계 안 됨</th></tr></thead>
                <tbody>{report.agents.map((row) => <tr key={`${row.agentId}-${row.modelName}`}>
                  <td>{row.agentName}</td>
                  <td>{row.modelName || '—'}</td>
                  <td>{row.runs}</td>
                  <td>{tokens(row.inputTokens)}</td>
                  <td>{tokens(row.outputTokens)}</td>
                  <td>{row.priced ? money(row.cost, row.currency) : <span className="row-time">미산정</span>}</td>
                  {/* A row of zeroes is unreadable without this: it says whether the
                      agent spent nothing or simply never said. */}
                  <td>{row.unmeteredRuns > 0
                    ? <span className="metering-tag warn" title="이 에이전트는 사용량을 보고하지 않습니다. 계량되는 런타임을 쓰거나 에이전트 토큰 예산을 지정하세요.">{row.unmeteredRuns}건</span>
                    : <span className="row-time">—</span>}</td>
                </tr>)}</tbody>
              </table></div>
              {report.unpricedTokens > 0 && <small>단가가 등록되지 않은 모델의 토큰은 금액에 포함되지 않습니다. 관리자 · 리소스 · 모델 엔드포인트에서 단가를 입력하세요.</small>}
              {/* A total is not evidence unless it says what it could not see. */}
              {report.unmeteredRuns > 0 && <small>실행 {report.runs}건 중 {report.unmeteredRuns}건은 에이전트가 사용량을 알려주지 않아 위 숫자에 들어 있지 않습니다. 실행 상세에서 어떤 실행인지 확인할 수 있습니다.</small>}
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
          <EventDeliveryState event={event} />
        </li>)}</ul>)}
  </section>
}

/**
 * What happened to one event. The feed used to say only "전달 대기", which covered
 * both an event still on its way and one that had been given up on — and said
 * nothing about whether a subscriber actually received it.
 */
function EventDeliveryState({ event }: { event: PlatformEvent }) {
  const subscribers = event.deliveries > 0
    ? <span className="event-delivered" title={event.deliveredTo ? `전달된 Trigger: ${event.deliveredTo}` : undefined}>구독 {event.deliveries}건 전달</span>
    : null
  if (event.deadLetteredAt) {
    return <><span className="event-failed" title={event.lastError}>전달 실패 · {event.attempts}회 시도</span>{subscribers}</>
  }
  if (event.dispatchedAt) {
    return subscribers ?? <span className="event-delivered">전달 완료 · 구독자 없음</span>
  }
  if (event.attempts > 0) {
    return <><span className="event-pending" title={event.lastError}>재시도 대기 · {event.attempts}회 시도</span>{subscribers}</>
  }
  return <span className="event-pending">전달 대기</span>
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
      <label><span>작업 내용</span>
        <textarea rows={6} value={input} onChange={(e) => setInput(e.target.value)} placeholder="에이전트가 수행할 작업을 구체적으로 적으세요." />
        <small>완료 여부를 판단할 수 있게 구체적으로 적는 편이 좋습니다. 비워 두면 에이전트에 설정된 목표만으로 실행됩니다.</small>
      </label>
      <div className="example-buttons">{TASK_EXAMPLES.map((example) => <button type="button" key={example.label} onClick={() => setInput(example.input)}>{example.label} 예시</button>)}</div>
      <label><span>우선순위</span>
        <select value={priority} onChange={(e) => setPriority(e.target.value)}>
          {Object.entries(PRIORITY_LABELS).map(([value, label]) => <option key={value} value={value}>{label}</option>)}
        </select>
        <small>워커가 가져가는 순서만 정합니다. 실행 속도나 한도는 달라지지 않습니다.</small>
      </label>
      <div className="info-box"><Bot size={17} /><div><strong>등록하면 이렇게 진행됩니다</strong>
        <p>대기열에 들어가면 워커가 우선순위 순으로 가져가 에이전트의 목표·완료 조건에 따라 수행하고, 필요하면 런타임을 자동으로 띄웁니다. 승인이 필요한 조치를 만나면 <b>승인 대기</b>로 멈추고 검토자에게 알림이 갑니다.</p></div></div>
    </form>
  </Drawer>
}

/** The full picture of one attempt: timeline, per-step reasoning and artifacts. */
export function RunDrawer({ runId, close }: { runId: string; close: () => void }) {
  const t = useTerms()
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

  if (error) return <Drawer title={t('runs')} close={close}><ErrorBanner message={error} /></Drawer>
  if (!data) return <Drawer title={t('runs')} close={close}><Loading /></Drawer>

  const { run, steps, events, artifacts, plan } = data
  const verdict = run.completion ?? {}
  // Who counted this run's tokens, shown beside the number: zero can mean the
  // platform made no model calls, or that the agent never said what it spent.
  const meter = meteringOf(run.metering)
  return <Drawer title={`${t('runs')} #${run.id.slice(0, 8)}`} subtitle={`시도 ${run.attempt} · ${run.modelName || '모델 미지정'}`} close={close}>
    <div className="detail-hero">
      <div className="runtime-logo xlarge custom"><ClipboardList size={22} /></div>
      <div><StatusBadge status={run.status} /><h3>{run.durationMs}ms · {run.stepCount}단계{run.resumedSteps > 0 ? ` (이전 시도 ${run.resumedSteps}단계 이어받음)` : ''}</h3>
        <p>토큰 {run.totalTokens.toLocaleString('ko-KR')}{meter && <span className={`metering-tag ${meter.tone}`} title={meter.hint}>{meter.label}</span>} · trace <code>{run.traceId || '—'}</code></p></div>
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

    {(steps.some((step) => step.type === 'rpc') || !run.finishedAt) && <RunDirectives runId={run.id} live={!run.finishedAt} />}

    {steps.some((step) => step.type === 'review') && <ReviewFindings runId={run.id} />}

    <section className="detail-section"><h4>타임라인</h4>
      <ol className="run-timeline">{events.map((event) => <li key={event.id} className={event.type.replace('.', '-')}>
        <time>{new Date(event.occurredAt).toLocaleTimeString('ko-KR')}</time>
        <div><strong>{event.type}</strong>{event.message && <p>{event.message}</p>}</div>
      </li>)}</ol>
    </section>

    {artifacts.length > 0 && <section className="detail-section"><h4>산출물</h4>
      {/* A screenshot is shown rather than listed: an agent that says "the page
          shows an error" is making a claim, and the picture is the evidence for
          it. A link somebody has to open is evidence nobody looks at. */}
      {artifacts.some((v) => v.type === 'image') && <div className="artifact-shots">
        {artifacts.filter((v) => v.type === 'image').map((artifact) => <figure key={artifact.id}>
          <a href={`/api/v1/artifacts/${artifact.id}/content`} target="_blank" rel="noopener noreferrer">
            <img src={`/api/v1/artifacts/${artifact.id}/content`} alt={artifact.name} loading="lazy"/>
          </a>
          <figcaption>{artifact.name}</figcaption>
        </figure>)}
      </div>}
      <div className="tool-links">{artifacts.filter((v) => v.type !== 'image').map((artifact) => <a key={artifact.id} className="artifact-link"
        href={`/api/v1/artifacts/${artifact.id}/content`} target="_blank" rel="noopener noreferrer">
        <ClipboardList size={17} />{artifact.name}<small>{artifact.type} · {artifact.sizeBytes.toLocaleString('ko-KR')}B</small>
      </a>)}</div>
    </section>}

    <section className="detail-section"><h4>{t('runSteps')}</h4>
      <div className="run-trace">{steps.map((step) => <article key={step.id} className={step.status}>
        <header><strong>{step.title || step.type}</strong><StatusBadge status={step.status} />
          <small>{step.durationMs}ms · {step.promptTokens + step.completionTokens} 토큰</small></header>
        {step.error ? <p className="run-error">{step.error}</p> : <pre className="custom-scroll">{step.output}</pre>}
      </article>)}</div>
    </section>
  </Drawer>
}

/**
 * Closing a task a person took over in the runtime.
 *
 * The note replaces the agent's last message as the task's final word, so "what
 * did we actually do about this" survives the handover instead of ending at
 * "somebody was asked to look at it".
 */
function ResolveDialog({ task, busy, close, resolve }: {
  task: AgentTask; busy: boolean; close: () => void
  resolve: (task: AgentTask, status: 'completed' | 'cancelled', note: string) => void
}) {
  const [note, setNote] = useState('')
  const [status, setStatus] = useState<'completed' | 'cancelled'>('completed')
  useEscape(close)
  return <div className="drawer-layer">
    <button className="drawer-scrim" onClick={close} aria-label="취소" />
    <div className="confirm-dialog" role="alertdialog" aria-modal="true" aria-label="인계 작업 마무리">
      <div className="confirm-icon"><Check size={22} /></div>
      <h3>런타임에서 이어받은 작업을 마무리할까요?</h3>
      <div className="confirm-body">
        <strong>{task.title}</strong> — 에이전트가 남긴 요청: {task.lastError || '내용 없음'}
        <label className="confirm-field"><span>처리 결과</span>
          <select value={status} onChange={(e) => setStatus(e.target.value as 'completed' | 'cancelled')}>
            <option value="completed">완료 — 사람이 마무리했습니다</option>
            <option value="cancelled">취소 — 하지 않기로 했습니다</option>
          </select>
        </label>
        <label className="confirm-field"><span>기록</span>
          <input value={note} onChange={(e) => setNote(e.target.value)} placeholder="무엇을 했는지 한 줄로 남기세요" />
        </label>
      </div>
      <div className="confirm-actions">
        <button className="button ghost" onClick={close} disabled={busy}>취소</button>
        <button className="button primary" disabled={busy} onClick={() => resolve(task, status, note)}>{busy ? '처리 중…' : '마무리'}</button>
      </div>
    </div>
  </div>
}
