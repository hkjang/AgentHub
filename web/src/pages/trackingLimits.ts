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

// 허용 출처 입력이 실제로 몇 개 · 몇 자가 되는지. 서버의 splitHosts 를 그대로 옮긴 것이다:
// 쉼표·공백·탭·줄바꿈으로 나누고, 공백을 떼고, 끝의 슬래시를 하나 떼고, 빈 것은 버린다.
// 콘솔이 서버와 다르게 세면 안내가 거짓이 되므로 그 동작을 테스트가 고정한다.
export function allowedHostsUsage(list: string): { entries: number; runes: number } {
  const entries: string[] = []
  for (const field of list.split(/[,\s]+/)) {
    const trimmed = field.trim().replace(/\/$/, '')
    if (trimmed !== '') entries.push(trimmed)
  }
  return { entries: entries.length, runes: entries.reduce((total, entry) => total + runeCount(entry), 0) }
}
