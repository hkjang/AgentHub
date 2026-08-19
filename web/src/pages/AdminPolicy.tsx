import { FormEvent, useCallback, useEffect, useState } from 'react'
import { ArrowDown, ArrowUp, Braces, FlaskConical, Plus, Save, ShieldCheck, Trash2 } from 'lucide-react'
import { api } from '../api'
import { ConfirmDialog, Drawer, ErrorBanner, GuidePanel, Loading, PageHeader, SuccessBanner } from '../components/UI'

/**
 * The central policy.
 *
 * The controls this joins up were all real and all separate — an allow list on
 * one agent, a global approval switch, ownership, a quota — so the sentence a
 * security review asks for had nowhere to live. Order is the policy here, first
 * match wins, so the screen is a list you read top to bottom, and the simulator
 * beside it answers "which rule decided" without anyone having to infer it.
 */

type Rule = {
  id: string; description?: string; enabled?: boolean; effect: string
  actions?: string[]; roles?: string[]; users?: string[]; agents?: string[]
  servers?: string[]; tools?: string[]; dataClasses?: string[]; reason?: string
}
type Document = { defaultEffect?: string; rules: Rule[] }
type Loaded = { document: Document; actions: string[]; effects: string[]; roles: string[] }
type Decision = { effect: string; ruleId?: string; reason?: string; matched?: string[] }

const EFFECT_LABELS: Record<string, string> = { allow: '허용', deny: '차단', require_approval: '승인 필요' }
const ACTION_LABELS: Record<string, string> = {
  'task.create': '작업 생성', 'runtime.start': '런타임 시작', 'tool.call': 'MCP 도구 호출',
  'model.call': '모델 호출', 'workflow.run': '워크플로 실행', 'agent.update': '에이전트 수정',
}
const emptyRule = (): Rule => ({ id: '', effect: 'deny', actions: ['tool.call'], reason: '' })
const list = (values?: string[]) => (values ?? []).join(', ')
const parseList = (value: string) => value.split(',').map((item) => item.trim()).filter(Boolean)

/** A rule reads as a sentence, or nobody will check it against what they meant. */
function summarise(rule: Rule) {
  const parts: string[] = []
  if (rule.roles?.length) parts.push(`역할 ${list(rule.roles)}`)
  if (rule.users?.length) parts.push(`사용자 ${list(rule.users)}`)
  if (rule.agents?.length) parts.push(`에이전트 ${list(rule.agents)}`)
  if (rule.servers?.length) parts.push(`서버 ${list(rule.servers)}`)
  if (rule.tools?.length) parts.push(`도구 ${list(rule.tools)}`)
  if (rule.dataClasses?.length) parts.push(`데이터 ${list(rule.dataClasses)}`)
  return parts.length ? parts.join(' · ') : '모든 요청'
}

