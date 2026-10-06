// 추적 설정의 상한은 서버가 거절로 알려 주고, 콘솔은 그 숫자를 미리 보여 준다. 두 곳에
// 같은 숫자가 적히는 중복은 피할 수 없다 — Go 상수를 프런트로 전달하는 계약이 이 저장소에
// 없다. 그래서 이 회귀는 그 중복이 어긋나는 쪽을 막는다: 상한을 Go 에서 올리거나 내리면
// 여기서 실패하므로, 콘솔이 더는 사실이 아닌 숫자를 안내하는 상태로 남지 않는다.
//
// 두 번째 묶음은 허용 출처 입력의 사용량 계산이 서버의 splitHosts 와 같은 방식으로 세는지
// 본다. 콘솔의 숫자가 서버보다 느슨하면 안내가 거짓이 되고, 더 엄하면 관리자가 쓸 수 있는
// 값을 못 쓴다고 믿게 된다.
import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { dirname, join } from 'node:path'

import { TRACKING_LIMITS, allowedHostsUsage, runeCount } from '../src/pages/trackingLimits.ts'

const here = dirname(fileURLToPath(import.meta.url))
const source = readFileSync(join(here, '..', '..', 'internal', 'tracking', 'tracking.go'), 'utf8')

// Go 쪽 선언은 `const Name = 300` 과 const 블록 안의 `Name = 200` 두 형태로 쓰여 있고,
// MaxSnippetBytes 만 `8 * 1024` 라는 식이다. 식을 읽지 않으면 그 하나를 놓친다.
function goConstant(name) {
  const match = source.match(new RegExp(`\\b${name}\\s*=\\s*([0-9]+(?:\\s*\\*\\s*[0-9]+)?)`))
  assert.ok(match, `internal/tracking/tracking.go 에 ${name} 선언이 없습니다`)
  return match[1].split('*').reduce((total, part) => total * Number(part.trim()), 1)
}

// 콘솔이 안내하는 숫자 ↔ 그 숫자를 거절에 쓰는 Go 상수.
const PAIRS = [
  ['snippetBytes', 'MaxSnippetBytes'],
  ['providerIdRunes', 'MaxProviderIDRunes'],
  ['providerUrlRunes', 'MaxProviderURLRunes'],
  ['providerOriginRunes', 'MaxProviderOriginRunes'],
  ['allowedHostRunes', 'MaxAllowedHostRunes'],
  ['allowedHostEntries', 'MaxAllowedHostEntries'],
  ['allowedHostsTotalRunes', 'MaxAllowedHostsTotalRunes'],
  ['snippetOriginEntries', 'MaxSnippetOriginEntries'],
  ['snippetOriginsTotalRunes', 'MaxSnippetOriginsTotalRunes'],
]

for (const [key, constant] of PAIRS) {
  test(`콘솔이 안내하는 ${key} 는 서버의 ${constant} 와 같다`, () => {
    assert.equal(
      TRACKING_LIMITS[key],
      goConstant(constant),
      `콘솔은 ${String(TRACKING_LIMITS[key])} 를 안내하는데 서버는 ${constant} 로 거절합니다`,
    )
  })
}

test('안내하지 않는 상한이 남아 있지 않다', () => {
  assert.deepEqual(Object.keys(TRACKING_LIMITS).sort(), PAIRS.map(([key]) => key).sort())
})

test('허용 출처는 쉼표·공백·탭·줄바꿈 어느 것으로 나눠도 같게 센다', () => {
  for (const list of [
    'https://a.local\nhttps://b.local',
    'https://a.local, https://b.local',
    'https://a.local\thttps://b.local',
    ' https://a.local  https://b.local ',
  ]) {
    assert.deepEqual(allowedHostsUsage(list), { entries: 2, runes: 30 }, `나눠 세지 못한 입력: ${JSON.stringify(list)}`)
  }
})

test('빈 줄과 구분자만 있는 입력은 항목이 아니다', () => {
  assert.deepEqual(allowedHostsUsage(''), { entries: 0, runes: 0 })
  assert.deepEqual(allowedHostsUsage('\n\n , ,\t'), { entries: 0, runes: 0 })
})

test('끝의 슬래시 하나는 서버가 떼고 저장하므로 세지 않는다', () => {
  assert.deepEqual(allowedHostsUsage('https://a.local/'), { entries: 1, runes: 15 })
  // 서버의 TrimSuffix 는 하나만 뗀다. 두 개를 떼면 콘솔이 서버보다 적게 센다.
  assert.deepEqual(allowedHostsUsage('https://a.local//'), { entries: 1, runes: 16 })
})

test('길이는 바이트가 아니라 룬으로 센다', () => {
  assert.equal(runeCount('한국'), 2)
  assert.equal(runeCount('𝄞'), 1)
  assert.deepEqual(allowedHostsUsage('https://한국.local'), { entries: 1, runes: 16 })
})
