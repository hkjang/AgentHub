import { useCallback, useEffect, useState } from 'react'
import { FileSearch, Filter } from 'lucide-react'
import { api } from '../api'
import { Empty, ErrorBanner, Loading, PageHeader } from '../components/UI'
import { RunDrawer } from './Tasks'
import type { ReviewFinding } from '../types'

const SEVERITY_LABEL: Record<string, string> = { critical: '심각', high: '높음', medium: '보통', low: '낮음' }
const CATEGORY_LABEL: Record<string, string> = {
  bug: '버그', security: '보안', performance: '성능', maintainability: '유지보수',
  test: '테스트', style: '스타일', documentation: '문서', other: '기타',
}
const ORDER = ['critical', 'high', 'medium', 'low']
type Page = { items: ReviewFinding[]; total: number; limit: number; offset: number; openBySeverity: Record<string, number>; categories: string[] }

/** What every review has found and nobody has dealt with.
 *
 *  A finding used to be reachable only by knowing which run produced it, which
 *  meant somebody who ran three reviews yesterday had no way to ask the one
 *  question the list is for. It opens on what is still open, because a page that
 *  starts with a year of dismissed findings is a page nobody reads twice.
 *
 *  The counts are of everything the filter selects, not of the page — counting
 *  the rows that happened to be fetched is the mistake the notification bell was
 *  fixed for, and the review queue after it. */
export function CodeReview() {
  const [page, setPage] = useState<Page>()
  const [error, setError] = useState('')
  const [severity, setSeverity] = useState('')
  const [category, setCategory] = useState('')
  const [status, setStatus] = useState('open')
  const [openRun, setOpenRun] = useState<string | null>(null)
  const [busy, setBusy] = useState('')

  const load = useCallback(async () => {
    const query = new URLSearchParams({ status })
    if (severity) query.set('severity', severity)
    if (category) query.set('category', category)
    try { setPage(await api.get(`/api/v1/review-findings?${query}`)) }
    catch (e) { setError(e instanceof Error ? e.message : '리뷰 지적을 불러오지 못했습니다.') }
  }, [severity, category, status])
  useEffect(() => { void load() }, [load])

  const decide = async (id: string, decision: 'accepted' | 'dismissed' | 'fixed') => {
    setBusy(id); setError('')
    try { await api.post(`/api/v1/review-findings/${id}/decision`, { decision }); await load() }
    catch (e) { setError(e instanceof Error ? e.message : '처리하지 못했습니다.') }
    finally { setBusy('') }
  }

  if (!page) return <Loading />
  const counts = page.openBySeverity ?? {}
  return <div className="page">
    <PageHeader eyebrow="코드 인텔리전스" title="코드 리뷰" description="리뷰가 찾은 지적을 한자리에서 봅니다. 각 지적은 파일과 줄을 가리키고, 인정하거나 오탐으로 처리하면 다음 리뷰의 소음을 잴 수 있습니다." />
    {error && <ErrorBanner message={error} onClose={() => setError('')} />}

    <div className="review-filters">
      <button className={severity === '' ? 'active' : ''} onClick={() => setSeverity('')}><Filter size={13} />전체 {page.total}</button>
      {ORDER.filter((value) => counts[value] > 0).map((value) =>
        <button key={value} className={`${value} ${severity === value ? 'active' : ''}`} onClick={() => setSeverity(value)}>
          {SEVERITY_LABEL[value]} {counts[value]}
        </button>)}
      <select value={category} onChange={(e) => setCategory(e.target.value)}>
        <option value="">모든 분류</option>
        {(page.categories ?? []).map((value) => <option key={value} value={value}>{CATEGORY_LABEL[value] ?? value}</option>)}
      </select>
      <select value={status} onChange={(e) => setStatus(e.target.value)}>
        <option value="open">처리 전</option>
        <option value="accepted">인정</option>
        <option value="dismissed">오탐</option>
        <option value="fixed">수정됨</option>
        <option value="all">전체</option>
      </select>
    </div>

    {page.items.length === 0
      ? <Empty icon={<FileSearch />} title={status === 'open' ? '처리할 리뷰 지적이 없습니다' : '해당하는 지적이 없습니다'}
          description="코드 리뷰 실행 방식을 쓰는 에이전트가 변경분을 리뷰하면 여기에 쌓입니다." />
      : <ol className="review-list">{page.items.map((finding) => <li key={finding.id} className={`${finding.severity} ${finding.status}`}>
        <header>
          <span className={`severity ${finding.severity}`}>{SEVERITY_LABEL[finding.severity] ?? finding.severity}</span>
          <span className="category">{CATEGORY_LABEL[finding.category] ?? finding.category}</span>
          <code>{finding.filePath}:{finding.startLine}{finding.endLine > finding.startLine ? `-${finding.endLine}` : ''}</code>
          <button className="text-link" onClick={() => setOpenRun(finding.runId)}>실행 기록</button>
          {finding.status !== 'open' && <span className={`decided ${finding.status}`}>
            {finding.status === 'accepted' ? '인정' : finding.status === 'dismissed' ? '오탐' : '수정됨'}
          </span>}
        </header>
        <p>{finding.message}</p>
        {finding.existingCode && <pre className="custom-scroll">{finding.existingCode}</pre>}
        {finding.suggestion && <pre className="custom-scroll suggestion">{finding.suggestion}</pre>}
        {finding.status === 'open' && <div className="review-actions">
          <button className="button subtle" disabled={busy === finding.id} onClick={() => void decide(finding.id, 'dismissed')}>오탐</button>
          <button className="button ghost" disabled={busy === finding.id} onClick={() => void decide(finding.id, 'fixed')}>수정됨</button>
          <button className="button primary" disabled={busy === finding.id} onClick={() => void decide(finding.id, 'accepted')}>인정</button>
        </div>}
      </li>)}</ol>}

    {page.total > page.items.length && <p className="review-empty">{page.total}건 중 {page.items.length}건을 보여 줍니다. 조건을 좁혀 주세요.</p>}
    {openRun && <RunDrawer runId={openRun} close={() => setOpenRun(null)} />}
  </div>
}