export function AdminPolicy() {
  const [loaded, setLoaded] = useState<Loaded | null>(null)
  const [document, setDocument] = useState<Document>({ rules: [] })
  const [dirty, setDirty] = useState(false)
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const [busy, setBusy] = useState(false)
  const [editing, setEditing] = useState<{ rule: Rule; index: number } | null>(null)
  const [removing, setRemoving] = useState<number | null>(null)
  const [raw, setRaw] = useState<string | null>(null)

  const load = useCallback(async () => {
    try {
      const result = await api.get<Loaded>('/api/v1/admin/policy')
      setLoaded(result)
      setDocument({ defaultEffect: result.document.defaultEffect || 'allow', rules: result.document.rules ?? [] })
      setDirty(false)
    } catch (e) { setError(e instanceof Error ? e.message : '정책을 불러오지 못했습니다.') }
  }, [])
  useEffect(() => { void load() }, [load])

  const change = (next: Document) => { setDocument(next); setDirty(true); setNotice('') }
  const move = (index: number, by: number) => {
    const rules = [...document.rules]
    const target = index + by
    if (target < 0 || target >= rules.length) return
    ;[rules[index], rules[target]] = [rules[target], rules[index]]
    change({ ...document, rules })
  }
  const save = async () => {
    setBusy(true); setError('')
    try {
      const result = await api.put<{ message: string }>('/api/v1/admin/policy', document)
      setNotice(result.message); setDirty(false); await load()
    } catch (e) { setError(e instanceof Error ? e.message : '정책을 저장하지 못했습니다.') }
    finally { setBusy(false) }
  }
  const applyRaw = () => {
    try {
      const parsed = JSON.parse(raw ?? '') as Document
      if (!Array.isArray(parsed.rules)) throw new Error('rules 배열이 필요합니다.')
      change(parsed); setRaw(null)
    } catch (e) { setError(e instanceof Error ? `JSON을 읽지 못했습니다: ${e.message}` : 'JSON을 읽지 못했습니다.') }
  }

  if (!loaded) return <div className="page">{error ? <ErrorBanner message={error} /> : <Loading />}</div>
  return <div className="page">
    <PageHeader eyebrow="관리자 · 거버넌스" title="정책"
      description="사용자·에이전트·MCP 도구·데이터 등급을 대상으로 한 규칙을 한곳에서 정의하고, 작업 생성·런타임 시작·도구 호출 시점에 강제합니다."
      actions={<>
        <button className="button ghost" onClick={() => setRaw(JSON.stringify(document, null, 2))}><Braces size={15} />JSON으로 편집</button>
        <button className="button primary" disabled={busy || !dirty} onClick={() => void save()}><Save size={16} />정책 저장</button>
      </>} />
    <GuidePanel id="admin-policy" title="정책은 이렇게 동작합니다" steps={[
      { title: '위에서 아래로, 처음 맞는 규칙이 결정합니다', body: '방화벽 규칙과 같습니다. 좁은 예외(허용)를 넓은 차단 위에 두면 그 예외가 이깁니다.' },
      { title: '비워 둔 조건은 전체를 뜻합니다', body: '규칙은 자신이 채운 조건이 모두 맞을 때 적용됩니다. 도구 이름은 끝에 *를 붙여 "delete_*", "github/delete_*"처럼 쓸 수 있습니다.' },
      { title: '도구 규칙은 Pod 안에서 강제됩니다', body: '저장하면 실행 중인 런타임의 게이트웨이 설정까지 다시 씁니다. 에이전트가 우회할 수 없는 자리에서 막히고, 차단된 도구는 목록에도 보이지 않습니다.' },
      { title: '저장 전에 시뮬레이터로 확인하세요', body: '아래 시뮬레이터는 저장하지 않은 편집 내용 그대로 판정해 주고, 어떤 규칙이 결정했는지 보여 줍니다.' },
    ]} />
    {error && <ErrorBanner message={error} onClose={() => setError('')} />}
    {notice && <SuccessBanner message={notice} />}

    <section className="panel ops-panel">
      <div className="ops-form">
        <label>
          <span>기본 정책</span>
          <select value={document.defaultEffect ?? 'allow'} onChange={(e) => change({ ...document, defaultEffect: e.target.value })}>
            {loaded.effects.map((effect) => <option key={effect} value={effect}>{EFFECT_LABELS[effect] ?? effect}</option>)}
          </select>
          <small>어떤 규칙에도 맞지 않는 요청의 처리 방식입니다. 기본값은 허용입니다.</small>
        </label>
        <button className="button ghost" onClick={() => setEditing({ rule: emptyRule(), index: document.rules.length })}><Plus size={15} />규칙 추가</button>
      </div>
    </section>

    {document.rules.length === 0
      ? <section className="table-panel"><div className="empty-compact">규칙이 없습니다. 지금은 모든 요청이 기본 정책을 따릅니다.</div></section>
      : <section className="table-panel"><div className="table-wrap custom-scroll"><table>
          <thead><tr><th>순서</th><th>규칙</th><th>동작</th><th>조건</th><th>효과</th><th /></tr></thead>
          <tbody>{document.rules.map((rule, index) => <tr key={`${rule.id}-${index}`} className={rule.enabled === false ? 'rule-off' : ''}>
            <td>
              <div className="rule-order">
                <button title="위로" disabled={index === 0} onClick={() => move(index, -1)}><ArrowUp size={14} /></button>
                <span>{index + 1}</span>
                <button title="아래로" disabled={index === document.rules.length - 1} onClick={() => move(index, 1)}><ArrowDown size={14} /></button>
              </div>
            </td>
            <td><div className="mono-stack"><code>{rule.id}</code><small>{rule.description || rule.reason || '—'}</small></div></td>
            <td>{(rule.actions?.length ? rule.actions : ['*']).map((action) => ACTION_LABELS[action] ?? action).join(', ')}</td>
            <td><span className="rule-scope">{summarise(rule)}</span></td>
            <td><span className={`effect-badge ${rule.effect}`}>{EFFECT_LABELS[rule.effect] ?? rule.effect}</span>{rule.enabled === false && <span className="version-tag muted">사용 안 함</span>}</td>
            <td><div className="row-actions">
              <button title="수정" onClick={() => setEditing({ rule, index })}><ShieldCheck size={15} /></button>
              <button className="danger" title="삭제" onClick={() => setRemoving(index)}><Trash2 size={15} /></button>
            </div></td>
          </tr>)}</tbody>
        </table></div></section>}

    <Simulator document={document} actions={loaded.actions} roles={loaded.roles} />

    {editing && <RuleDrawer rule={editing.rule} actions={loaded.actions} effects={loaded.effects} roles={loaded.roles}
      close={() => setEditing(null)}
      save={(rule) => {
        const rules = [...document.rules]
        rules[editing.index] = rule
        change({ ...document, rules })
        setEditing(null)
      }} />}
    {raw !== null && <Drawer title="정책 JSON" subtitle="규칙 전체를 코드로 편집합니다. 저장하기 전에 시뮬레이터로 확인하세요." close={() => setRaw(null)}
      footer={<><button className="button ghost" onClick={() => setRaw(null)}>취소</button><button className="button primary" onClick={applyRaw}>적용</button></>}>
      <textarea className="policy-json" value={raw} spellCheck={false} onChange={(e) => setRaw(e.target.value)} />
    </Drawer>}
    {removing !== null && <ConfirmDialog title="규칙을 삭제할까요?"
      message={<><strong>{document.rules[removing]?.id}</strong> 규칙이 목록에서 제거됩니다. 저장해야 실제로 반영됩니다.</>}
      onConfirm={() => { change({ ...document, rules: document.rules.filter((_, index) => index !== removing) }); setRemoving(null) }}
      onCancel={() => setRemoving(null)} />}
  </div>
}

