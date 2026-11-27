import { act, render } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { TerminalTabView } from './TerminalTabView'

const webglInstances = vi.hoisted(() => [] as Array<{
  dispose: ReturnType<typeof vi.fn>
  loseContext: ReturnType<typeof vi.fn>
  triggerContextLoss: () => void
  isLost: () => boolean
  setLost: (lost: boolean) => void
}>)

vi.mock('next-themes', () => ({
  useTheme: () => ({ resolvedTheme: 'dark' }),
}))

const fontScaleSnapshot = vi.hoisted(() => ({ terminal: 13 }))

vi.mock('@/features/settings/content-font-scale', () => ({
  getContentFontScale: () => fontScaleSnapshot,
  subscribeContentFontScale: () => () => {},
}))

vi.mock('@xterm/addon-fit', () => ({
  FitAddon: class MockFitAddon {
    fit = vi.fn()
  },
}))

vi.mock('@xterm/addon-webgl', () => ({
  WebglAddon: class MockWebglAddon {
    private lost = false
    private contextLossListener: (() => void) | null = null
    dispose = vi.fn()
    _renderer: { _gl: { isContextLost: () => boolean; getExtension: (name: string) => { loseContext: () => void } | null } }

    constructor() {
      const loseContext = vi.fn(() => {
        this.lost = true
      })
      this._renderer = {
        _gl: {
          isContextLost: () => this.lost,
          getExtension: (name: string) => (name === 'WEBGL_lose_context' ? { loseContext } : null),
        },
      }
      webglInstances.push({
        dispose: this.dispose,
        loseContext,
        triggerContextLoss: () => {
          this.lost = true
          this.contextLossListener?.()
        },
        isLost: () => this.lost,
        setLost: (value: boolean) => { this.lost = value },
      })
    }

    onContextLoss(listener: () => void) {
      this.contextLossListener = listener
      return { dispose: () => { this.contextLossListener = null } }
    }
  },
}))

vi.mock('@xterm/xterm', () => ({
  Terminal: class MockTerminal {
    cols = 80
    rows = 24
    options: Record<string, unknown> = {}
    loadAddon = vi.fn()
    open = vi.fn()
    onData = vi.fn()
    onResize = vi.fn()
    onTitleChange = vi.fn()
    refresh = vi.fn()
    write = vi.fn()
    reset = vi.fn()
    dispose = vi.fn()
  },
}))

vi.mock('./api', () => ({
  closeTerminalSession: vi.fn().mockResolvedValue(undefined),
  createTerminalSession: vi.fn(() => new Promise(() => {})),
  getTerminalRuntimeStatus: vi.fn(() => new Promise(() => {})),
  terminalAttachURL: vi.fn(() => 'ws://localhost/terminal'),
}))

vi.mock('./connection', () => ({
  TerminalConnection: class MockTerminalConnection {
    send = vi.fn()
    resize = vi.fn()
    close = vi.fn()
  },
}))

describe('TerminalTabView WebGL lifecycle', () => {
  const rafCallbacks: FrameRequestCallback[] = []

  beforeEach(() => {
    webglInstances.length = 0
    rafCallbacks.length = 0
    vi.stubGlobal('ResizeObserver', class MockResizeObserver {
      observe() {}
      disconnect() {}
    })
    vi.spyOn(window, 'requestAnimationFrame').mockImplementation((cb) => {
      rafCallbacks.push(cb)
      return rafCallbacks.length
    })
    vi.spyOn(window, 'cancelAnimationFrame').mockImplementation(() => {})
  })

  afterEach(() => {
    vi.restoreAllMocks()
    vi.unstubAllGlobals()
  })

  const tab = {
    kind: 'terminal' as const,
    id: 'term-1',
    projectId: 'project-a',
    workspace: '/books/a',
    group: 'primary' as const,
    profileId: 'shell',
    title: 'Shell',
  }

  function flushFrames() {
    while (rafCallbacks.length > 0) {
      rafCallbacks.shift()?.(performance.now())
    }
  }

  it('reuses a healthy WebGL context across tab activations and explicitly releases it on unmount', () => {
    const { rerender, unmount } = render(
      <TerminalTabView
        tab={tab}
        active
        onSessionEstablished={() => true}
        onTitleChange={() => {}}
        onStatusChange={() => {}}
      />,
    )

    act(() => flushFrames())
    expect(webglInstances).toHaveLength(1)

    rerender(
      <TerminalTabView
        tab={tab}
        active={false}
        onSessionEstablished={() => true}
        onTitleChange={() => {}}
        onStatusChange={() => {}}
      />,
    )
    rerender(
      <TerminalTabView
        tab={tab}
        active
        onSessionEstablished={() => true}
        onTitleChange={() => {}}
        onStatusChange={() => {}}
      />,
    )
    act(() => flushFrames())
    expect(webglInstances).toHaveLength(1)

    act(() => {
      webglInstances[0].triggerContextLoss()
    })
    rerender(
      <TerminalTabView
        tab={tab}
        active={false}
        onSessionEstablished={() => true}
        onTitleChange={() => {}}
        onStatusChange={() => {}}
      />,
    )
    rerender(
      <TerminalTabView
        tab={tab}
        active
        onSessionEstablished={() => true}
        onTitleChange={() => {}}
        onStatusChange={() => {}}
      />,
    )
    act(() => flushFrames())
    expect(webglInstances).toHaveLength(2)

    unmount()
    expect(webglInstances[1].dispose).toHaveBeenCalledTimes(1)
    expect(webglInstances[1].loseContext).toHaveBeenCalledTimes(1)
  })
})
