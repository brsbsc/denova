import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { setConfiguredLocale } from '@/i18n'
import type { AuthSessionSnapshot, EngineDescriptor } from '@/features/agent-runtime/types'
import { AgentEngineAuthDialog } from './AgentEngineAuthDialog'

const mocks = vi.hoisted(() => ({
  startEngineAuth: vi.fn(),
  fetchEngineAuthSession: vi.fn(),
  submitEngineAuth: vi.fn(),
  cancelEngineAuth: vi.fn(),
}))

vi.mock('@/features/agent-runtime/api', () => mocks)

const READY_CODEX: EngineDescriptor = {
  id: 'codex',
  name_key: 'agentRuntime.codex',
  status: 'ready',
  auth_supported: true,
  capabilities: {
    ask_user: true,
    cancel: true,
    interactive_approval: false,
    delegation: false,
    goal: true,
    queue: true,
    steer: false,
    pause: false,
  },
  configuration_sections: [],
}

beforeEach(() => {
  setConfiguredLocale('zh-CN')
  vi.clearAllMocks()
  mocks.cancelEngineAuth.mockResolvedValue({
    session_id: 'cancelled',
    engine: 'codex',
    mode: 'device_code',
    status: 'cancelled',
    expires_at: '2026-11-26T15:00:00Z',
  })
})

afterEach(() => {
  vi.useRealTimers()
})

