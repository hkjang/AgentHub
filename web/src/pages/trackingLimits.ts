// 추적 설정의 상한. 서버는 과대 입력을 잘라 저장하지 않고 거절하므로, 관리자가 저장을
// 누르고 거절문을 읽은 뒤에야 숫자를 알게 되는 대신 입력 옆에서 미리 볼 수 있어야 한다.
//
// 같은 숫자가 Go 와 여기 두 곳에 적혀 있다. 이 저장소에는 Go 상수를 프런트로 전달하는
// 계약이 없어 피할 수 없는 중복이고, 정본은 서버다 — 거절을 내리는 쪽이 서버이므로.
// 어긋나면 콘솔이 사실이 아닌 숫자를 안내하게 되므로, scripts/tracking-limits-check.test.mjs
// 가 internal/tracking/tracking.go 의 상수를 읽어 아래 값과 맞는지 본다. 짝은 이렇다:
// snippetBytes=MaxSnippetBytes, providerIdRunes=MaxProviderIDRunes,
// providerUrlRunes=MaxProviderURLRunes, providerOriginRunes=MaxProviderOriginRunes,
// allowedHostRunes=MaxAllowedHostRunes,
// allowedHostEntries=MaxAllowedHostEntries, allowedHostsTotalRunes=MaxAllowedHostsTotalRunes,
// snippetOriginEntries=MaxSnippetOriginEntries, snippetOriginsTotalRunes=MaxSnippetOriginsTotalRunes.
export const TRACKING_LIMITS = {
  snippetBytes: 8 * 1024,
  providerIdRunes: 200,
  providerUrlRunes: 1024,
  providerOriginRunes: 300,
  allowedHostRunes: 300,
  allowedHostEntries: 64,
  allowedHostsTotalRunes: 4096,
  snippetOriginEntries: 32,
  snippetOriginsTotalRunes: 1024,
}

// 서버는 길이를 룬으로 센다 — 주소는 한국어로도 쓸 수 있으므로. 자바스크립트의 length 는
// UTF-16 단위라 네 바이트 문자를 둘로 세므로, 코드포인트로 펼쳐 센다.
export function runeCount(text: string): number {
  return [...text].length
}

// 서버의 splitHosts 는 FieldsFunc 로 쉼표·공백·\n·\r·\t **다섯 글자에서만** 나눈다. 여기를
// 자바스크립트의 `\s` 로 쓰면 안 된다 — `\s` 는 NBSP(U+00A0)·전각 공백(U+3000)·U+2028·\v·\f
// 까지 포함하므로 서버가 한 항목으로 읽는 자리에서 콘솔만 나누게 된다. 그 어긋남은 작지
// 않다: 출처 20개를 NBSP 로 이어 붙이면 콘솔은 20개·480자(전부 상한 안)로 세는데 서버는
// 한 항목 499룬으로 읽어 MaxAllowedHostRunes(300)로 거절한다. 그 한 항목이 https:// 접두
// 검사를 통과하므로 "어차피 형식 오류" 로도 덮이지 않는다. 그래서 다섯 글자를 그대로 적는다.
const HOST_SEPARATOR = /[, \t\r\n]+/

// 나눈 뒤 서버는 strings.TrimSpace 로 양끝을 떼는데, 그 집합은 unicode.IsSpace — 위 다섯
// 글자에 \v·\f·U+0085 와 유니코드 Z 범주를 더한 것이다. JS 의 trim() 과 두 글자가 다르다:
// trim() 은 U+FEFF 를 떼지만(서버는 남긴다) U+0085 는 남긴다(서버는 뗀다). U+FEFF 쪽이
// 콘솔을 서버보다 **적게** 세게 만드는 방향이므로 — 300룬 항목 앞에 U+FEFF 하나면 서버는
// 301룬으로 거절하는데 콘솔은 300룬이라 안내한다 — 집합을 직접 적어 맞춘다.
const GO_SPACE = '\\t\\n\\v\\f\\r \\u0085\\u00a0\\u1680\\u2000-\\u200a\\u2028\\u2029\\u202f\\u205f\\u3000'
const HOST_TRIM = new RegExp(`^[${GO_SPACE}]+|[${GO_SPACE}]+$`, 'g')

// 허용 출처 입력이 실제로 몇 개 · 몇 자가 되는지. 서버의 splitHosts 를 그대로 옮긴 것이다:
// 위 다섯 글자로 나누고, TrimSpace 와 같은 집합으로 양끝을 떼고, 끝의 슬래시를 하나 떼고,
// 빈 것은 버린다. 콘솔이 서버와 다르게 세면 안내가 거짓이 되므로 그 동작을 테스트가 고정한다.
export function allowedHostsUsage(list: string): { entries: number; runes: number } {
  const entries: string[] = []
  for (const field of list.split(HOST_SEPARATOR)) {
    const trimmed = field.replace(HOST_TRIM, '').replace(/\/$/, '')
    if (trimmed !== '') entries.push(trimmed)
  }
  return { entries: entries.length, runes: entries.reduce((total, entry) => total + runeCount(entry), 0) }
}
