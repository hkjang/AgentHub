// What each line of a run's timeline is called.
//
// The timeline used to print the event's identifier — `runtime.acquiring`,
// `acp.permission.asked` — which is the name the code calls it, not the name a
// person reading a finished run calls it. The identifiers stay in the payload
// and in the admin event list; this is what the screen says out loud.
//
// A type with no entry falls back to its identifier, which is honest but is not
// the point: a guard in the execution package fails the build when something
// this platform emits is missing from here.
export const RUN_EVENT_LABELS: Record<string, string> = {
  // 작업의 시작과 끝
  'task.started': '실행 시작',
  'task.completed': '실행 완료',
  'task.failed': '실행 실패',
  'task.cancelled': '실행 취소됨',
  'task.dead_letter': '재시도 소진',
  'task.resumed': '이어서 실행',
  'task.handoff': '사람에게 인계',
  'task.delegated': '다른 에이전트에 위임',

  // Runtime 확보
  'runtime.acquiring': 'Runtime 확보 중',
  'runtime.ready': 'Runtime 준비됨',
  'runtime.reused': 'Runtime 재사용',
  'runtime.released': 'Runtime 반납',
  'runtime.retained': 'Runtime 유지',
  'runtime.kept_warm': 'Runtime 예열 유지',
  'runtime.refused': 'Runtime 거부됨',
  'runtime.unavailable': 'Runtime 없음',

  // 추론 루프
  'step.completed': '단계 완료',
  'step.failed': '단계 실패',
  'plan.created': '계획 수립',
  'plan.failed': '계획 실패',
  'memory.written': '기억 저장',
  'completion.evaluated': '완료 판정',
  'approval.requested': '승인 요청',
  'artifact.created': '산출물 생성',

  // CLI 실행
  'cli.started': 'CLI 실행 시작',
  'cli.completed': 'CLI 실행 완료',
  'cli.failed': 'CLI 실행 실패',

  // ACP 실행
  'acp.started': 'ACP 실행 시작',
  'acp.completed': 'ACP 실행 완료',
  'acp.failed': 'ACP 실행 실패',
  'acp.permission': 'ACP 권한 처리',
  'acp.permission.asked': 'ACP 권한 요청',
  'acp.permission.unavailable': 'ACP 권한 응답 불가',
  'acp.pictures.skipped': 'ACP 이미지 건너뜀',

  // 에이전트 서버 실행
  'agentserver.started': '에이전트 서버 실행 시작',
  'agentserver.completed': '에이전트 서버 실행 완료',
  'agentserver.failed': '에이전트 서버 실행 실패',
  'agentserver.activity': '에이전트 서버 활동',
  'agentserver.permission.asked': '에이전트 서버 권한 요청',
  'agentserver.permission.answered': '에이전트 서버 권한 응답',

  // 프로토콜 실행
  'rpc.started': '프로토콜 실행 시작',
  'rpc.completed': '프로토콜 실행 완료',
  'rpc.failed': '프로토콜 실행 실패',
  'rpc.directive': '프로토콜 지시',
  'rpc.directive_refused': '프로토콜 지시 거부',
  'rpc.endpoint_mismatch': '프로토콜 주소 불일치',

  // 코드 리뷰
  'review.started': '리뷰 시작',
  'review.completed': '리뷰 완료',
  'review.failed': '리뷰 실패',
  'review.posted': '리뷰 댓글 남김',
  'review.post.failed': '리뷰 댓글 실패',
  'review.withheld': '리뷰 댓글 보류(내용 검사)',

  // 조사 실행
  'investigate.started': '조사 시작',
  'investigate.completed': '조사 완료',
  'investigate.failed': '조사 실패',

  // 실행 패브릭
  'orca.started': '패브릭 실행 시작',
  'orca.completed': '패브릭 실행 완료',
  'orca.failed': '패브릭 실행 실패',

  // 흐름 실행
  'flow.started': '흐름 실행 시작',
  'flow.completed': '흐름 실행 완료',
  'flow.failed': '흐름 실행 실패',

  // 외부 앱 실행
  'external.started': '외부 앱 실행 시작',
  'external.completed': '외부 앱 실행 완료',
  'external.failed': '외부 앱 실행 실패',
}

// runEventLabel is what the screen shows. The identifier is the fallback, not
// the default: an unlabelled type is a gap, and it should read as one rather
// than as a design.
export function runEventLabel(type: string): string {
  return RUN_EVENT_LABELS[type] ?? type
}