/** Editing one rule. Every selector is a comma-separated list, which is what
 *  people paste from a ticket. */
function RuleDrawer({ rule, actions, effects, roles, close, save }: {
  rule: Rule; actions: string[]; effects: string[]; roles: string[]; close: () => void; save: (rule: Rule) => void
}) {
  const [draft, setDraft] = useState<Rule>({ ...rule })
  const [error, setError] = useState('')
  const update = (patch: Partial<Rule>) => setDraft((current) => ({ ...current, ...patch }))
  const submit = (event: FormEvent) => {
    event.preventDefault()
    if (!draft.id.trim()) { setError('규칙 ID를 입력해 주세요.'); return }
    if (draft.effect !== 'allow' && !draft.reason?.trim()) { setError('차단·승인 규칙에는 사유가 필요합니다. 거절된 사람이 읽는 문장입니다.'); return }
    save({ ...draft, id: draft.id.trim() })
  }
  return <Drawer title={rule.id ? `규칙 ${rule.id}` : '새 규칙'} subtitle="비워 둔 조건은 전체를 뜻합니다." close={close}
    footer={<><button className="button ghost" onClick={close}>취소</button><button className="button primary" form="policy-rule">규칙 저장</button></>}>
    {error && <ErrorBanner message={error} onClose={() => setError('')} />}
    <form id="policy-rule" className="drawer-form" onSubmit={submit}>
      <label><span>규칙 ID</span><input value={draft.id} onChange={(e) => update({ id: e.target.value })} placeholder="deny-shell-tools" /></label>
      <label><span>설명</span><input value={draft.description ?? ''} onChange={(e) => update({ description: e.target.value })} placeholder="무엇을 위한 규칙인지" /></label>
      <label><span>효과</span>
        <select value={draft.effect} onChange={(e) => update({ effect: e.target.value })}>
          {effects.map((effect) => <option key={effect} value={effect}>{EFFECT_LABELS[effect] ?? effect}</option>)}
        </select>
        <small>허용은 아래의 넓은 차단보다 위에 둘 때 예외로 동작합니다.</small>
      </label>
      <fieldset className="scope-picker">
        <legend>적용 동작</legend>
        {actions.map((action) => <label key={action} className="scope-option">
          <input type="checkbox" checked={(draft.actions ?? []).includes(action)}
            onChange={() => update({ actions: (draft.actions ?? []).includes(action) ? (draft.actions ?? []).filter((v) => v !== action) : [...(draft.actions ?? []), action] })} />
          <span><strong>{ACTION_LABELS[action] ?? action}</strong><small><code>{action}</code></small></span>
        </label>)}
        <p className="field-hint">아무것도 선택하지 않으면 모든 동작에 적용됩니다.</p>
      </fieldset>
      <label><span>역할</span><input value={list(draft.roles)} onChange={(e) => update({ roles: parseList(e.target.value) })} placeholder={roles.join(', ')} /></label>
      <label><span>사용자</span><input value={list(draft.users)} onChange={(e) => update({ users: parseList(e.target.value) })} placeholder="아이디 또는 사용자 ID, 쉼표로 구분" /></label>
      <label><span>에이전트</span><input value={list(draft.agents)} onChange={(e) => update({ agents: parseList(e.target.value) })} placeholder="이름 또는 ID" /></label>
      <label><span>MCP 서버</span><input value={list(draft.servers)} onChange={(e) => update({ servers: parseList(e.target.value) })} placeholder="github, jira" /></label>
      <label><span>도구</span><input value={list(draft.tools)} onChange={(e) => update({ tools: parseList(e.target.value) })} placeholder="run_shell, github/delete_*" /></label>
      <label><span>데이터 등급</span><input value={list(draft.dataClasses)} onChange={(e) => update({ dataClasses: parseList(e.target.value) })} placeholder="rrn, card (내용 검사에서 발견된 등급)" /></label>
      <label><span>사유</span><input value={draft.reason ?? ''} onChange={(e) => update({ reason: e.target.value })} placeholder="차단될 때 사용자에게 보여 줄 문장" /></label>
      <label className="toggle-row"><span>규칙 사용</span><input type="checkbox" checked={draft.enabled !== false} onChange={(e) => update({ enabled: e.target.checked })} /><i /></label>
    </form>
  </Drawer>
}

