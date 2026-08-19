// How each supported runtime adapter is presented.
//
// The descriptions come from the platform — GET /api/v1/runtime-types — because
// what a runtime is good at, whether it has a terminal and where its files live
// are facts about the adapter the operator runs, not about this console. They
// were duplicated here and had already started to drift. The palette stays local:
// which colour a tile is is genuinely a console decision.
export type RuntimeType = 'opencode' | 'hermes' | 'qwenpaw' | 'custom'

export type RuntimeDescriptor = {
  type: string; code: string; label: string; summary: string
  strengths?: string[]; watchouts?: string[]
  workspace?: string; port?: number
  browserUi?: boolean; terminal?: boolean; toolLoop?: boolean; mcpConfigured?: boolean; proxiedUi?: boolean
  bestFor?: string
}

/** Enough to render before the platform answers, and to fall back to if it cannot. */
const SEED: Record<RuntimeType, RuntimeDescriptor> = {
  opencode: {type: 'opencode', code: 'OC', label: 'OpenCode', summary: '브라우저 기반 코딩 IDE와 터미널 워크스페이스'},
  hermes: {type: 'hermes', code: 'H', label: 'Hermes', summary: '장기 기억과 도구 실행을 갖춘 자율 에이전트'},
  qwenpaw: {type: 'qwenpaw', code: 'QP', label: 'Qwen Paw', summary: 'AgentScope 개인 에이전트 워크스테이션'},
  custom: {type: 'custom', code: 'A', label: 'Custom', summary: '직접 정의한 컨테이너 실행 명령'},
}

const FALLBACK: RuntimeDescriptor = {type: '', code: 'A', label: 'Unknown', summary: '알 수 없는 Runtime 유형'}

let loaded: Record<string, RuntimeDescriptor> = {...SEED}

/**
 * Replaces the seeded descriptions with the platform's own. Called once when the
 * shell loads; every helper below reads whatever is current, so a screen rendered
 * before the answer arrives simply shows the seed.
 */
export function setRuntimeDescriptors(items: RuntimeDescriptor[]) {
  if (!items?.length) return
  const next: Record<string, RuntimeDescriptor> = {}
  for (const item of items) next[item.type] = item
  loaded = next
}

export const RUNTIME_TYPES = Object.keys(SEED) as RuntimeType[]

/** Every runtime the platform reported, in its order. */
export const runtimeDescriptors = () => Object.values(loaded)

export function descriptor(type: string): RuntimeDescriptor {
  return loaded[type] ?? SEED[type as RuntimeType] ?? FALLBACK
}

/** Short badge text rendered inside the runtime logo tile. */
export const runtimeCode = (type: string) => descriptor(type).code

/** Human-facing runtime name; falls back to the raw value so unknown types stay debuggable. */
export const runtimeLabel = (type: string) => (loaded[type]?.label ?? SEED[type as RuntimeType]?.label ?? type ?? FALLBACK.label)

export const runtimeSummary = (type: string) => descriptor(type).summary

/**
 * Class list for the runtime logo tile. Unknown types get the neutral `custom`
 * palette instead of an unstyled transparent box.
 */
export function runtimeLogoClass(type: string, size?: 'large' | 'xlarge') {
  const palette = type in SEED ? type : 'custom'
  return `runtime-logo ${palette}${size ? ` ${size}` : ''}`
}

/**
 * Short relative time for list rows. Absolute timestamps are hard to scan when
 * the question is "did this just change?"; the exact value stays in a tooltip.
 */
export function relativeTime(value: string) {
  const then = new Date(value).getTime()
  if (Number.isNaN(then)) return '—'
  const seconds = Math.round((Date.now() - then) / 1000)
  if (seconds < 0) return '방금'
  if (seconds < 60) return '방금'
  const minutes = Math.floor(seconds / 60)
  if (minutes < 60) return `${minutes}분 전`
  const hours = Math.floor(minutes / 60)
  if (hours < 24) return `${hours}시간 전`
  const days = Math.floor(hours / 24)
  if (days < 7) return `${days}일 전`
  return new Date(value).toLocaleDateString('ko-KR', {year: '2-digit', month: '2-digit', day: '2-digit'})
}
