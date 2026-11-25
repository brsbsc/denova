import { describe, expect, it } from 'vitest'
import { parseSSEStream } from './sse'
import type { SSEEvent } from './types'

function streamFromChunks(chunks: string[]): ReadableStream<Uint8Array> {
  const encoder = new TextEncoder()
  return new ReadableStream<Uint8Array>({
    start(controller) {
      for (const chunk of chunks) {
        controller.enqueue(encoder.encode(chunk))
      }
      controller.close()
    },
  })
}

async function collectEvents(stream: ReadableStream<SSEEvent>): Promise<SSEEvent[]> {
  const reader = stream.getReader()
  const events: SSEEvent[] = []
  while (true) {
    const { done, value } = await reader.read()
    if (done) return events
    events.push(value)
  }
}

describe('parseSSEStream', () => {
  it('defaults omitted event names to message while ignoring comment-only blocks', async () => {
    const events = await collectEvents(
      parseSSEStream(
        streamFromChunks([
          ': heartbeat\n\n',
          'id: 1\ndata: hello\ndata: world\n\n',
          'event: custom\ndata: {"ok":true}\r\n\r\n',
          'data:\n\n',
        ]),
      ),
    )

    expect(events).toEqual([
      { id: '1', event: 'message', data: 'hello\nworld' },
      { event: 'custom', data: '{"ok":true}' },
      { event: 'message', data: '' },
    ])
  })
})
