// A stand-in model gateway for photographing the guides.
//
// The run history, the task queue and the runtime list are only worth showing
// with something in them, and the only honest way to fill them is to let the
// real worker execute the seeded tasks. The worker talks to whatever
// OpenAI-compatible endpoint the model record points at, so this answers
// POST /v1/chat/completions with canned Korean replies keyed on the task title
// guide-shots.mjs seeds. Nothing else about the run is invented: the worker
// creates the run, records the steps, files the artifact and judges completion
// exactly as it would against a real gateway.
//
// The replies are a script, not a model, and they say so in the guide.
//
//   AGENTHUB_GUIDE_MODEL_PORT=18089 node scripts/guide-model-stub.mjs
import { createServer } from 'node:http'

const port = Number(process.env.AGENTHUB_GUIDE_MODEL_PORT ?? 18089)

/** Two turns per task: a first step that reads the request, and a second that
 *  finishes it. The worker asks again with the transcript attached, so the second
 *  turn is recognised by the "# 이전 단계" heading it adds. */
const scripts = [
  {
    match: /결제 테스트/,
    first: '실패한 테스트 3건을 확인했습니다: `test_refund_partial`, `test_refund_currency`, `test_settlement_rounding`. 셋 모두 반올림 규칙이 바뀐 뒤 기대값이 갱신되지 않은 경우입니다. 고칠 파일과 순서를 정리한 뒤 편집을 시작합니다.',
    second: [
      '테스트 기대값을 고치려면 저장소 파일을 직접 편집하고 `pytest payments/` 를 실행해야 합니다. 이 실행 루프에서는 파일 편집과 명령 실행을 할 수 없어 런타임에서 이어서 진행하도록 넘깁니다.',
      '<<<HANDOFF payments 테스트 3건의 기대값 갱신',
      '- tests/test_refund.py: `test_refund_partial`, `test_refund_currency` 의 기대 금액을 반올림 규칙(HALF_EVEN)에 맞게 갱신',
      '- tests/test_settlement.py: `test_settlement_rounding` 의 기대값 1원 차이 수정',
      '- 수정 후 `pytest payments/ -q` 로 3건 통과 확인',
      '>>>',
    ].join('\n'),
  },
  {
    match: /보관 기간/,
    first: '사내 위키에서 데이터 보관 관련 규정 문서 세 건을 찾았습니다: 「개인정보 보관·파기 지침」, 「로그 보존 정책」, 「백업 매체 관리 규정」. 각 문서의 보관 기간 조항을 발췌해 표로 정리합니다.',
    second: [
      '세 문서의 보관 기간 조항을 근거 문서 이름과 함께 정리했습니다.',
      '<<<ARTIFACT 데이터-보관기간-규정-정리.md',
      '# 데이터 보관 기간 규정 정리',
      '',
      '| 데이터 종류 | 보관 기간 | 근거 문서 |',
      '| --- | --- | --- |',
      '| 회원 개인정보 | 탈퇴 후 30일 | 개인정보 보관·파기 지침 §4 |',
      '| 결제 기록 | 5년 | 개인정보 보관·파기 지침 §6 |',
      '| 접속 로그 | 1년 | 로그 보존 정책 §2 |',
      '| 감사 로그 | 3년 | 로그 보존 정책 §3 |',
      '| 백업 매체 | 90일 후 파기 | 백업 매체 관리 규정 §5 |',
      '',
      '기간이 지난 데이터의 파기 절차는 각 문서의 마지막 절에 있습니다.',
      '>>>',
      'TASK_COMPLETE',
    ].join('\n'),
  },
  {
    match: /처리량/,
    first: '9월 1일부터 10일까지의 일자별 처리량 데이터를 받았습니다. 주말(6일·7일)은 처리량이 낮고, 3일에 배치 재처리로 한 번 튀어 있습니다. 표와 요약을 만듭니다.',
    second: [
      '9월 일자별 처리량 표를 만들었습니다.',
      '<<<ARTIFACT 9월-처리량.md',
      '# 9월 일자별 처리량',
      '',
      '| 일자 | 처리 건수 | 실패 건수 | 비고 |',
      '| --- | ---: | ---: | --- |',
      '| 9/1 (월) | 12,480 | 31 | |',
      '| 9/2 (화) | 12,915 | 27 | |',
      '| 9/3 (수) | 18,204 | 44 | 배치 재처리 |',
      '| 9/4 (목) | 12,662 | 19 | |',
      '| 9/5 (금) | 11,973 | 22 | |',
      '| 9/6 (토) | 4,118 | 6 | |',
      '| 9/7 (일) | 3,907 | 4 | |',
      '| 9/8 (월) | 12,733 | 25 | |',
      '| 9/9 (화) | 13,046 | 30 | |',
      '| 9/10 (수) | 12,588 | 21 | |',
      '',
      '평일 평균 12,950건, 주말 평균 4,012건. 실패율은 0.2% 안팎으로 유지됐습니다.',
      '>>>',
      'TASK_COMPLETE',
    ].join('\n'),
  },
]

const fallback = {
  first: '요청을 확인했습니다. 필요한 자료를 모아 다음 단계에서 정리합니다.',
  second: '요청한 내용을 정리해 마쳤습니다.\nTASK_COMPLETE',
}

/** reply picks the canned answer for one request. */
export function reply(messages) {
  const user = [...(messages ?? [])].reverse().find((m) => m.role === 'user')?.content ?? ''
  const script = scripts.find((s) => s.match.test(user)) ?? fallback
  const followUp = user.includes('# 이전 단계')
  return followUp ? script.second : script.first
}

const server = createServer((request, response) => {
  if (request.method !== 'POST' || !request.url.endsWith('/chat/completions')) {
    response.writeHead(404, { 'Content-Type': 'application/json' })
    response.end(JSON.stringify({ error: { message: 'guide model stub: only POST …/chat/completions is served' } }))
    return
  }
  let raw = ''
  request.on('data', (chunk) => { raw += chunk })
  request.on('end', () => {
    let body = {}
    try { body = JSON.parse(raw || '{}') } catch { body = {} }
    const content = reply(body.messages)
    // Token counts are estimated from length so the usage column has something
    // proportional in it; a gateway would report the real figures here.
    const promptTokens = Math.ceil(raw.length / 4)
    const completionTokens = Math.ceil(content.length / 3)
    response.writeHead(200, { 'Content-Type': 'application/json' })
    response.end(JSON.stringify({
      id: `guide-${Date.now()}`, object: 'chat.completion', model: body.model ?? 'guide-stub',
      choices: [{ index: 0, message: { role: 'assistant', content }, finish_reason: 'stop' }],
      usage: { prompt_tokens: promptTokens, completion_tokens: completionTokens, total_tokens: promptTokens + completionTokens },
    }))
  })
})

server.listen(port, '127.0.0.1', () => {
  console.log(`guide model stub listening on http://127.0.0.1:${port}/v1`)
})
