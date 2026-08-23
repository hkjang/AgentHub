export type User = { id: string; username: string; email: string; displayName: string; role: 'user' | 'manager' | 'admin' }
export type Version = { name: string; version: string; commit: string; buildTime: string }
export type Runtime = { id: string; agentId: string; ownerId: string; status: string; desiredState: string; crdName: string; podName: string; nodeName: string; endpoint: string; restartCount: number; failureReason: string; warmUntil?: string; updatedAt: string }
export type Agent = { id: string; ownerId: string; name: string; description: string; runtimeType: string; runtimeProfileId?: string; runtimeImageId?: string; securityProfileId?: string; networkProfileId?: string; mcpBundleId?: string; modelEndpointId?: string; workspaceId?: string; version: number; spec?: { systemPrompt?: string; customCommand?: string[]; customPort?: number }; runtime?: Runtime; createdAt: string; updatedAt: string }
export type Template = { id: string; name: string; slug: string; description: string; category: string; runtimeType: string; runtimeProfileId: string; version: number }
export type RuntimeProfile = { id: string; name: string; description: string; cpuMillis: number; memoryMb: number; storageGb: number; gpuCount: number; idleTimeoutSeconds: number }
export type Workspace = { id: string; name: string; type: string; sizeGb: number; repositoryUrl: string; branch: string; pvcName: string; status: string; updatedAt: string; gitCredentialSecretId?: string; gitCredentialKind?: '' | 'token' | 'ssh-key'; gitCredentialUsername?: string }
export type WorkspaceSnapshot = { id: string; workspaceId: string; name: string; status: string; storageRef: string; sizeBytes: number; createdAt: string }
export type MCPServer = { id: string; name: string; description: string; mode: 'shared' | 'dedicated' | 'sidecar'; transport: string; endpoint: string; image: string; port: number; riskLevel: string; approvalRequired: boolean; enabled: boolean; authType?: 'none'|'bearer'|'header'|'basic'; authHeader?: string; perUserCredential?: boolean; credentialConfigured?: boolean }
export type MCPBundle = { id: string; name: string; description: string; serverIds: string[]; enabled: boolean }
export type ModelEndpoint = { id: string; name: string; provider: string; baseUrl: string; defaultModel: string; secretConfigured?: boolean; enabled: boolean; inputPricePerMTok?: number; outputPricePerMTok?: number; currency?: string }
export type UsageRow = { agentId:string; agentName:string; modelName:string; currency:string; runs:number; steps:number; inputTokens:number; outputTokens:number; cost:number; priced:boolean; unmeteredRuns:number }
export type UsagePoint = { day:string; inputTokens:number; outputTokens:number; cost:number }
export type UsageReport = { from:string; to:string; currency:string; inputTokens:number; outputTokens:number; cost:number; unpricedTokens:number; runs:number; unmeteredRuns:number; agents:UsageRow[]; daily:UsagePoint[] }
export type RuntimeSession = { id: string; runtimeId: string; agentId: string; agentName: string; runtimeType: string; title: string; status: string; trace: unknown[]; createdAt: string; updatedAt: string }
export type Workflow = { id:string; name:string; description:string; mode:string; maxDepth:number; maxAgentCalls:number; maxToolCalls:number; maxDurationSeconds:number; maxParallelAgents:number; definition:{steps:{id:string;agentId:string;dependsOn:string[]}[]}; enabled:boolean; updatedAt:string }
export type EvaluationTestSet = { id:string; name:string; description:string; passThreshold:number; cases:{name:string;expectedRuntime?:string;requiresProfile?:boolean;requiresModel?:boolean;requiresMcp?:boolean;requiresWorkspace?:boolean;requiresRunning?:boolean;requiresSecurity?:boolean}[]; updatedAt:string }
export type AgentEvaluation = { id:string; agentId:string; agentName:string; agentVersion:number; testSetId:string; testSetName:string; status:string; score:number; metrics:{total:number;passed:number;failed:number;threshold:number;evaluationType:string}; result:{cases:{name:string;passed:boolean;failures:string[]}[]}; createdAt:string }
// A saved definition, and where the agent stands in its release cycle.
export type AgentVersion = { agentId:string; version:number; name:string; description:string; systemPrompt:string; spec:Record<string,unknown>; note:string; createdBy?:string; createdAt:string; promoted:boolean; evaluationStatus?:string; evaluationScore?:number }
export type AgentRelease = { promotedVersion?:number; promotedAt?:string; promotedBy?:string; promotionNote?:string; requirePromotion:boolean; currentVersion:number }
export type WorkflowStepResult = { id:string; agentId:string; agentName:string; status:string; output:string; error?:string; skipped?:boolean; durationMs:number; level:number }
export type ConsensusVote = { stepId:string; agentId:string; agentName:string; choice:string; normalised:string; abstained?:boolean }
export type ConsensusResult = { winner:string; agreed:number; total:number; unanimous:boolean; tie:boolean; votes:ConsensusVote[] }
export type RevisionRequest = { stepId:string; agent:string; request:string }
export type SupervisionRound = { round:number; approved:boolean; revisions:RevisionRequest[]|null }
export type SupervisionResult = { supervisor:string; approved:boolean; rounds:SupervisionRound[]; exhausted:boolean }
export type RoutingResult = { step:string; chosen:string[]; reason?:string; validated:boolean; fellBack?:boolean; note?:string }
export type WorkflowRunResult = { mode:string; status:string; output:string; steps:WorkflowStepResult[]; agentCalls:number; levels:string[][]; consensus?:ConsensusResult; supervision?:SupervisionResult; routing?:RoutingResult }
export type WorkflowRun = { id:string; workflowId:string; status:string; input:{input?:string;mode?:string}; output:WorkflowRunResult; startedAt?:string; finishedAt?:string; createdAt:string }
export type Notification = { id:string; type:string; title:string; message:string; resourceUrl:string; readAt?:string; createdAt:string }
export type PersonalSecret = { id: string; name: string; kind: string; keyVersion: number; lastUsedAt?: string; createdAt: string }
export type APIKey = { id: string; name: string; prefix: string; scopes: string[]; expiresAt?: string; lastUsedAt?: string; createdAt: string }

