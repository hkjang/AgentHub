import { useCallback, useSyncExternalStore } from 'react'

/**
 * Two ways of reading the same console.
 *
 * The platform is easy to describe and hard to picture: an agent is a container
 * somebody owns, a task is work handed to it, an approval is a person deciding
 * whether it may act. People who run workshops already have words for all of
 * that, and they are better words — 일감, 공구, 작업 일지 — because they carry the
 * relationships with them. A "task queue" is a list; an 일감 대기열 is work waiting
 * for a bench.
 *
 * So the vocabulary is a preference rather than a decision the product makes for
 * everybody. Standard mode says what things are called in the documentation and
 * the API; workshop mode says what they are.
 *
 * Two rules keep it from becoming decoration:
 *
 * It only renames what it makes clearer. Most of the console reads the same in
 * both modes, and a term with no better workshop word simply has no entry here.
 *
 * It never renames an outcome. A run that failed says 실패 in both modes, an
 * approval that was refused says 거절, a quota that is exhausted says so. Dressing
 * up a status is how a metaphor starts lying, and this one is meant to help
 * somebody understand the system, not feel better about it.
 */
export type ViewMode = 'standard' | 'workshop'

export const VIEW_MODES: { id: ViewMode; label: string; hint: string }[] = [
  { id: 'standard', label: '일반 모드', hint: '문서와 API에서 쓰는 이름 그대로' },
  { id: 'workshop', label: '공방 모드', hint: '작업대·공구·일감으로 바꿔 읽기' },
]

/**
 * The words. A key with no `workshop` entry reads the same in both modes, which
 * is most of the console.
 */
const TERMS = {
  agents: { standard: '내 에이전트', workshop: '내 작업대' },
  agentsHint: {
    standard: '에이전트 정의와 런타임 상태',
    workshop: '작업대마다 도구와 서랍이 딸려 있습니다',
  },
  agentSingular: { standard: '에이전트', workshop: '작업대' },
  agentsEyebrow: { standard: '에이전트 정의', workshop: '작업대 편성' },
  builder: { standard: '에이전트 빌더', workshop: '작업대 꾸리기' },
  catalog: { standard: '에이전트 카탈로그', workshop: '작업대 종류' },
  tasks: { standard: '작업 대기열', workshop: '일감 대기열' },
  taskSingular: { standard: '작업', workshop: '일감' },
  runs: { standard: '실행 기록', workshop: '작업 일지' },
  // The record of one run. This is where the metaphor earns its keep: a list of
  // steps is a list, and a 작업 일지 with 손댄 내역 is a record somebody kept.
  runSteps: { standard: '단계별 기록', workshop: '손댄 내역' },
  // The home screen. Its metric labels and the three-step opener are where a
  // person meets the vocabulary first, so they carry it too.
  homeHint: {
    standard: '에이전트 런타임과 작업공간 상태를 한눈에 확인하세요.',
    workshop: '어느 작업대가 돌아가고 있고, 서랍에 무엇이 있는지 한눈에 봅니다.',
  },
  recentAgents: { standard: '최근 에이전트', workshop: '최근 작업대' },
  recentAgentsHint: { standard: '현재 런타임 상태와 마지막 활동', workshop: '지금 가동 상태와 마지막 손댄 시각' },
  emptyAgents: {
    standard: '아직 생성한 에이전트가 없습니다. 카탈로그에서 시작해 보세요.',
    workshop: '아직 꾸린 작업대가 없습니다. 작업대 종류에서 하나 골라 시작해 보세요.',
  },
  metricAgents: { standard: '내 에이전트', workshop: '내 작업대' },
  metricAgentsNote: { standard: '정의된 에이전트', workshop: '꾸려 둔 작업대' },
  metricRunning: { standard: '실행 중', workshop: '가동 중' },
  metricWorkspaces: { standard: '작업공간', workshop: '자료 서랍' },
  quickStart: { standard: '빠른 시작', workshop: '작업대 차리기' },
  quickStartHint: { standard: '첫 런타임을 준비하는 3단계', workshop: '첫 작업대를 차리는 3단계' },
  quickStep1: { standard: '템플릿 선택', workshop: '작업대 종류 고르기' },
  quickStep2: { standard: '작업공간 연결', workshop: '서랍 연결' },
  quickStep3: { standard: '런타임 시작', workshop: '전원 넣기' },
  openCatalog: { standard: '에이전트 카탈로그 열기', workshop: '작업대 종류 보기' },
  runtime: { standard: '런타임', workshop: '가동 중인 작업대' },
  // The page titles are separate entries from the menu ones on purpose: the menu
  // says 런타임 and the page says 내 런타임, and a single key for both quietly
  // rewrote the standard vocabulary — which is the one thing this must not do.
  runtimeTitle: { standard: '내 런타임', workshop: '내 가동 작업대' },
  sessionsTitle: { standard: '런타임 세션', workshop: '열려 있는 작업대' },
  newSession: { standard: '새 세션', workshop: '작업대 열기' },
  runtimeType: { standard: '런타임', workshop: '도구' },
  sessions: { standard: '세션', workshop: '작업대 열기' },
  workspaces: { standard: '작업공간', workshop: '자료 서랍' },
  snapshots: { standard: '스냅샷', workshop: '서랍 사본' },
  mcpCatalog: { standard: 'MCP 카탈로그', workshop: '공구 카탈로그' },
  mcpBundles: { standard: 'MCP 번들', workshop: '공구 세트' },
  mcpBundlesAdmin: { standard: 'MCP 번들 관리', workshop: '공구 세트 관리' },
  mcpServers: { standard: 'MCP 서버', workshop: '공구 공급처' },
  reviews: { standard: '검토 · 승인', workshop: '서명 대기' },
  runtimeImages: { standard: '런타임 이미지', workshop: '작업대 도구 세트' },
  runtimeProfiles: { standard: '런타임 프로파일', workshop: '작업대 크기' },
} as const

