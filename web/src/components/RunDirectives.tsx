import { useCallback, useEffect, useState } from 'react'
import { CornerDownRight, Send } from 'lucide-react'
import { api } from '../api'
import { ErrorBanner } from './UI'

type Directive = { id:string; kind:'steer'|'follow_up'; message:string; createdAt:string; deliveredAt?:string; outcome?:string }

/** Saying something to an agent that is still working.
 *
 *  The other way to affect a running task is to cancel it, which stops
 *  everything. This is the smaller act: change the direction of the work, or add
 *  what to do after it.
 *
 *  What is deliberately shown is the difference between asked and delivered. The
 *  browser cannot reach the conversation directly — a worker process holds it —
 *  so a directive waits until that worker picks it up, and a screen that showed
 *  the request as the delivery would be claiming the agent had heard. */
export function RunDirectives({ runId, live }: { runId: string; live: boolean }) {
  const [items, setItems] = useState<Directive[]>([])
  const [message, setMessage] = useState('')
  const [kind, setKind] = useState<'steer'|'follow_up'>('steer')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  const load = useCallback(async () => {
    try { setItems((await api.get<{ items: Directive[] }>(`/api/v1/runs/${runId}/directives`)).items) }
    catch { /* a run that has none is not an error */ }
  }, [runId])
  useEffect(() => { void load() }, [load])
  // While something is pending the worker has not picked it up yet, so this asks
  // again — and stops as soon as everything has arrived.
  const waiting = items.some((item) => !item.deliveredAt)
  useEffect(() => {
    if (!live || !waiting) return
    const timer = window.setInterval(() => { if (document.visibilityState === 'visible') void load() }, 3000)
    return () => window.clearInterval(timer)
  }, [live, waiting, load])

  const send = async () => {
    const text = message.trim()
    if (!text) return
    setBusy(true); setError('')
    try { await api.post(`/api/v1/runs/${runId}/steer`, { kind, message: text }); setMessage(''); await load() }
    catch (e) { setError(e instanceof Error ? e.message : '전달하지 못했습니다.') }
    finally { setBusy(false) }
  }

  if (!live && items.length === 0) return null
  return <section className="detail-section run-directives">
    <h4>진행 중 지시 {items.length > 0 && <small>{items.length}건</small>}</h4>
    {error && <ErrorBanner message={error} onClose={() => setError('')} />}
    {items.length > 0 && <ol className="directive-list">{items.map((item) => <li key={item.id} className={item.deliveredAt ? 'delivered' : 'pending'}>
      <span className="kind">{item.kind === 'steer' ? '방향 수정' : '이어서'}</span>
      <p>{item.message}</p>
      <span className="state">{item.outcome ? item.outcome : item.deliveredAt ? '전달됨' : '전달 대기'}</span>
    </li>)}</ol>}
    {live && <div className="directive-compose">
      <select value={kind} onChange={(e) => setKind(e.target.value as 'steer'|'follow_up')}>
        <option value="steer">방향 수정</option>
        <option value="follow_up">이어서 할 일</option>
      </select>
      <input value={message} placeholder={kind === 'steer' ? '지금 하는 방식을 멈추고…' : '끝나면 이어서…'}
        onChange={(e) => setMessage(e.target.value)}
        onKeyDown={(e) => { if (e.key === 'Enter' && !busy) void send() }} />
      <button className="button primary" disabled={busy || !message.trim()} onClick={() => void send()}>
        {kind === 'steer' ? <Send size={14}/> : <CornerDownRight size={14}/>}전달
      </button>
    </div>}
    {live && <small className="directive-note">에이전트가 지금 하고 있는 일이 끝나는 지점에서 전달됩니다 — 문장 중간에 끼어들면 대화가 깨지기 때문입니다.</small>}
  </section>
}
