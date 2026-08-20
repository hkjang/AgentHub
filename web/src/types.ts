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
export type UsageRow = { agentId:string; agentName:string; modelName:string; currency:string; runs:number; steps:number; inputTokens:number; outputTokens:number; cost:number; priced:boolean }
export type UsagePoint = { day:string; inputTokens:number; outputTokens:number; cost:number }
export type UsageReport = { from:string; to:string; currency:string; inputTokens:number; outputTokens:number; cost:number; unpricedTokens:number; agents:UsageRow[]; daily:UsagePoint[] }
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
  runner: 'prose' | 'flow' | 'cli' | 'dify'
  flowId: string
  flowOutputComponent: string
  cliApprovalMode: 'plan' | 'default' | 'auto-edit' | 'auto' | 'yolo'
  externalAppId: string
  externalInputKey: string
}
export type RuntimeFlow = { id:string; name:string; description?:string; endpointName?:string; mcpEnabled?:boolean }
export type ExternalApp = { id:string; name:string; provider:string; baseUrl:string; appKind:'workflow'|'chat'; description?:string; enabled:boolean; secretConfigured?:boolean }
export type UsageBudget = { windowDays:number; tokenBudget:number; tokensUsed:number; costBudget:number; costUsed:number; currency:string; maxRunning:number; runningNow:number }
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
  currentRunId?: string; lastError: string; createdAt: string; updatedAt: string
}
export type AgentRun = {
  id: string; taskId: string; agentId: string; attempt: number; status: string; agentVersion: number
  runtimeId?: string; modelName: string; traceId: string; workerId: string
  resumedSteps: number
  stepCount: number; toolCalls: number; totalTokens: number; durationMs: number
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
