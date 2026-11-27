import { render } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { AgentUIMessage } from '@/lib/agent-ui'
import { MessageList } from './AgentMessageList'

const virtuosoRenderSpy = vi.fn()

vi.mock('react-virtuoso', () => ({
  Virtuoso: (props: { data?: Array<{ kind?: string; content?: string }>; totalCount?: number }) => {
    virtuosoRenderSpy({
      dataLength: props.data?.length,
      totalCount: props.totalCount,
      hasData: 'data' in props && props.data !== undefined,
      itemKinds: props.data?.map((item) => item.kind),
      activityContents: props.data?.filter((item) => item.kind === 'activity').map((item) => item.content),
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
    expect(virtuosoRenderSpy).toHaveBeenLastCalledWith(expect.objectContaining({
      dataLength: 2,
      totalCount: undefined,
      hasData: true,
    }))
  })

  it('clears transient activity rows on hide so hidden surfaces never retain stale status text', () => {
    const { rerender } = render(
      <MessageList
        messages={[]}
        isStreaming={true}
        visible={true}
        activityContent="Recovering active run..."
        scrollResetKey="session-2"
      />,
    )
    expect(virtuosoRenderSpy).toHaveBeenLastCalledWith(expect.objectContaining({
      itemKinds: ['activity'],
      activityContents: ['Recovering active run...'],
    }))

    rerender(
      <MessageList
        messages={[]}
        isStreaming={true}
        visible={false}
        activityContent="Recovering active run..."
        scrollResetKey="session-2"
      />,
    )
    expect(virtuosoRenderSpy).toHaveBeenLastCalledWith(expect.objectContaining({
      activityContents: [],
    }))
    const rendersOnHide = virtuosoRenderSpy.mock.calls.length

    rerender(
      <MessageList
        messages={[]}
        isStreaming={false}
        visible={false}
        activityContent=""
        scrollResetKey="session-2"
      />,
    )
    expect(virtuosoRenderSpy).toHaveBeenCalledTimes(rendersOnHide)

    rerender(
      <MessageList
        messages={[]}
        isStreaming={true}
        visible={true}
        activityContent="Thinking..."
        scrollResetKey="session-2"
      />,
    )
    expect(virtuosoRenderSpy).toHaveBeenLastCalledWith(expect.objectContaining({
      itemKinds: ['activity'],
      activityContents: ['Thinking...'],
    }))
  })
})