// --- Agent Execution Plane ---
export type ExecutionMode = 'interactive' | 'task' | 'scheduled' | 'event' | 'service' | 'hybrid'
export type AgentGoal = {
  agentId: string; description: string; successCriteria: string[]; failureCriteria: string[]; constraints: string
  maxSteps: number; maxToolCalls: number; maxDurationSeconds: number; maxRetries: number
  startOnDemand: boolean; stopAfterTask: boolean
  completionStrategy: 'agent' | 'rule' | 'judge' | 'composite'
  concurrencyPolicy: 'reject' | 'queue' | 'parallel' | 'replace'
  maxConcurrentRuns: number
  plannerMode: 'none' | 'native' | 'platform' | 'hybrid'
  approvalRequired: boolean
  maxDelegationDepth: number
  warmupSeconds: number
  keepWarmSeconds: number
  resumeFromCheckpoint: boolean
  tokenBudget: number
  runner: 'prose' | 'flow' | 'cli' | 'dify' | 'acp' | 'investigate' | 'review' | 'orca' | 'rpc' | 'agentserver'
  flowId: string
  flowOutputComponent: string
  approvalMode: 'plan' | 'default' | 'auto-edit' | 'auto' | 'yolo'
  externalAppId: string
  externalInputKey: string
  reviewMode?: 'workspace' | 'range' | 'commit' | 'scan' | 'trigger'
  reviewBaseRef?: string
  reviewHeadRef?: string
  reviewPath?: string
  reviewExclude?: string
  reviewFailOn?: '' | 'critical' | 'high' | 'medium' | 'low'
  orcaAgents?: string
  agentServerId?: string
  agentServerZone?: string
  agentServerDir?: string
  toolPolicy?: { deny?: string[]; allow?: string[] }
}
/** One located observation from a code review. The severity and category are the
 *  review engine's own words, not a mapping onto something of ours. */
export type ReviewFinding = {
  id:string; runId:string; agentId:string; filePath:string; startLine:number; endLine:number
  severity:'critical'|'high'|'medium'|'low'
  category:'bug'|'security'|'performance'|'maintainability'|'test'|'style'|'documentation'|'other'
  message:string; existingCode?:string; suggestion?:string
  status:'open'|'accepted'|'dismissed'|'fixed'; source:string; createdAt:string; fixTaskId?:string
}
/** What the review covered — the claim its findings rest on. An empty list of
 *  findings means one thing when 17 files were read and another when none were. */
export type ReviewCoverage = {
  runId:string; mode:string; baseRef:string; headRef:string; resolvedBase:string; resolvedHead:string
  filesSelected:number; filesReviewed:number; filesFailed:number; sessionId:string; engineVersion:string; status:string
}
export type RuntimeFlow = { id:string; name:string; description?:string; endpointName?:string; mcpEnabled?:boolean }
export type ExternalApp = { id:string; name:string; provider:string; baseUrl:string; appKind:'workflow'|'chat'; description?:string; enabled:boolean; secretConfigured?:boolean }
export type UsageBudget = { windowDays:number; tokenBudget:number; tokensUsed:number; costBudget:number; costUsed:number; currency:string; maxRunning:number; runningNow:number
  // The department's own budget, when the person belongs to one that has any.
  // Shown alongside because being refused for a limit the console never displayed
  // is how a quota becomes a mystery.
  department?: { name:string; tokenBudget:number; tokensUsed:number; costBudget:number; costUsed:number; maxRunning:number; runningNow:number } }