export type TermKey = keyof typeof TERMS

const STORAGE_PREFIX = 'agenthub.viewmode.'
let scope = ''
let current: ViewMode = 'standard'
const listeners = new Set<() => void>()

function read(userId: string): ViewMode {
  try {
    return window.localStorage.getItem(STORAGE_PREFIX + userId) === 'workshop' ? 'workshop' : 'standard'
  } catch { return 'standard' }
}

/**
 * Bind the preference to whoever is signed in. It is stored per user rather than
 * per browser: a shared machine must not hand one person's reading of the console
 * to the next person who signs in.
 */
export function setViewModeScope(userId: string) {
  if (scope === userId) return
  scope = userId
  const next = userId ? read(userId) : 'standard'
  if (next !== current) { current = next; listeners.forEach((listener) => listener()) }
  else listeners.forEach((listener) => listener())
}

export function setViewMode(mode: ViewMode) {
  if (mode === current) return
  current = mode
  try { if (scope) window.localStorage.setItem(STORAGE_PREFIX + scope, mode) } catch { /* storage may be blocked */ }
  listeners.forEach((listener) => listener())
}

function subscribe(listener: () => void) {
  listeners.add(listener)
  return () => { listeners.delete(listener) }
}

export function useViewMode(): ViewMode {
  return useSyncExternalStore(subscribe, () => current, () => 'standard')
}

/** The word for something in the mode the reader chose. */
export function useTerms() {
  const mode = useViewMode()
  return useCallback((key: TermKey) => {
    const entry = TERMS[key]
    return mode === 'workshop' && 'workshop' in entry ? entry.workshop : entry.standard
  }, [mode])
}

/** Read one term outside a component, for the few places that are not one. */
export function term(key: TermKey, mode: ViewMode = current) {
  const entry = TERMS[key]
  return mode === 'workshop' && 'workshop' in entry ? entry.workshop : entry.standard
}
