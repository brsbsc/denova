import { afterEach, describe, expect, it, vi } from 'vitest'
import { createRafUpdateBatcher } from './raf-update-batcher'

describe('createRafUpdateBatcher', () => {
  afterEach(() => {
    vi.useRealTimers()
    vi.restoreAllMocks()
    Object.defineProperty(document, 'visibilityState', {
      configurable: true,
      value: 'visible',
    })
  })

  it('commits queued updates via timer fallback when the tab is hidden and rAF is suspended', () => {
    vi.useFakeTimers()
    Object.defineProperty(document, 'visibilityState', {
      configurable: true,
      value: 'hidden',
    })
    const raf = vi.spyOn(window, 'requestAnimationFrame').mockImplementation(() => 1)
    let state = 0
    const commit = vi.fn((update: (current: number) => number) => {
      state = update(state)
    })
    const batcher = createRafUpdateBatcher<number>(commit, { minIntervalMs: 80 })

    batcher.enqueue((n) => n + 1)
    batcher.enqueue((n) => n + 2)

    expect(raf).not.toHaveBeenCalled()
    expect(commit).not.toHaveBeenCalled()

    vi.advanceTimersByTime(300)

    expect(commit).toHaveBeenCalledTimes(1)
    expect(state).toBe(3)
  })

  it('commits via fallback timer if the tab becomes hidden after rAF was already scheduled', () => {
    vi.useFakeTimers()
    Object.defineProperty(document, 'visibilityState', {
      configurable: true,
      value: 'visible',
    })
    const cancelRaf = vi.spyOn(window, 'cancelAnimationFrame').mockImplementation(() => {})
    vi.spyOn(window, 'requestAnimationFrame').mockImplementation(() => 42)
    let state = 0
    const commit = vi.fn((update: (current: number) => number) => {
      state = update(state)
    })
    const batcher = createRafUpdateBatcher<number>(commit, { minIntervalMs: 80 })

    batcher.enqueue((n) => n + 5)
    expect(commit).not.toHaveBeenCalled()

    vi.advanceTimersByTime(300)

    expect(cancelRaf).toHaveBeenCalledWith(42)
    expect(commit).toHaveBeenCalledTimes(1)
    expect(state).toBe(5)
  })
})
