// How each supported runtime adapter is presented.
//
// The descriptions come from the platform — GET /api/v1/runtime-types — because
// what a runtime is good at, whether it has a terminal and where its files live
// are facts about the adapter the operator runs, not about this console. They
// were duplicated here and had already started to drift. The palette stays local:
// which colour a tile is is genuinely a console decision.
export type RuntimeType = 'openhands' | 'opencode' | 'hermes' | 'qwenpaw' | 'qwencode' | 'goose' | 'holmes' | 'browsercode' | 'jupyter' | 'langflow' | 'nodered' | 'n8n' | 'opencodereview' | 'orca' | 'pi' | 'custom'

export type RuntimeDescriptor = {
  type: string; code: string; label: string; summary: string
  strengths?: string[]; watchouts?: string[]
  workspace?: string; port?: number
  browserUi?: boolean; terminal?: boolean; toolLoop?: boolean; mcpConfigured?: boolean; proxiedUi?: boolean
  hostSessionOnly?: boolean; runners?: string[]; coarseToolKinds?: boolean
  /** What this deployment has seen this type do. Absent on an older control plane. */
  experience?: {
    verdict:'proven'|'attempted'|'failed'|'untried'; detail:string; attempts:number; started:number; approvedImages:number
    /** What would have to change here before this could run. Empty when nothing
     *  is missing — a list of reassurances beside every choice buries the one
     *  entry that needs attention. */
    missing?: { what:string; where:string }[]
    missingSummary?: string
  }
  bestFor?: string
}

/**
 * Enough to render before the platform answers, and to fall back to if it cannot.
 *
 * `coarseToolKinds` is carried here as well as in the platform's descriptors,
 * because the goal drawer warns from it: a failed descriptor fetch would
 * otherwise let somebody save an ACP goal, on a strict approval mode, that
 * refuses every tool call the runtime makes — with nothing on screen saying so.
 */
const SEED: Record<RuntimeType, RuntimeDescriptor> = {
  opencode: {type: 'opencode', code: 'OC', label: 'OpenCode', summary: '브라우저 기반 코딩 IDE와 터미널 워크스페이스'},
  hermes: {type: 'hermes', code: 'H', label: 'Hermes', summary: '장기 기억과 도구 실행을 갖춘 자율 에이전트'},
  qwenpaw: {type: 'qwenpaw', code: 'QP', label: 'Qwen Paw', summary: 'AgentScope 개인 에이전트 워크스테이션'},
  qwencode: {type: 'qwencode', code: 'QC', label: 'Qwen Code', summary: '터미널에서 사는 코딩 에이전트'},
  goose: {type: 'goose', code: 'GO', label: 'Goose', summary: '프로토콜로 대화하는 오픈소스 에이전트', coarseToolKinds: true},
  holmes: {type: 'holmes', code: 'HG', label: 'HolmesGPT', summary: '장애를 조사하는 SRE 에이전트'},
  browsercode: {type: 'browsercode', code: 'BC', label: 'BrowserCode', summary: '진짜 브라우저를 직접 모는 에이전트', coarseToolKinds: true},
  jupyter: {type: 'jupyter', code: 'JL', label: 'JupyterLab', summary: '노트북 작업대 + Qwen Code 에이전트'},
  langflow: {type: 'langflow', code: 'LF', label: 'Langflow', summary: '흐름을 그려서 만드는 시각적 에이전트 빌더'},
  nodered: {type: 'nodered', code: 'NR', label: 'Node-RED', summary: '노드를 이어 만드는 배선 자동화'},
  n8n: {type: 'n8n', code: 'N8', label: 'n8n', summary: '연동이 많은 업무 자동화'},
  opencodereview: {type: 'opencodereview', code: 'CR', label: 'Open Code Review', summary: '코드리뷰 전용 엔진'},
  orca: {type: 'orca', code: 'OR', label: 'Orca', summary: '여러 코딩 에이전트를 한 작업에 동시에 붙이는 실행 패브릭'},
  pi: {type: 'pi', code: 'PI', label: 'Pi', summary: '일하는 도중에 말을 걸 수 있는 코딩 에이전트'},
  openhands: {type: 'openhands', code: 'OH', label: 'OpenHands', summary: 'REST API로 대화를 열어 일을 시키는 에이전트 서버'},
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

/**
 * Every runtime type this deployment has, for the screens that offer a choice.
 *
 * A function rather than a constant, and read from what the platform answered
 * rather than from the seed: as a constant over the seed it was fixed at build
 * time, and it had gone stale — three runtimes the platform supports could not
 * be given an image, filtered for, or named as an evaluation condition, because
 * this console did not know they existed.
 */
export const runtimeTypeList = (): string[] => Object.keys(loaded)

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

/** What a runtime supports beyond the prose loop, said in one line. */
export const RUNNER_LABELS: Record<string, string> = {
  flow: '저장한 흐름을 그대로 실행',
  cli: '런타임의 에이전트가 직접 수행',
  acp: 'ACP로 에이전트와 대화하며 수행',
  investigate: '조사하고 근거와 함께 결론을 남김',
  review: '변경분을 리뷰하고 파일·줄 단위 지적을 남김',
  orca: '여러 에이전트를 각자 작업 사본에서 동시에 돌림',
  rpc: '일하는 도중에 말을 걸 수 있는 프로토콜 실행',
}

/** What each way of running has done on this deployment.
 *
 *  The same question as the runtime types, about the other choice a person makes.
 *  Loaded once with the descriptors, because the goal form already has those and
 *  a second fetch to answer "has this ever worked here" would be a request per
 *  agent somebody opens. */
export type RunnerExperience = {
  runner:string; verdict:'proven'|'failing'|'untried'; detail:string; runs:number; completed:number
  /** What would have to change here before this way of running could be used.
   *  Empty when nothing is missing. */
  missing?: { what:string; where:string }[]
  missingSummary?: string
}
let runnerExperience: Record<string, RunnerExperience> = {}
export function setRunnerExperience(value: Record<string, RunnerExperience> | undefined) {
  runnerExperience = value ?? {}
}
export const runnerExperienceOf = (runner: string) => runnerExperience[runner]

export const RUNNER_VERDICT_LABELS: Record<string, string> = {
  proven: '이 배포에서 확인됨',
  failing: '완료된 적 없음',
  untried: '안 해 봄',
}

/** What to call a type's history on this deployment.
 *
 *  Deliberately about the past. "확인됨" means one ran here; it does not promise
 *  the next one will, because the cluster may have changed. "안 해 봄" is not a
 *  warning — most deployments will never use most of these — it is the absence of
 *  evidence, said plainly rather than left to look like approval. */
export const EXPERIENCE_LABELS: Record<string, string> = {
  proven: '이 배포에서 확인됨',
  attempted: '만들어졌지만 실행된 적 없음',
  failed: '마지막 시도 실패',
  untried: '안 해 봄',
}

export function runnerSummary(runners?: string[]) {
  const known = (runners ?? []).map((item) => RUNNER_LABELS[item]).filter(Boolean)
  return known.length ? known.join(' · ') : '추론 루프 + 사람에게 인계'
}

/** Whether this runtime can be handed a task that way. */
export const supportsRunner = (type: string, runner: string) => (descriptor(type).runners ?? []).includes(runner)

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