/** "What would happen if" — the affordance that makes first-match order visible
 *  instead of something an operator has to infer. */
function Simulator({ document, actions, roles }: { document: Document; actions: string[]; roles: string[] }) {
  const [request, setRequest] = useState({ action: 'tool.call', role: 'user', user: '', agent: '', server: '', tool: '', dataClasses: '' })
  const [decision, setDecision] = useState<Decision | null>(null)
  const [error, setError] = useState('')
  const run = async (event: FormEvent) => {
    event.preventDefault()
    setError('')
    try {
      setDecision(await api.post<Decision>('/api/v1/admin/policy/simulate', {
        document,
        request: { ...request, dataClasses: parseList(request.dataClasses) },
      }))
    } catch (e) { setError(e instanceof Error ? e.message : '판정하지 못했습니다.'); setDecision(null) }
  }
  return <section className="panel ops-panel">
    <h3><FlaskConical size={16} /> 시뮬레이터</h3>
    <p className="field-hint">지금 화면에 있는(저장 전) 규칙으로 판정합니다. 어떤 규칙이 결정했는지까지 보여 줍니다.</p>
    {error && <ErrorBanner message={error} onClose={() => setError('')} />}
    <form className="ops-form" onSubmit={run}>
      <label><span>동작</span>
        <select value={request.action} onChange={(e) => setRequest({ ...request, action: e.target.value })}>
          {actions.map((action) => <option key={action} value={action}>{ACTION_LABELS[action] ?? action}</option>)}
        </select>
      </label>
      <label><span>역할</span>
        <select value={request.role} onChange={(e) => setRequest({ ...request, role: e.target.value })}>
          {roles.map((role) => <option key={role} value={role}>{role}</option>)}
        </select>
      </label>
      <label><span>사용자</span><input value={request.user} onChange={(e) => setRequest({ ...request, user: e.target.value })} placeholder="아이디" /></label>
      <label><span>에이전트</span><input value={request.agent} onChange={(e) => setRequest({ ...request, agent: e.target.value })} placeholder="이름 또는 ID" /></label>
      <label><span>MCP 서버</span><input value={request.server} onChange={(e) => setRequest({ ...request, server: e.target.value })} placeholder="github" /></label>
      <label><span>도구</span><input value={request.tool} onChange={(e) => setRequest({ ...request, tool: e.target.value })} placeholder="delete_branch" /></label>
      <label><span>데이터 등급</span><input value={request.dataClasses} onChange={(e) => setRequest({ ...request, dataClasses: e.target.value })} placeholder="rrn, card" /></label>
      <button className="button primary">판정</button>
    </form>
    {decision && <div className={`simulation ${decision.effect}`}>
      <strong>{EFFECT_LABELS[decision.effect] ?? decision.effect}</strong>
      <span>{decision.ruleId ? `규칙 ${decision.ruleId}` : '기본 정책'}{decision.reason ? ` — ${decision.reason}` : ''}</span>
      {(decision.matched?.length ?? 0) > 1 && <small>이 요청에 해당하는 규칙: {decision.matched?.join(' → ')} (첫 번째가 결정)</small>}
    </div>}
  </section>
}
