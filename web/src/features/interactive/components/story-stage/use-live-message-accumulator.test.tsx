import { act, renderHook } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import type { AgentUIMessage } from '@/lib/agent-ui'
import * as liveStreamMessages from './live-stream-messages'
import { useLiveMessageAccumulator } from './use-live-message-accumulator'

describe('useLiveMessageAccumulator', () => {
  it('coalesces consecutive assistant, reasoning, and tool-arg deltas before applying to state', () => {
    let messages: AgentUIMessage[] = []
    const setMessages = vi.fn((update: AgentUIMessage[] | ((current: AgentUIMessage[]) => AgentUIMessage[])) => {
      messages = typeof update === 'function' ? update(messages) : update
    })
    const appendSpy = vi.spyOn(liveStreamMessages, 'appendBufferedLiveMessage')
    const updateToolSpy = vi.spyOn(liveStreamMessages, 'updateToolMessageInput')
    try {
      const { result } = renderHook(() => useLiveMessageAccumulator({ setMessages }))

      act(() => {
        for (let i = 0; i < 50; i += 1) {
          result.current.appendThinking('t')
        }
        result.current.flush()
      })
      expect(appendSpy).toHaveBeenCalledTimes(1)
      expect(messages.at(-1)?.parts[0]).toMatchObject({ type: 'reasoning', text: 't'.repeat(50) })

      act(() => {
        result.current.appendToolCall({ id: 'call-1', name: 'write_file', args: '' })
        for (let i = 0; i < 50; i += 1) {
          result.current.appendToolArgs({ id: 'call-1', name: 'write_file', delta: 'x' })
        }
        result.current.flush()
      })
      expect(updateToolSpy).toHaveBeenCalledTimes(1)
      expect(liveStreamMessages.toolMessageInput(messages.at(-1)!)).toBe('x'.repeat(50))

      appendSpy.mockClear()
      act(() => {
        for (let i = 0; i < 50; i += 1) {
          result.current.appendAssistant('p', 'turn-1')
        }
        result.current.flush()
      })
      expect(appendSpy).toHaveBeenCalledTimes(1)
      expect(messages.at(-1)?.parts[0]).toMatchObject({ type: 'text', text: 'p'.repeat(50) })
    } finally {
      appendSpy.mockRestore()
      updateToolSpy.mockRestore()
    }
  })

  it('preserves ordering for interleaved reasoning, collapse, subagents, assistant prose, and replacing tool args in one batch', () => {
    let messages: AgentUIMessage[] = []
    const setMessages = vi.fn((update: AgentUIMessage[] | ((current: AgentUIMessage[]) => AgentUIMessage[])) => {
      messages = typeof update === 'function' ? update(messages) : update
    })
    const { result } = renderHook(() => useLiveMessageAccumulator({ setMessages }))

    act(() => {
      result.current.appendToolCall({ id: 'call-1', name: 'write_file', args: '' })
      result.current.appendToolArgs({ id: 'call-1', name: 'write_file', delta: '{"a":' })
      result.current.appendToolArgs({ id: 'call-1', name: 'write_file', delta: '1}' })
      result.current.appendToolArgs({ id: 'call-1', name: 'write_file', args: '{"final":true}' })
      result.current.appendToolArgs({ id: 'call-1', name: 'write_file', delta: '!' })
      result.current.appendThinking('plan-1')
      result.current.appendThinking('-2')
      result.current.appendThinking('sub-a', { subagent: true, subagent_session_id: 'sub-a' })
      result.current.appendThinking('-more', { subagent: true, subagent_session_id: 'sub-a' })
      result.current.appendThinking('sub-b', { subagent: true, subagent_session_id: 'sub-b' })
      result.current.collapseNonNarrative()
      result.current.appendAssistant('Hello ', 'turn-1')
      result.current.appendAssistant('world', 'turn-1')
      result.current.flush()
    })

    expect(liveStreamMessages.toolMessageInput(messages[0])).toBe('{"final":true}!')
    expect(messages[1].parts[0]).toMatchObject({ type: 'reasoning', text: 'plan-1-2', state: 'done' })
    expect(messages[2].parts[0]).toMatchObject({ type: 'reasoning', text: 'sub-a-more', state: 'done' })
    expect(messages[2].metadata?.subagent_session_id).toBe('sub-a')
    expect(messages[3].parts[0]).toMatchObject({ type: 'reasoning', text: 'sub-b', state: 'done' })
    expect(messages[3].metadata?.subagent_session_id).toBe('sub-b')
    expect(messages[4].parts[0]).toMatchObject({ type: 'text', text: 'Hello world', state: 'streaming' })
  })
})
