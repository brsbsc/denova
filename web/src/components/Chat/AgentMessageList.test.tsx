import { render } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { AgentUIMessage } from '@/lib/agent-ui'
import { MessageList } from './AgentMessageList'

const virtuosoRenderSpy = vi.fn()

vi.mock('react-virtuoso', () => ({
  Virtuoso: (props: { data?: unknown[]; totalCount?: number }) => {
    virtuosoRenderSpy({
      dataLength: props.data?.length,
      totalCount: props.totalCount,
      hasData: 'data' in props && props.data !== undefined,
    })
    return <div data-testid="mock-virtuoso" />
  },
}))

function userMessage(id: string, text: string): AgentUIMessage {
  return {
    id,
    role: 'user',
    parts: [{ type: 'text', text }],
    metadata: { created_at: '2026-11-27T00:00:00Z' },
  }
}

describe('MessageList visibility gating', () => {
  beforeEach(() => {
    virtuosoRenderSpy.mockReset()
  })

  it('skips Virtuoso re-renders while hidden and reconciles once when revealed', () => {
    const firstMessages = [userMessage('m1', 'hello')]
    const secondMessages = [...firstMessages, userMessage('m2', 'world')]
    const { rerender } = render(
      <MessageList
        messages={firstMessages}
        isStreaming={false}
        visible={false}
        activityContent=""
        scrollResetKey="session-1"
      />,
    )
    expect(virtuosoRenderSpy).toHaveBeenCalledTimes(1)

    rerender(
      <MessageList
        messages={secondMessages}
        isStreaming={true}
        visible={false}
        activityContent="thinking"
        scrollResetKey="session-1"
      />,
    )
    expect(virtuosoRenderSpy).toHaveBeenCalledTimes(1)

    rerender(
      <MessageList
        messages={secondMessages}
        isStreaming={false}
        visible={true}
        activityContent=""
        scrollResetKey="session-1"
      />,
    )
    expect(virtuosoRenderSpy.mock.calls.length).toBeGreaterThanOrEqual(2)
    expect(virtuosoRenderSpy).toHaveBeenLastCalledWith({
      dataLength: 2,
      totalCount: undefined,
      hasData: true,
    })
  })
})
