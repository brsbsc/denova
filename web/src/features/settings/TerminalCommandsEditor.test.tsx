import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { setConfiguredLocale } from '@/i18n'
import { TerminalCommandsEditor, terminalCommandsForEditor } from './TerminalCommandsEditor'
import type { TerminalCommandSettings } from './types'

beforeEach(() => {
  setConfiguredLocale('zh-CN')
})

describe('TerminalCommandsEditor', () => {
  it('marks codex, claude, and antigravity as preset shortcuts while keeping user entries custom', () => {
    const commands: TerminalCommandSettings[] = [
      { id: 'codex', name: 'Codex CLI', command: 'codex', enabled: true },
      { id: 'claude', name: 'Claude Code', command: 'claude', enabled: true },
      { id: 'antigravity', name: 'Antigravity CLI', command: 'agy', enabled: true },
      { id: 'cli-custom', name: 'Aider', command: 'aider', enabled: false },
    ]

    render(<TerminalCommandsEditor commands={commands} onChange={vi.fn()} />)
    const cards = screen.getAllByTestId('terminal-command-card')
    expect(cards).toHaveLength(4)
    expect(within(cards[0]).getByText('预置')).toBeVisible()
    expect(within(cards[1]).getByText('预置')).toBeVisible()
    expect(within(cards[2]).getByText('预置')).toBeVisible()
    expect(within(cards[2]).getByText('$ agy')).toBeVisible()
    expect(within(cards[3]).getByText('自定义')).toBeVisible()
  })

  it('toggles, reorders, adds, and removes commands through onChange', async () => {
    const user = userEvent.setup()
    const commands: TerminalCommandSettings[] = [
      { id: 'codex', name: 'Codex CLI', command: 'codex', enabled: true },
      { id: 'antigravity', name: 'Antigravity CLI', command: 'agy', enabled: true },
    ]
    const onChange = vi.fn()

    render(<TerminalCommandsEditor commands={commands} onChange={onChange} />)

    await user.click(screen.getByRole('switch', { name: '启用或停用“Antigravity CLI”' }))
    expect(onChange).toHaveBeenLastCalledWith([
      commands[0],
      { ...commands[1], enabled: false },
    ])

    await user.click(screen.getByRole('button', { name: '上移“Antigravity CLI”' }))
    expect(onChange).toHaveBeenLastCalledWith([commands[1], commands[0]])

    await user.click(screen.getByRole('button', { name: '添加 CLI 快捷命令' }))
    expect(onChange).toHaveBeenLastCalledWith([
      ...commands,
      expect.objectContaining({ name: '新 CLI', command: 'my-cli', enabled: false }),
    ])

    await user.click(screen.getByRole('button', { name: '删除“Codex CLI”' }))
    expect(onChange).toHaveBeenLastCalledWith([commands[1]])
  })

  it('resolves draft commands over effective defaults without sharing object references', () => {
    const effective = [{ id: 'antigravity', name: 'Antigravity CLI', command: 'agy', enabled: true }]
    const resolved = terminalCommandsForEditor({}, { terminal_commands: effective })
    expect(resolved).toEqual(effective)
    expect(resolved[0]).not.toBe(effective[0])
  })
})
