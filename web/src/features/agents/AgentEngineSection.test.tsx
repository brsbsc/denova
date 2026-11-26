import { useState } from 'react'
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { setConfiguredLocale } from '@/i18n'
import { APIError } from '@/lib/api-client'
import type { AgentEngineID, RuntimePreferences } from '@/features/agent-runtime/types'
import { AgentEngineSection } from './AgentEngineSection'

const mocks = vi.hoisted(() => ({
  fetchAgentEngines: vi.fn(),
  fetchEngineModels: vi.fn(),
  checkAgentEngine: vi.fn(),
  checkEngineCLIUpdate: vi.fn(),
  installEngineCLI: vi.fn(),
  fetchEngineCLIStatus: vi.fn(),
  startEngineAuth: vi.fn(),
  fetchEngineAuthSession: vi.fn(),
  submitEngineAuth: vi.fn(),
  cancelEngineAuth: vi.fn(),
  logoutEngine: vi.fn(),
  profiles: [] as { id: string; label: string; modelLabel: string }[],
}))
vi.mock('@/features/agent-runtime/api', () => mocks)
vi.mock('@/features/agent-runtime/api-profiles', () => ({ useRuntimeProfiles: () => ({ profiles: mocks.profiles, failed: false }) }))

beforeEach(() => {
  setConfiguredLocale('zh-CN')
  vi.clearAllMocks()
})

afterEach(() => {
  vi.useRealTimers()
})

function Harness({ engine, onChange }: { engine: AgentEngineID; onChange: (value: RuntimePreferences) => void }) {
  const [value, setValue] = useState<RuntimePreferences>({ selected: engine })
  return <AgentEngineSection value={value} inherited={{ [engine]: { model: 'cli-model', effort: 'high', ...(engine === 'codex' ? { sandbox: 'read-only' } : {}) } }}
    onChange={next => { onChange(next); setValue(next) }} beforeSwitch={async () => {}} onCatalog={() => {}} />
}

describe('AgentEngineSection model source', () => {
  for (const engine of ['codex', 'claude'] as const) {
    it(`switches ${engine} inherited CLI settings to a Denova profile and back atomically`, async () => {
      mocks.fetchAgentEngines.mockResolvedValue({ items: [{ id: engine, name_key: `agentRuntime.${engine}`, status: 'ready' }] })
      mocks.fetchEngineModels.mockResolvedValue({ items: [{ id: 'cli-model', display_name: 'CLI model', efforts: ['high'] }], default_id: 'cli-model' })
      mocks.profiles = [{ id: 'profile:gateway', label: 'Gateway', modelLabel: 'Gateway' }]
      const onChange = vi.fn()
      render(<Harness engine={engine} onChange={onChange} />)
      await userEvent.click(screen.getByRole('radio', { name: 'Denova 模型' }))
      expect(onChange).toHaveBeenLastCalledWith({ selected: engine, [engine]: { profile_id: 'gateway', ...(engine === 'codex' ? { sandbox: 'read-only' } : {}) } })
      expect(screen.queryByRole('combobox', { name: '推理强度' })).not.toBeInTheDocument()
      await waitFor(() => expect(screen.getByRole('radio', { name: 'CLI 模型' })).toBeEnabled())
      await userEvent.click(screen.getByRole('radio', { name: 'CLI 模型' }))
      expect(onChange).toHaveBeenLastCalledWith({ selected: engine, [engine]: { model: 'cli-model', ...(engine === 'codex' ? { sandbox: 'read-only' } : {}) } })
    })
  }

  it('explains the required protocol when no compatible Denova model exists', async () => {
    mocks.fetchAgentEngines.mockResolvedValue({ items: [{ id: 'claude', name_key: 'agentRuntime.claude', status: 'auth_required' }] })
    mocks.profiles = []
    const onChange = vi.fn()
    render(<Harness engine="claude" onChange={onChange} />)
    expect(screen.getByRole('radio', { name: 'Denova 模型' })).toBeDisabled()
    expect(screen.getByText(/请在设置 → 模型中添加使用 Anthropic Messages/)).toBeVisible()
    expect(onChange).not.toHaveBeenCalled()
    await screen.findByText('需要登录')
  })

  for (const engine of ['grok', 'antigravity'] as const) {
    it(`selects ${engine} CLI models without offering API profiles or unsupported effort`, async () => {
      mocks.fetchAgentEngines.mockResolvedValue({ items: [{ id: engine, name_key: `agentRuntime.${engine}`, status: 'ready' }] })
      mocks.fetchEngineModels.mockResolvedValue({ items: [
        { id: 'cli-model', display_name: 'CLI model', efforts: [] },
        { id: 'second-model', display_name: 'Second CLI model', efforts: [] },
      ] })
      mocks.profiles = [{ id: 'profile:gateway', label: 'Gateway', modelLabel: 'Gateway' }]
      const onChange = vi.fn()
      render(<Harness engine={engine} onChange={onChange} />)
      await waitFor(() => expect(screen.getByRole('combobox', { name: '引擎模型' })).toBeEnabled())
      expect(screen.queryByRole('radio', { name: 'Denova 模型' })).not.toBeInTheDocument()
      expect(screen.queryByRole('combobox', { name: '推理强度' })).not.toBeInTheDocument()
      await userEvent.click(screen.getByRole('combobox', { name: '引擎模型' }))
      await userEvent.click(await screen.findByRole('option', { name: 'Second CLI model' }))
      expect(onChange).toHaveBeenLastCalledWith({ selected: engine, [engine]: { model: 'second-model' } })
    })
  }
})