export type QueueSnapshot = { ready:number; running:number; workers:number; status:Record<string,number> }
export type WarmRuntime = { runtimeId:string; agentId:string; agentName:string; status:string; warmUntil:string }
export type AgentMemory = { id: string; scope: string; key: string; value: string; updatedAt: string }
export type AgentPlan = { id: string; runId: string; mode: string; goal: string; steps: unknown; createdAt: string }
export type AgentTrigger = {
  id: string; agentId: string; name: string; type: 'manual' | 'cron' | 'webhook' | 'event'; enabled: boolean
  schedule: string; timezone: string; taskTitle: string; taskInput: string; priority: string
  lastFiredAt?: string; nextFireAt?: string; hasSecret: boolean
  eventType?: string; eventFilter?: Record<string, unknown>
}
export type MCPServerRef = { id: string; name: string; mode: string; riskLevel?: string }
export type MCPToolPolicy = {
  id: string; agentId: string; serverId: string; serverName?: string
  mode: 'allow' | 'deny'; tools: string[]; approvalTools: string[]; updatedAt: string
}
export type PlatformEvent = {
  id: string; type: string; subjectType: string; subjectId: string
  payload: Record<string, unknown>; createdAt: string; dispatchedAt?: string
  attempts: number; lastError?: string; deadLetteredAt?: string
  deliveries: number; deliveredTo?: string
}
export type AgentTask = {
  id: string; agentId: string; agentName?: string; title: string; input: string; priority: string
  status: string; source: string; triggerId?: string; attempts: number; scheduledAt: string
  parentTaskId?: string; delegationDepth: number; approvalId?: string
  currentRunId?: string; lastError: string
  // Why the task is queued rather than running — a quota it is waiting behind.
  // Separate from lastError because waiting is not failing.
  waitingReason?: string
  createdAt: string; updatedAt: string
}
export type Metering = '' | 'gateway' | 'agent' | 'context_only' | 'unmetered'
export type AgentRun = {
  id: string; taskId: string; agentId: string; agentName?: string; attempt: number; status: string; agentVersion: number
  runtimeId?: string; modelName: string; traceId: string; workerId: string
  resumedSteps: number
  stepCount: number; toolCalls: number; totalTokens: number; metering?: Metering; durationMs: number
  result: string; failureReason: string
  completion: { strategy?: string; passed?: boolean; reason?: string; met?: string[]; unmet?: string[] }
  startedAt: string; finishedAt?: string
}
export type AgentRunStep = {
  id: string; runId: string; sequence: number; type: string; title: string; input: string; output: string
  status: string; error: string; promptTokens: number; completionTokens: number; durationMs: number; createdAt: string
}
export type AgentRunEvent = { id: number; runId: string; taskId: string; type: string; message: string; details: Record<string, unknown>; occurredAt: string }
export type AgentArtifact = { id: string; runId: string; taskId: string; agentId: string; name: string; type: string; contentType: string; sizeBytes: number; createdAt: string }

export type Limits = { maxRuntimes?: number; maxCpuMillis?: number; maxMemoryMb?: number; maxStorageGb?: number; maxRunningTasks?: number; tokenBudget?: number; costBudget?: number }
export type Held = { runtimes: number; cpuMillis: number; memoryMb: number; storageGb: number }
export type Department = { id: string; name: string; description: string; quota: { perMember: Limits; total: Limits }; members: number; held: Held; createdAt: string; updatedAt: string }
export type UserQuota = { ownerId: string; username: string; quota: Limits; note: string; updatedAt: string }
export type EffectiveQuota = { ownerId: string; departmentId?: string; department?: string; platform: Limits; inherited: Limits; personal: Limits; effective: Limits; held: Held; departmentQuota: { perMember: Limits; total: Limits }; departmentHeld: Held }
export type ManagedUser = User & { status: string; managerId?: string; departmentId?: string; lastLoginAt?: string; createdAt: string }

/** A server this deployment may send work to. It is not a runtime the platform
 *  starts — it is capacity somebody else runs, which is why an administrator
 *  registers it and why what matters about it is where it sits. */
export type AgentServer = {
  id:string; name:string; baseUrl:string; kind:string; networkZone:string
  capacity:number; enabled:boolean; running?:number
  health:'unknown'|'healthy'|'unreachable'|'refused'; healthDetail?:string
  checkedAt?:string; createdAt:string; updatedAt:string
}
/** What a Goal author sees of those servers: names, networks and health, never
 *  addresses. */
export type UsableAgentServer = { id:string; name:string; networkZone:string; health:AgentServer['health']; kind:string }
