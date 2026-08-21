import type { Metering } from './types'

/**
 * Who counted a run's tokens, in words. A number with no source is not evidence:
 * zero tokens can mean the platform made no model calls, or that the agent did
 * the work in its own process and never said what it spent. Those need different
 * answers, so the console never shows the number without the source.
 */
export const METERING: Record<Exclude<Metering, ''>, { label: string; tone: string; hint: string }> = {
  gateway: { label: '플랫폼 집계', tone: 'ok', hint: '모델 호출이 모두 플랫폼 게이트웨이를 지나므로 토큰이 빠짐없이 집계됩니다.' },
  agent: { label: '에이전트 보고', tone: 'ok', hint: '에이전트가 스스로 보고한 사용량을 그대로 기록했습니다.' },
  context_only: { label: '컨텍스트만', tone: 'warn', hint: '에이전트가 컨텍스트 사용률만 알려주고 실제 소비량은 알려주지 않았습니다. 여기 보이는 토큰은 플랫폼이 직접 쓴 몫뿐입니다.' },
  unmetered: { label: '집계 안 됨', tone: 'warn', hint: '에이전트가 사용량을 알려주지 않았습니다. 이 실행의 실제 소비량은 어떤 합계에도 들어 있지 않습니다.' }
}

export function metering(value?: Metering) {
  return value ? METERING[value] : undefined
}
