export type RafStateUpdate<T> = (current: T) => T

export interface RafUpdateBatcher<T> {
  enqueue: (update: RafStateUpdate<T>) => void
  flush: () => void
  discard: () => void
}

export interface RafUpdateBatcherOptions {
  /** Minimum time between React commits. Updates remain ordered and are never dropped. */
  minIntervalMs?: number
}

// Text streaming stays perceptually fluid at this cadence while leaving most
// of each frame available for Markdown, virtualization, and input handling.
export const STREAMING_RENDER_INTERVAL_MS = 80
// Hidden tabs suspend requestAnimationFrame; flush on a bounded timer instead of
// letting pending state closures accumulate indefinitely in the background.
const BACKGROUND_COMMIT_FALLBACK_MS = 250

function isDocumentHidden() {
  return typeof document !== 'undefined' && document.visibilityState === 'hidden'
}

/** Coalesces ordered state updates and commits them on a render-budgeted animation frame. */
export function createRafUpdateBatcher<T>(
  commit: (update: RafStateUpdate<T>) => void,
  options: RafUpdateBatcherOptions = {},
): RafUpdateBatcher<T> {
  let frameID: number | null = null
  let timerID: ReturnType<typeof setTimeout> | null = null
  let fallbackTimerID: ReturnType<typeof setTimeout> | null = null
  let lastCommitAt: number | null = null
  let pending: RafStateUpdate<T>[] = []
  const minIntervalMs = Math.max(0, options.minIntervalMs || 0)
  const fallbackDelayMs = Math.max(minIntervalMs, BACKGROUND_COMMIT_FALLBACK_MS)

  const commitPending = () => {
    if (pending.length === 0) return
    const updates = pending
    pending = []
    lastCommitAt = Date.now()
    commit(current => updates.reduce((next, apply) => apply(next), current))
  }

  const cancelScheduledCommit = () => {
    if (frameID !== null) {
      cancelAnimationFrame(frameID)
      frameID = null
    }
    if (timerID !== null) {
      clearTimeout(timerID)
      timerID = null
    }
    if (fallbackTimerID !== null) {
      clearTimeout(fallbackTimerID)
      fallbackTimerID = null
    }
  }

  const scheduleFrame = () => {
    if (frameID !== null || fallbackTimerID !== null) return
    if (isDocumentHidden()) {
      fallbackTimerID = setTimeout(() => {
        fallbackTimerID = null
        commitPending()
      }, fallbackDelayMs)
      return
    }
    frameID = requestAnimationFrame(() => {
      cancelScheduledCommit()
      commitPending()
    })
    fallbackTimerID = setTimeout(() => {
      cancelScheduledCommit()
      commitPending()
    }, fallbackDelayMs)
  }

  const scheduleCommit = () => {
    if (frameID !== null || timerID !== null || fallbackTimerID !== null) return
    const elapsed = lastCommitAt === null ? minIntervalMs : Date.now() - lastCommitAt
    const delay = Math.max(0, minIntervalMs - elapsed)
    if (isDocumentHidden()) {
      timerID = setTimeout(() => {
        timerID = null
        commitPending()
      }, Math.max(delay, fallbackDelayMs))
      return
    }
    if (delay === 0) {
      scheduleFrame()
      return
    }
    timerID = setTimeout(() => {
      timerID = null
      scheduleFrame()
    }, delay)
  }

  const flush = () => {
    cancelScheduledCommit()
    commitPending()
  }

  const discard = () => {
    cancelScheduledCommit()
    pending = []
  }

  const enqueue = (update: RafStateUpdate<T>) => {
    pending.push(update)
    scheduleCommit()
  }

  return { enqueue, flush, discard }
}
