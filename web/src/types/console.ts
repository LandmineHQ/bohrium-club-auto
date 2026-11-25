export type ConsoleTab =
  | "overview"
  | "assets"
  | "gateway"
  | "logs"
  | "recon"
  | "settings"

export interface AccountScheduleInfo {
  accountId?: string
  plannedTodayAt?: string
  doneToday?: boolean
  running?: boolean
  lastScheduledDay?: string
  lastRunSummary?: string
}

export interface AccountSummaryView {
  id: string
  userId: number
  orgId: number
  userType: number
  label: string
  nickname: string
  phone?: string
  email?: string
  avatar?: string
  loginMethod: string
  clubBound: boolean
  expiresAt?: string
  lastLoginAt?: string
  lastRefreshAt?: string
  storeId: string
  poolEnabled: boolean
  poolOrder: number
  checkinEnabled: boolean
  photonBalance: number
  orgBalance: number
  walletBalanceCny: number
  balanceUpdatedAt?: string
  healthStatus: "active" | "expired" | "insufficient" | "cooldown" | string
  cooldownUntil?: string
  lastError?: string
  lastScheduledDay?: string
  lastRunAt?: string
  lastRunSummary?: string
  displayName: string
  effectiveBalance: number
  weightPercent: number
  healthyForPool: boolean
  expiresInSec: number
  activeBindings: number
  schedule?: AccountScheduleInfo
}

export interface AccountOverviewSummary {
  totalAccounts: number
  onlineAccounts: number
  poolAccounts: number
  healthyPoolAccounts: number
  totalPhotons: number
  totalOrgBalance: number
  poolEffectivePhotons: number
  checkinDoneToday: number
  checkinEnabledCount: number
  activeBindings: number
}

export interface DailyTaskItem {
  taskKey: string
  actionKey?: string
  titleZh?: string
  descriptionZh?: string
  rewardAmount: number
  status: number
  statusLabel: string
  skipped?: boolean
  skipReason?: string
}

export interface DailyTasksResponse {
  taskDate?: string
  totalCount: number
  completedCount: number
  skippedCount: number
  earnedPhotons: number
  tasks: DailyTaskItem[]
}

export interface QuotaMetric {
  remaining: number | null
  total: number | null
}

export interface ExpiringLot {
  remaining: number
  description?: string
  sourceType?: string
  createdTime?: string
  expireTime?: string
}

export interface IntegralHistoryItem {
  amount: number
  description?: string
  type?: string
  expireAt?: string
  createdAt?: string
}

export interface StorageInfo {
  usedPercent?: number
  remainingHuman?: string
  usedHuman?: string
  totalHuman?: string
}

export interface PointsAndCapacity {
  photonBalance: number
  orgBalance: number
  orgBalanceLimit: number
  walletBalance: number
  expiring7DayPhotons: number
  storage?: StorageInfo
  paperQuota?: QuotaMetric
  knowledgeChat?: QuotaMetric
  deepResearch?: QuotaMetric
  expiringLots?: ExpiringLot[]
  recentIntegralHistory?: IntegralHistoryItem[]
  warnings?: string[]
}

export interface SchedulerStatus {
  enabled: boolean
  running: boolean
  currentJob?: string
  timeHHMM: string
  timezone: string
  jitterMinutes: number
  nextRunAt?: string
  lastScheduledDay?: string
  lastRunSummary?: string
}

export interface PromptStats {
  scienceSpacePrompts: number
  academicWritingPrompts: number
  literatureReadingPrompts: number
  paperCatalogCount: number
}

export interface RunLogItem {
  id: string
  accountId?: string
  accountUid?: number
  nickname?: string
  timestamp: string
  trigger: string
  action: string
  taskKey?: string
  taskTitle?: string
  success: boolean
  skipped?: boolean
  message: string
  durationMs: number
  meta?: {
    promptPreview?: string
    [key: string]: unknown
  }
}

export interface OverviewResponse {
  ok: boolean
  version: string
  userAgent: string
  selectedAccountId: string
  accounts: AccountSummaryView[]
  accountSummary: AccountOverviewSummary
  tasks?: DailyTasksResponse | null
  tasksError?: string
  assets?: PointsAndCapacity | null
  assetsError?: string
  scheduler: SchedulerStatus
  promptStats: PromptStats
  pdfStats: { cached: number; custom: number }
  reconDir: string
  logs: RunLogItem[]
}

export interface PoolMemberView {
  accountId: string
  userId: number
  displayName: string
  effectiveBalance: number
  photonBalance: number
  orgBalance: number
  weightPercent: number
  healthy: boolean
  healthStatus?: string
  activeBindings: number
}

export interface SessionBindingItem {
  sessionHash: string
  accountId: string
  accountLabel?: string
  accountNickname?: string
  accountUid?: number
  modelHint?: string
  hitCount: number
  createdAt: string
  lastUsedAt: string
  expiresAt: string
}

export interface GatewayApiKey {
  id: string
  name: string
  key: string
  createdAt?: string
  enabled: boolean
}

export interface GatewayConfigView {
  enabled: boolean
  defaultModel: string
  allowNoAuth: boolean
  corsOrigins: string
  preflightMode: boolean
  sciMasterPrompt: boolean
  storeId?: string
  apiKeys: GatewayApiKey[]
}

export interface SciXModelItem {
  id: string
  name?: string
  api?: string
  hidden?: boolean
  context_window?: number
  max_output_tokens?: number
  input?: string[]
  reasoning_efforts?: string[]
  presentation?: {
    name?: string
    description?: string
    visible?: boolean
    default?: boolean
    sortOrder?: number
    usageMultiplier?: number
  }
}

export interface GatewayRecentRequest {
  timestamp: string
  endpoint: string
  stream?: boolean
  model?: string
  accountId?: string
  accountLabel?: string
  routingMode?: string
  status: number
  error?: string
  durationMs: number
  promptTokens: number
  completionTokens: number
  reasoningTokens: number
}

export interface GatewayStatusResponse {
  ok: boolean
  bohriumLoggedIn: boolean
  config: GatewayConfigView
  pool: {
    pooled: PoolMemberView[]
    candidates: AccountSummaryView[]
    totalEffectiveBalance: number
    activeBindingCount?: number
    ttlDays?: number
  }
  accounts: AccountSummaryView[]
  bindings: SessionBindingItem[]
  catalog?: { data?: SciXModelItem[] } | null
  catalogError?: string
  stats: {
    totalRequests: number
    chatRequests: number
    responseRequests: number
    streamRequests: number
    promptTokens: number
    completionTokens: number
    reasoningTokens: number
    storedResponses: number
    recentRequests: GatewayRecentRequest[]
  }
  playgroundPrompts?: string[]
}

export interface SettingsPayload {
  userAgent: string
  proxyUrl: string
  taskDelaySec: number
  cancelAfterMs: number
  reconDir?: string
  schedule: {
    enabled: boolean
    timeHHMM: string
    timezone: string
    jitterMinutes: number
    autoClaim: boolean
    autoRefresh: boolean
  }
}

export interface ReconStatusResponse {
  ok: boolean
  baselineExists: boolean
  manifestPath: string
  fetchedAt?: string
  assetCount?: number
  totalBytes?: number
  markers?: {
    plazaTasksEndpoint?: boolean
    plazaClaimEndpoint?: boolean
    gatewayHeader?: boolean
    wechatQrEndpoint?: boolean
  }
}

export interface ReconDiffData {
  same: boolean
  summary: string
  markerChanges?: string[]
  added?: Array<{ url: string }>
  changed?: Array<{ url: string }>
  removed?: Array<{ url: string }>
}
