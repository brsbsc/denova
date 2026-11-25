import { act, renderHook } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { emptyProjectTabState, readStoredWorkbenchState, type AgentChatWorkbenchState } from '../tab-state'
import { TerminalConnection } from './connection'
import { useTerminalSessionLifecycle } from './use-terminal-session-lifecycle'

vi.mock('./api', () => ({
  closeTerminalSession: vi.fn().mockResolvedValue(undefined),
  getTerminalRuntimeStatus: vi.fn().mockResolvedValue({ sessions: [] }),
}))

class FakeWebSocket {
  static readonly CONNECTING = 0
  static readonly OPEN = 1
  static readonly CLOSING = 2
  static readonly CLOSED = 3
  static instances: FakeWebSocket[] = []

  readonly url: string
  binaryType: BinaryType = 'blob'
  readyState = FakeWebSocket.CONNECTING
  onopen: ((event: Event) => void) | null = null
  onmessage: ((event: MessageEvent) => void) | null = null
  onerror: ((event: Event) => void) | null = null
  onclose: ((event: CloseEvent) => void) | null = null
  readonly sent: Array<string | Uint8Array> = []
  closeCalls = 0

  constructor(url: string) {
    this.url = url
    FakeWebSocket.instances.push(this)
  }

  send(data: string | Uint8Array) {
    this.sent.push(data)
  }

  close() {
    this.closeCalls += 1
    this.readyState = FakeWebSocket.CLOSED
  }
}

describe('TerminalConnection', () => {
  beforeEach(() => {
    FakeWebSocket.instances = []
    vi.stubGlobal('WebSocket', FakeWebSocket)
    window.localStorage.clear()
  })

  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('detaches socket handlers on close so late transport events cannot fire callbacks', () => {
    const onOutput = vi.fn()
    const onControl = vi.fn()
    const onClosed = vi.fn()

    const connection = new TerminalConnection('ws://127.0.0.1:7789/api/terminal/sessions/s1/attach', {
      onOutput,
      onControl,
      onClosed,
    })
    const socket = FakeWebSocket.instances[0]
    expect(socket).toBeDefined()
    const staleOnMessage = socket.onmessage
    const staleOnClose = socket.onclose

    connection.close()

    expect(socket.onopen).toBeNull()
    expect(socket.onmessage).toBeNull()
    expect(socket.onerror).toBeNull()
    expect(socket.onclose).toBeNull()
    expect(socket.closeCalls).toBe(1)
    expect(onClosed).toHaveBeenCalledTimes(1)
    expect(onClosed).toHaveBeenCalledWith('local')

    expect(staleOnMessage).not.toBeNull()
    expect(staleOnClose).not.toBeNull()
  })
})

describe('useTerminalSessionLifecycle', () => {
  beforeEach(() => {
    window.localStorage.clear()
  })

  it('persists terminalSessionId when bindTerminalSession resolves after AgentChat unmounts', () => {
    const initialWorkbench: AgentChatWorkbenchState = {
      activeProjectId: 'proj-1',
      projects: {
        'proj-1': {
          ...emptyProjectTabState(),
          tabs: [
            {
              id: 'term-tab-1',
              kind: 'terminal',
              projectId: 'proj-1',
              workspace: '/books/proj-1',
              group: 'primary',
              profileId: 'shell',
              title: '',
            },
          ],
          activeTabIds: { primary: 'term-tab-1', secondary: null },
        },
      },
    }
    const setWorkbench = vi.fn()

    const { result, unmount } = renderHook(() =>
      useTerminalSessionLifecycle(initialWorkbench, false, setWorkbench),
    )

    unmount()

    let accepted = false
    act(() => {
      accepted = result.current.bindTerminalSession('proj-1', 'term-tab-1', {
        id: 'pty-session-42',
        project_id: 'proj-1',
        owner_tab_id: 'term-tab-1',
        profile_id: 'shell',
        title: 'pwsh',
        command: 'pwsh',
        args: [],
        cwd: '/books/proj-1',
        workspace: '/books/proj-1',
        cols: 80,
        rows: 24,
        attached: 0,
        exited: false,
        exit_code: 0,
        created_at: '2026-11-26T00:00:00Z',
        token: 'tok-1',
      })
    })

    expect(accepted).toBe(true)
    expect(setWorkbench).not.toHaveBeenCalled()
    const persisted = readStoredWorkbenchState()
    const persistedTab = persisted.projects['proj-1']?.tabs[0]
    expect(persistedTab).toMatchObject({
      id: 'term-tab-1',
      kind: 'terminal',
      terminalSessionId: 'pty-session-42',
      title: 'pwsh',
    })
  })
})
