import { useCallback, useEffect, useState } from 'react'
import { AlertTriangle, Check, FileCode2, X } from 'lucide-react'
import { api } from '../api'
import { ErrorBanner, Loading } from './UI'
import type { ReviewCoverage, ReviewFinding } from '../types'

const SEVERITY_LABEL: Record<string, string> = { critical: '심각', high: '높음', medium: '보통', low: '낮음' }
const CATEGORY_LABEL: Record<string, string> = {
  bug: '버그', security: '보안', performance: '성능', maintainability: '유지보수',
  test: '테스트', style: '스타일', documentation: '문서', other: '기타',
}
const ORDER = ['critical', 'high', 'medium', 'low']

/** What a code review found.
 *
 *  Deliberately not a summary. A review's value is that each observation points
 *  at a file and a line and carries a severity somebody has to act on, and the
 *  moment that becomes a paragraph the reader has to go and find the code
 *  themselves. What is here is the list, worst first, with the two decisions a
 *  person actually makes: this one is real, or it is not.
 *
 *  The coverage is shown even when nothing was found, because an empty list
 *  means two opposite things — a clean review, and a review that read nothing —
 *  and the file counts are what tell them apart. */
export function ReviewFindings({ runId }: { runId: string }) {
  const [data, setData] = useState<{ items: ReviewFinding[]; coverage: ReviewCoverage; severityCounts: Record<string, number>; open: number }>()
  const [error, setError] = useState('')
  const [severity, setSeverity] = useState('')
  const [busy, setBusy] = useState('')

  const load = useCallback(async () => {
    try { setData(await api.get(`/api/v1/runs/${runId}/review`)) }
    catch (e) { setError(e instanceof Error ? e.message : '리뷰 결과를 불러오지 못했습니다.') }
  }, [runId])
  useEffect(() => { void load() }, [load])

  const decide = async (id: string, decision: 'accepted' | 'dismissed' | 'fixed') => {
    setBusy(id); setError('')
    try { await api.post(`/api/v1/review-findings/${id}/decision`, { decision }); await load() }
    catch (e) { setError(e instanceof Error ? e.message : '처리하지 못했습니다.') }
    finally { setBusy('') }
  }

  if (error && !data) return <ErrorBanner message={error} />
  if (!data) return <Loading />
  const { items, coverage, severityCounts } = data
  const shown = severity ? items.filter((item) => item.severity === severity) : items

  return <section className="detail-section review-findings">
    <h4>코드 리뷰 <small>{coverage?.filesReviewed ?? 0}개 파일 · 지적 {items.length}건</small></h4>
    {error && <ErrorBanner message={error} onClose={() => setError('')} />}

    <div className="review-coverage">
      {coverage?.baseRef && <span><FileCode2 size={13} />{coverage.baseRef} → {coverage.headRef}</span>}
      <span>대상 {coverage?.filesSelected ?? 0}개 중 {coverage?.filesReviewed ?? 0}개 리뷰</span>
      {coverage?.filesFailed > 0 && <span className="warn"><AlertTriangle size={13} />{coverage.filesFailed}개는 리뷰하지 못했습니다</span>}
      {coverage?.engineVersion && <span className="muted">{coverage.engineVersion}</span>}
    </div>

    <div className="review-filters">
      <button className={severity === '' ? 'active' : ''} onClick={() => setSeverity('')}>전체 {items.length}</button>
      {ORDER.filter((value) => severityCounts[value] > 0).map((value) =>
        <button key={value} className={`${value} ${severity === value ? 'active' : ''}`} onClick={() => setSeverity(value)}>
          {SEVERITY_LABEL[value]} {severityCounts[value]}
        </button>)}
    </div>

    {shown.length === 0
      ? <p className="review-empty">{items.length === 0
        ? (coverage?.filesReviewed ? '리뷰한 파일에서 지적할 점을 찾지 못했습니다.' : '리뷰한 파일이 없습니다 — 비교 대상에 변경분이 있는지 확인해 주세요.')
        : '이 심각도의 지적은 없습니다.'}</p>
      : <ol className="review-list">{shown.map((finding) => <li key={finding.id} className={`${finding.severity} ${finding.status}`}>
        <header>
          <span className={`severity ${finding.severity}`}>{SEVERITY_LABEL[finding.severity] ?? finding.severity}</span>
          <span className="category">{CATEGORY_LABEL[finding.category] ?? finding.category}</span>
          <code>{finding.filePath}:{finding.startLine}{finding.endLine > finding.startLine ? `-${finding.endLine}` : ''}</code>
          {finding.status !== 'open' && <span className={`decided ${finding.status}`}>
            {finding.status === 'accepted' ? '인정' : finding.status === 'dismissed' ? '오탐' : '수정됨'}
          </span>}
        </header>
        <p>{finding.message}</p>
        {finding.existingCode && <pre className="custom-scroll">{finding.existingCode}</pre>}
        {finding.suggestion && <pre className="custom-scroll suggestion">{finding.suggestion}</pre>}
        {finding.status === 'open' && <div className="review-actions">
          <button className="button subtle" disabled={busy === finding.id} onClick={() => void decide(finding.id, 'dismissed')}><X size={14} />오탐</button>
          <button className="button ghost" disabled={busy === finding.id} onClick={() => void decide(finding.id, 'accepted')}><Check size={14} />인정</button>
        </div>}
      </li>)}</ol>}
  </section>
}