describe('AgentEngineSection managed CLI and web OAuth', () => {
  it('hides managed CLI and web auth controls when engine is unmanaged', async () => {
    mocks.fetchAgentEngines.mockResolvedValue({
      items: [{ id: 'codex', name_key: 'agentRuntime.codex', status: 'auth_required', auth_supported: false }],
    })
    mocks.profiles = []
    render(<Harness engine="codex" onChange={vi.fn()} />)
    await screen.findByText('需要登录')
    expect(screen.queryByRole('button', { name: '安装' })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: '检查更新' })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: '登录' })).not.toBeInTheDocument()
    expect(screen.getByText(/打开终端，执行 codex login/)).toBeVisible()
  })

  it('installs a missing managed CLI, polls download progress, and refreshes into sign-in ready state', async () => {
    vi.useFakeTimers()
    const notInstalledEngine = {
      id: 'codex' as const,
      name_key: 'agentRuntime.codex',
      status: 'not_installed' as const,
      auth_supported: true,
      managed: {
        managed: true,
        installed: false,
        update_available: false,
        state: 'idle' as const,
      },
    }
    const installedEngine = {
      ...notInstalledEngine,
      status: 'auth_required' as const,
      version: '0.132.0',
      managed: {
        managed: true,
        installed: true,
        installed_version: '0.132.0',
        update_available: false,
        state: 'idle' as const,
      },
    }
    mocks.fetchAgentEngines
      .mockResolvedValueOnce({ items: [notInstalledEngine] })
      .mockResolvedValueOnce({ items: [installedEngine] })
    mocks.installEngineCLI.mockResolvedValueOnce({
      managed: true,
      installed: false,
      update_available: false,
      state: 'installing',
      bytes_downloaded: 0,
      total_bytes: 100 * 1024 * 1024,
    })
    mocks.fetchEngineCLIStatus
      .mockResolvedValueOnce({
        managed: {
          managed: true,
          installed: false,
          update_available: false,
          state: 'installing',
          bytes_downloaded: 45 * 1024 * 1024,
          total_bytes: 100 * 1024 * 1024,
        },
      })
      .mockResolvedValueOnce({
        managed: installedEngine.managed,
        engine: installedEngine,
      })

    render(<Harness engine="codex" onChange={vi.fn()} />)
    await act(async () => { await Promise.resolve() })

    expect(screen.queryByRole('button', { name: '登录' })).not.toBeInTheDocument()
    const installButton = screen.getByRole('button', { name: '安装' })
    await act(async () => {
      fireEvent.click(installButton)
      await Promise.resolve()
    })

    expect(mocks.installEngineCLI).toHaveBeenCalledWith('codex')
    expect(screen.getByText('正在下载并安装…')).toBeVisible()

    await act(async () => {
      await vi.advanceTimersByTimeAsync(1500)
    })
    expect(screen.getByText('45%')).toBeVisible()
    expect(screen.getByText('45.0 MB / 100.0 MB')).toBeVisible()

    await act(async () => {
      await vi.advanceTimersByTimeAsync(1500)
    })
    expect(mocks.fetchAgentEngines).toHaveBeenCalledTimes(2)
    expect(screen.getByText('v0.132.0')).toBeVisible()
    expect(screen.getByRole('button', { name: '登录' })).toBeVisible()
  })

  it('checks for updates, shows update button and pending reload hint', async () => {
    const user = userEvent.setup()
    mocks.fetchAgentEngines.mockResolvedValue({
      items: [{
        id: 'antigravity',
        name_key: 'agentRuntime.antigravity',
        status: 'ready',
        version: '1.5.16',
        auth_supported: true,
        managed: {
          managed: true,
          installed: true,
          installed_version: '1.5.16',
          update_available: false,
          state: 'idle',
          pending_reload: true,
        },
      }],
    })
    mocks.fetchEngineModels.mockResolvedValue({ items: [{ id: 'cli-model', display_name: 'CLI model', efforts: [] }] })
    mocks.checkEngineCLIUpdate.mockResolvedValueOnce({
      managed: true,
      installed: true,
      installed_version: '1.5.16',
      latest_version: '1.5.18',
      update_available: true,
      state: 'idle',
      pending_reload: true,
      last_checked_at: '2026-11-26T14:00:00Z',
    })

    render(<Harness engine="antigravity" onChange={vi.fn()} />)
    expect(await screen.findByText('v1.5.16')).toBeVisible()
    expect(screen.getByText('新版本已安装，当前回合结束后自动生效。')).toBeVisible()

    await user.click(screen.getByRole('button', { name: '检查更新' }))
    expect(mocks.checkEngineCLIUpdate).toHaveBeenCalledWith('antigravity')
    expect(await screen.findByRole('button', { name: '更新至 v1.5.18' })).toBeVisible()
  })

  it('shows sign-out when ready and sign-in when auth_required, hiding auth buttons for API profiles', async () => {
    const user = userEvent.setup()
    mocks.fetchAgentEngines.mockResolvedValue({
      items: [{
        id: 'claude',
        name_key: 'agentRuntime.claude',
        status: 'ready',
        auth_supported: true,
        managed: { managed: true, installed: true, installed_version: '2.1.320', update_available: false, state: 'idle' },
      }],
    })
    mocks.fetchEngineModels.mockResolvedValue({ items: [{ id: 'cli-model', display_name: 'CLI model', efforts: [] }], default_id: 'cli-model' })
    mocks.profiles = [{ id: 'profile:gateway', label: 'Gateway', modelLabel: 'Gateway' }]
    mocks.logoutEngine.mockResolvedValueOnce({
      id: 'claude',
      name_key: 'agentRuntime.claude',
      status: 'auth_required',
      auth_supported: true,
      managed: { managed: true, installed: true, installed_version: '2.1.320', update_available: false, state: 'idle' },
    })

    render(<Harness engine="claude" onChange={vi.fn()} />)
    const signOutButton = await screen.findByRole('button', { name: '退出登录' })
    await user.click(signOutButton)

    expect(mocks.logoutEngine).toHaveBeenCalledWith('claude')
    expect(await screen.findByRole('button', { name: '登录' })).toBeVisible()

    // Switching to Denova API profile hides CLI web sign-in controls.
    await user.click(screen.getByRole('radio', { name: 'Denova 模型' }))
    expect(screen.queryByRole('button', { name: '登录' })).not.toBeInTheDocument()
  })

  it('localizes APIError.code on update check failure and clears stale live state on subsequent refresh', async () => {
    const user = userEvent.setup()
    const baseEngine = {
      id: 'codex' as const,
      name_key: 'agentRuntime.codex',
      status: 'ready' as const,
      version: '0.132.0',
      auth_supported: true,
      managed: {
        managed: true,
        installed: true,
        installed_version: '0.132.0',
        update_available: false,
        pending_reload: true,
        state: 'idle' as const,
      },
    }
    mocks.fetchAgentEngines
      .mockResolvedValueOnce({ items: [baseEngine] })
      .mockResolvedValueOnce({
        items: [{
          ...baseEngine,
          managed: { ...baseEngine.managed, pending_reload: false },
        }],
      })
    mocks.fetchEngineModels.mockResolvedValue({ items: [{ id: 'cli-model', display_name: 'CLI model', efforts: ['high'] }], default_id: 'cli-model' })
    mocks.checkEngineCLIUpdate
      .mockRejectedValueOnce(new APIError('raw server text', { status: 409, code: 'agentRuntime.updateBusy' }))
      .mockResolvedValueOnce({
        ...baseEngine.managed,
        last_checked_at: '2026-11-26T14:00:00Z',
      })
    mocks.checkAgentEngine.mockResolvedValueOnce({
      ...baseEngine,
      managed: { ...baseEngine.managed, pending_reload: false },
    })

    render(<Harness engine="codex" onChange={vi.fn()} />)
    expect(await screen.findByText('新版本已安装，当前回合结束后自动生效。')).toBeVisible()

    // Error code should resolve via i18n instead of showing raw fallback English.
    await user.click(screen.getByRole('button', { name: '检查更新' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('CLI 安装或更新正在进行中，请稍候。')

    // Successful check update must not leave sticky live state masking parent refreshes.
    await user.click(screen.getByRole('button', { name: '检查更新' }))
    expect(await screen.findByText('已是最新')).toBeVisible()

    // Clicking runtime check updates parent engine.managed (pending_reload -> false).
    await user.click(screen.getByRole('button', { name: '检查连接' }))
    await waitFor(() => {
      expect(screen.queryByText('新版本已安装，当前回合结束后自动生效。')).not.toBeInTheDocument()
    })
  })

  it('localizes APIError.code cleanly on sign-out failure without diagnostic suffix', async () => {
    const user = userEvent.setup()
    mocks.fetchAgentEngines.mockResolvedValue({
      items: [{
        id: 'codex',
        name_key: 'agentRuntime.codex',
        status: 'ready',
        auth_supported: true,
        managed: { managed: true, installed: true, installed_version: '0.135.0', update_available: false, state: 'idle' },
      }],
    })
    mocks.fetchEngineModels.mockResolvedValue({ items: [{ id: 'cli-model', display_name: 'CLI model', efforts: ['high'] }], default_id: 'cli-model' })
    mocks.logoutEngine.mockRejectedValueOnce(new APIError('raw server text', { status: 409, code: 'agentRuntime.auth.busy' }))

    render(<Harness engine="codex" onChange={vi.fn()} />)
    await user.click(await screen.findByRole('button', { name: '退出登录' }))

    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent('当前有正在进行的对话，请稍后再退出登录。')
    expect(alert.textContent).not.toContain('409')
  })
})
