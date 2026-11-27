export type ConsoleTab =
  | "overview"
  | "assets"
  | "gateway"
  | "apiKeys"
  | "analytics"
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

export interface ClientLoginBenefitStatus {
  eligible: boolean
  personalAccount: boolean
  withinPeriod: boolean
  firstLogin: boolean
  hasOldActivityLogin: boolean
  claimed: boolean
  rewardPhotons: number
  validDays: number
  reason?: string
  checkedAt?: string
}

export interface ClientLoginBenefitClaimResult {
  success: boolean
  claimed: boolean
  alreadyClaimed?: boolean
  skipped?: boolean
  rewardPhotons: number
  validDays: number
  message: string
  status?: ClientLoginBenefitStatus | null
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

export interface TaskJob {
  id: string
  kind: "single_task" | "account_all" | "all_accounts" | string
  accountId?: string
  accountName?: string
  taskKey?: string
  status: "running" | "completed" | "failed" | string
  message?: string
  error?: string
  startedAt: string
  completedAt?: string
  durationMs: number
  singleResult?: {
    success?: boolean
    skipped?: boolean
    message?: string
    taskTitle?: string
  }
  accountResult?: {
    summary?: string
    allWebDone?: boolean
    results?: Array<{
      success: boolean
      skipped?: boolean
      taskTitle?: string
      message?: string
    }>
  }
  allResults?: Array<{
    accountId: string
    displayName: string
    success: boolean
    error?: string
  }>
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
  clientBenefit?: ClientLoginBenefitStatus | null
  clientBenefitError?: string
  scheduler: SchedulerStatus
  activeJobs?: TaskJob[] | null
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
  updatedAt?: string
  enabled: boolean
  defaultModel?: string
  restrictModels: boolean
  allowedModels: string[]
  rateLimit: number
  concurrencyLimit: number
  expiresAt?: string
  tokenQuota: number
  dailyTokenQuota: number
  status: "active" | "disabled" | "expired" | "quota_exhausted" | string
  requestCount: number
  totalTokens: number
  todayRequests: number
  todayTokens: number
  lastUsedAt?: string
  remainingQuota?: number
  remainingDailyQuota?: number
}

export interface GatewayConfigView {
  enabled: boolean
  defaultModel: string
  allowNoAuth: boolean
  corsOrigins: string
  preflightMode: boolean
  sciMasterPrompt: boolean
  analyticsRetentionDays?: number
  storeId?: string
  apiKeys: GatewayApiKey[]
  modelEnabled?: Record<string, boolean>
}

export interface SciXModelItem {
  id: string
  name?: string
  api?: string
  hidden?: boolean
  context_window?: number
  max_output_tokens?: number
  input?: string[]
  reasoning_effort?: string
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
  apiKeyId?: string
  apiKeyName?: string
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

export interface UsageSummary {
  totalRequests: number
  successRequests: number
  failedRequests: number
  successRate: number
  promptTokens: number
  completionTokens: number
  reasoningTokens: number
  cachedTokens: number
  totalTokens: number
  weightedTokens?: number
  avgFirstTokenMs?: number
  avgDurationMs: number
  activeKeys: number
  activeModels: number
  activeAccounts: number
}

export interface TimeSeriesBucket {
  bucket: string
  label: string
  requests: number
  successRequests: number
  failedRequests: number
  promptTokens: number
  completionTokens: number
  reasoningTokens: number
  cachedTokens: number
  totalTokens: number
  weightedTokens?: number
}

export interface DimensionUsage {
  id: string
  name: string
  requests: number
  successRequests: number
  failedRequests: number
  promptTokens: number
  completionTokens: number
  reasoningTokens: number
  cachedTokens: number
  totalTokens: number
  weightedTokens?: number
  avgDurationMs: number
  sharePercent: number
}

export interface KeyModelUsage {
  apiKeyId: string
  apiKeyName: string
  model: string
  requests: number
  promptTokens: number
  completionTokens: number
  reasoningTokens: number
  cachedTokens: number
  totalTokens: number
  weightedTokens?: number
}

export interface FilterOption {
  id: string
  name: string
}

export interface GatewayAnalyticsData {
  rangeDays: number
  granularity: "hour" | "day"
  timezone: string
  from: string
  to: string
  summary: UsageSummary
  timeSeries: TimeSeriesBucket[]
  byKey: DimensionUsage[]
  byModel: DimensionUsage[]
  byAccount: DimensionUsage[]
  byKeyModel: KeyModelUsage[]
  availableKeys: FilterOption[]
  availableModels: FilterOption[]
  availableAccounts: FilterOption[]
  totalStoredRecords: number
  oldestRecordAt?: string
}

export interface GatewayAnalyticsResponse {
  ok: boolean
  analyticsRetentionDays: number
  analytics: GatewayAnalyticsData
}

export interface GatewayUsageLogItem {
  id: string
  createdAt: string
  apiKeyId: string
  apiKeyName: string
  accountId: string
  accountLabel: string
  model: string
  requestedModel?: string
  reasoningEffort?: string
  endpoint: string
  upstreamApi: string
  bridged: boolean
  routingMode: string
  sessionHash?: string
  requestId?: string
  clientIp?: string
  userAgent?: string
  stream: boolean
  hasTools: boolean
  hasVision: boolean
  preflightMode?: string
  sciMasterPrompt: boolean
  usageMultiplier: number
  weightedTokens: number
  status: number
  firstTokenMs: number
  durationMs: number
  promptTokens: number
  completionTokens: number
  reasoningTokens: number
  cachedTokens: number
  totalTokens: number
  error?: string
}

export interface UsageLogsPageResponse {
  ok: boolean
  items: GatewayUsageLogItem[]
  total: number
  page: number
  pageSize: number
  totalPages: number
}
