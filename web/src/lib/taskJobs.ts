import { apiRequest } from "@/lib/api"
import type { TaskJob } from "@/types/console"

export interface TaskJobStartResponse {
  ok?: boolean
  async?: boolean
  job?: TaskJob
  data?: {
    summary?: string
    message?: string
    success?: boolean
  }
  results?: Array<{ success: boolean }>
}

function sleepWithSignal(ms: number, signal?: AbortSignal): Promise<void> {
  return new Promise((resolve, reject) => {
    if (signal?.aborted) {
      reject(new DOMException("Aborted", "AbortError"))
      return
    }
    const timer = window.setTimeout(() => {
      signal?.removeEventListener("abort", onAbort)
      resolve()
    }, ms)
    function onAbort() {
      window.clearTimeout(timer)
      reject(new DOMException("Aborted", "AbortError"))
    }
    signal?.addEventListener("abort", onAbort, { once: true })
  })
}

export async function pollTaskJob(
  jobId: string,
  opts?: {
    intervalMs?: number
    onUpdate?: (job: TaskJob) => void
    signal?: AbortSignal
  }
): Promise<TaskJob> {
  const intervalMs = opts?.intervalMs ?? 1500
  let consecutiveErrors = 0

  while (true) {
    if (opts?.signal?.aborted) {
      throw new DOMException("Aborted", "AbortError")
    }
    try {
      const res = await apiRequest<{ ok: boolean; job?: TaskJob }>(
        `/api/tasks/jobs/${encodeURIComponent(jobId)}`
      )
      consecutiveErrors = 0
      const job = res.job
      if (job) {
        opts?.onUpdate?.(job)
        if (job.status === "completed" || job.status === "failed") {
          return job
        }
      }
    } catch (err) {
      consecutiveErrors += 1
      if (consecutiveErrors >= 5) {
        throw err
      }
    }
    await sleepWithSignal(intervalMs, opts?.signal)
  }
}