describe('AgentEngineAuthDialog', () => {
  it('renders Codex device code flow, copies code, polls, and completes sign-in', async () => {
    vi.useFakeTimers()
    const onAuthenticated = vi.fn()
    const onOpenChange = vi.fn()
    const writeText = vi.fn().mockResolvedValue(undefined)
    const originalClipboard = Object.getOwnPropertyDescriptor(navigator, 'clipboard')
    Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true })

    try {
      const initial: AuthSessionSnapshot = {
        session_id: 'sess-codex',
        engine: 'codex',
        mode: 'device_code',
        status: 'awaiting_user',
        auth_url: 'https://auth.openai.com/codex/device',
        user_code: 'ABCD-1234',
        hint_key: 'agentRuntime.auth.codexDeviceHint',
        expires_at: '2026-11-26T15:00:00Z',
      }
      mocks.startEngineAuth.mockResolvedValueOnce(initial)
      mocks.fetchEngineAuthSession.mockResolvedValueOnce({
        ...initial,
        status: 'succeeded',
        engine_state: READY_CODEX,
      })

      render(
        <AgentEngineAuthDialog
          engineId="codex"
          open
          onOpenChange={onOpenChange}
          onAuthenticated={onAuthenticated}
        />,
      )

      await act(async () => { await Promise.resolve() })
      expect(mocks.startEngineAuth).toHaveBeenCalledWith('codex', undefined)
      expect(screen.getByText('ABCD-1234')).toBeVisible()
      expect(screen.getByRole('link', { name: '打开登录链接' })).toHaveAttribute('href', 'https://auth.openai.com/codex/device')
      expect(screen.getByText(/请在 ChatGPT 安全设置中开启 Codex 设备码登录/)).toBeVisible()

      await act(async () => {
        fireEvent.click(screen.getByRole('button', { name: '复制设备码' }))
        await Promise.resolve()
      })
      expect(writeText).toHaveBeenCalledWith('ABCD-1234')

      await act(async () => {
        await vi.advanceTimersByTimeAsync(1500)
      })

      expect(mocks.fetchEngineAuthSession).toHaveBeenCalledWith('codex', 'sess-codex')
      expect(onAuthenticated).toHaveBeenCalledWith(READY_CODEX)
      expect(onOpenChange).toHaveBeenCalledWith(false)
      expect(mocks.cancelEngineAuth).not.toHaveBeenCalled()
    } finally {
      if (originalClipboard) Object.defineProperty(navigator, 'clipboard', originalClipboard)
      else delete (navigator as { clipboard?: unknown }).clipboard
    }
  })

  it('supports Antigravity code paste with validation retry and completion', async () => {
    const user = userEvent.setup()
    const onAuthenticated = vi.fn()
    const onOpenChange = vi.fn()

    const initial: AuthSessionSnapshot = {
      session_id: 'sess-agy',
      engine: 'antigravity',
      mode: 'code_paste',
      status: 'awaiting_user',
      auth_url: 'https://accounts.google.com/o/oauth2/v2/auth?redirect_uri=http%3A%2F%2Flocalhost%3A43210%2Fcallback',
      expires_at: '2026-11-26T15:00:00Z',
    }
    mocks.startEngineAuth.mockResolvedValueOnce(initial)
    mocks.fetchEngineAuthSession.mockResolvedValue(initial)
    mocks.submitEngineAuth
      .mockRejectedValueOnce(new Error('agentRuntime.auth.invalidInput'))
      .mockResolvedValueOnce({
        ...initial,
        status: 'succeeded',
        engine_state: { ...READY_CODEX, id: 'antigravity', name_key: 'agentRuntime.antigravity' },
      })

    render(
      <AgentEngineAuthDialog
        engineId="antigravity"
        open
        onOpenChange={onOpenChange}
        onAuthenticated={onAuthenticated}
      />,
    )

    const link = await screen.findByRole('link', { name: '打开登录链接' })
    expect(link).toHaveAttribute('href', initial.auth_url)

    const input = screen.getByLabelText('授权码或回调链接')
    await user.type(input, 'bad-input')
    await user.click(screen.getByRole('button', { name: '提交' }))

    expect(await screen.findByRole('alert')).toHaveTextContent('授权码或回调链接无效，请检查后重新粘贴。')
    expect(onOpenChange).not.toHaveBeenCalled()

    await user.clear(input)
    await user.type(input, 'http://localhost:43210/callback?code=valid')
    await user.click(screen.getByRole('button', { name: '提交' }))

    await waitFor(() => expect(onAuthenticated).toHaveBeenCalledWith(expect.objectContaining({ id: 'antigravity', status: 'ready' })))
    expect(mocks.submitEngineAuth).toHaveBeenLastCalledWith('antigravity', 'sess-agy', 'http://localhost:43210/callback?code=valid')
    expect(onOpenChange).toHaveBeenCalledWith(false)
  })

  it('lets Claude choose subscription or console method before starting', async () => {
    const user = userEvent.setup()
    const onAuthenticated = vi.fn()
    const onOpenChange = vi.fn()

    mocks.startEngineAuth.mockResolvedValueOnce({
      session_id: 'sess-claude',
      engine: 'claude',
      mode: 'code_paste',
      status: 'awaiting_user',
      auth_url: 'https://console.anthropic.com/oauth/authorize',
      expires_at: '2026-11-26T15:00:00Z',
    })

    render(
      <AgentEngineAuthDialog
        engineId="claude"
        open
        onOpenChange={onOpenChange}
        onAuthenticated={onAuthenticated}
      />,
    )

    expect(mocks.startEngineAuth).not.toHaveBeenCalled()
    expect(screen.getByRole('radio', { name: 'Claude 订阅' })).toHaveAttribute('data-state', 'on')

    await user.click(screen.getByRole('radio', { name: 'Anthropic Console' }))
    await user.click(screen.getByRole('button', { name: '开始登录' }))

    await waitFor(() => expect(mocks.startEngineAuth).toHaveBeenCalledWith('claude', { method: 'console' }))
    expect(await screen.findByRole('link', { name: '打开登录链接' })).toHaveAttribute('href', 'https://console.anthropic.com/oauth/authorize')
  })

  it('cancels an active session when the dialog is closed', async () => {
    const user = userEvent.setup()
    const onOpenChange = vi.fn()

    mocks.startEngineAuth.mockResolvedValueOnce({
      session_id: 'sess-cancel',
      engine: 'codex',
      mode: 'device_code',
      status: 'awaiting_user',
      auth_url: 'https://auth.openai.com/codex/device',
      user_code: 'WXYZ-5678',
      expires_at: '2026-11-26T15:00:00Z',
    })

    render(
      <AgentEngineAuthDialog
        engineId="codex"
        open
        onOpenChange={onOpenChange}
        onAuthenticated={vi.fn()}
      />,
    )

    await screen.findByText('WXYZ-5678')
    await user.click(screen.getByRole('button', { name: '取消' }))

    expect(mocks.cancelEngineAuth).toHaveBeenCalledWith('codex', 'sess-cancel')
    expect(onOpenChange).toHaveBeenCalledWith(false)
  })

  it('shows failure message and retries with a fresh session', async () => {
    const user = userEvent.setup()
    mocks.startEngineAuth
      .mockResolvedValueOnce({
        session_id: 'sess-failed',
        engine: 'codex',
        mode: 'device_code',
        status: 'failed',
        error_key: 'agentRuntime.auth.expired',
        expires_at: '2026-11-26T15:00:00Z',
      })
      .mockResolvedValueOnce({
        session_id: 'sess-retry',
        engine: 'codex',
        mode: 'device_code',
        status: 'awaiting_user',
        auth_url: 'https://auth.openai.com/codex/device',
        user_code: 'NEWC-9999',
        expires_at: '2026-11-26T15:10:00Z',
      })

    render(
      <AgentEngineAuthDialog
        engineId="codex"
        open
        onOpenChange={vi.fn()}
        onAuthenticated={vi.fn()}
      />,
    )

    expect(await screen.findByRole('alert')).toHaveTextContent('登录会话已过期，请重新开始。')
    await user.click(screen.getByRole('button', { name: '重试' }))

    expect(await screen.findByText('NEWC-9999')).toBeVisible()
    expect(mocks.startEngineAuth).toHaveBeenCalledTimes(2)
  })

  it('cancels a late-resolving auth session if the dialog closes before start finishes', async () => {
    let resolveStart!: (snapshot: AuthSessionSnapshot) => void
    mocks.startEngineAuth.mockImplementationOnce(() => new Promise<AuthSessionSnapshot>((resolve) => {
      resolveStart = resolve
    }))

    const { rerender } = render(
      <AgentEngineAuthDialog
        engineId="codex"
        open
        onOpenChange={vi.fn()}
        onAuthenticated={vi.fn()}
      />,
    )

    rerender(
      <AgentEngineAuthDialog
        engineId="codex"
        open={false}
        onOpenChange={vi.fn()}
        onAuthenticated={vi.fn()}
      />,
    )

    await act(async () => {
      resolveStart({
        session_id: 'sess-late',
        engine: 'codex',
        mode: 'device_code',
        status: 'awaiting_user',
        auth_url: 'https://auth.openai.com/codex/device',
        user_code: 'LATE-0001',
        expires_at: '2026-11-26T15:10:00Z',
      })
      await Promise.resolve()
    })

    expect(mocks.cancelEngineAuth).toHaveBeenCalledWith('codex', 'sess-late')
  })
})
