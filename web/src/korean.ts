/**
 * The particle that follows a word.
 *
 * Korean picks 이/가, 을/를 and 은/는 by whether the preceding word ends in a
 * consonant, so a sentence that writes one straight after an interpolated value
 * is wrong for half the values it will be given. The console shipped
 * "현재 정의 v3는 승격되지 않아" — v3 is 삼, which ends in ㅁ, so it takes 은.
 *
 * The same rule lives in internal/korean on the server, because both tiers write
 * these sentences and neither should be guessing.
 */

// How each digit is pronounced, and whether that reading ends in a consonant. A
// number ending in zero is read 영/십/백/천/만, every one of which does, so the
// last digit is enough to decide.
const DIGIT_ENDS_IN_CONSONANT: Record<string, boolean> = {
  "0": true, // 영
  "1": true, // 일
  "2": false, // 이
  "3": true, // 삼
  "4": false, // 사
  "5": false, // 오
  "6": true, // 육
  "7": true, // 칠
  "8": true, // 팔
  "9": false, // 구
};

/**
 * Whether the last character is pronounced with a final consonant.
 *
 * Anything neither Hangul nor a digit — a runtime's Latin name, a host — is
 * answered false: a reader says "github.com이" and "AWS는" by sound and there is
 * no rule to encode, so those sentences are written without a particle instead.
 */
export function endsInConsonant(word: string): boolean {
  const last = [...String(word ?? "")].pop();
  if (!last) return false;
  if (last in DIGIT_ENDS_IN_CONSONANT) return DIGIT_ENDS_IN_CONSONANT[last];
  const code = last.charCodeAt(0);
  if (code < 0xac00 || code > 0xd7a3) return false;
  // A Hangul syllable is (initial × 21 + medial) × 28 + final, so a remainder of
  // zero means no final consonant.
  return (code - 0xac00) % 28 !== 0;
}

/** 이 or 가. */
export const subject = (word: string) => (endsInConsonant(word) ? "이" : "가");
/** 을 or 를. */
export const object = (word: string) => (endsInConsonant(word) ? "을" : "를");
/** 은 or 는. */
export const topic = (word: string) => (endsInConsonant(word) ? "은" : "는");
