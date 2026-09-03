import type { EffectiveQuota, Limits } from './types'

// One description of the limits, so the admin screen, the drawer and the
// personal panel name and order them the same way. A limit whose name differs
// between two screens reads as two different limits.

export type LimitField = { key:keyof Limits; label:string; unit:string; step?:number; held?:'runtimes'|'cpuMillis'|'memoryMb'|'storageGb'|'gpus' }

export const LIMIT_FIELDS:LimitField[] = [
  {key:'maxRuntimes',label:'Runtime 수',unit:'개',held:'runtimes'},
  {key:'maxCpuMillis',label:'CPU',unit:'m',step:100,held:'cpuMillis'},
  {key:'maxMemoryMb',label:'Memory',unit:'MB',step:256,held:'memoryMb'},
  {key:'maxStorageGb',label:'Storage',unit:'GB',held:'storageGb'},
  // The dimension this table was missing. Without a row here a department or a
  // person had no way to be given a GPU ceiling at all — the field existed on
  // the server and on the platform settings screen, so the quota screens showed
  // every limit except the scarce one and read as though it were unlimited.
  {key:'maxGpus',label:'GPU',unit:'개',held:'gpus'},
  {key:'maxRunningTasks',label:'동시 실행 작업',unit:'개'},
  {key:'tokenBudget',label:'토큰 예산 (30일)',unit:'토큰',step:1000},
  {key:'costBudget',label:'비용 예산 (30일)',unit:'',step:0.5}
]

/** limitSummary is the one-line form used in tables and cards. */
export function limitSummary(limits:Limits):string {
  const parts = LIMIT_FIELDS.filter((field) => (limits[field.key] ?? 0) > 0).map((field) => `${field.label} ${limits[field.key]}${field.unit}`)
  return parts.length===0 ? '제한 없음' : parts.join(' · ')
}

/**
 * quotaSource says which level set the limit that is actually in force. An
 * administrator looking at "Runtime 2개" needs to know whether to edit the
 * person, the department, or the platform default — the number alone does not
 * say, and the wrong guess edits a setting that changes nothing.
 */
export function quotaSource(value:EffectiveQuota, key:keyof Limits):{label:string;tone:string} {
  if ((value.personal?.[key] ?? 0) > 0) return {label:'개인 예외',tone:'personal'}
  if ((value.inherited?.[key] ?? 0) > 0) return {label:value.department?`부서 · ${value.department}`:'부서',tone:'department'}
  if ((value.platform?.[key] ?? 0) > 0) return {label:'플랫폼 기본',tone:'platform'}
  return {label:'제한 없음',tone:'none'}
}
