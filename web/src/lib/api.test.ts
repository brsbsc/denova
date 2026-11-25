import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import {
  answerSessionAsk,
  cancelSessionAsk,
  createSession,
  deleteSession,
  executeCommand,
  getActiveChatTask,
  getMessagesPage,
  getSessions,
  projectSkillTarget,
  renameSession,
  recoverChatAgentRuntime,
  saveSkillDocument,
  switchSession,
} from './api'
import { apiRoute, installApiMock, jsonResponse } from '@/test/api-mock'

let apiMock: ReturnType<typeof installApiMock>

describe('api', () => {
  beforeEach(() => {
    apiMock = installApiMock(
      apiRoute.get('/api/session/messages', () => jsonResponse([])),
      apiRoute.get('/api/sessions', () => jsonResponse({ sessions: [] })),
      apiRoute.get('/api/chat/active', () => jsonResponse({ active: false })),
      apiRoute.post('/api/command', async ({ request }) => {
        const body = await request.json() as { command?: string }
        return jsonResponse({ result: `executed:${body.command || ''}` })
      }),
    )
  })

  afterEach(() => vi.unstubAllGlobals())

  it('answers and cancels only the exact session Ask identity', async () => {
    const requests: Array<{ path: string; body: unknown }> = []
    apiMock.use(
      apiRoute.post('/api/session/asks/:askID/answer', async ({ request }) => {
        requests.push({ path: new URL(request.url).pathname, body: await request.json() })
        return jsonResponse({ schema: 'ask.result.v1', id: 'ask/1', status: 'answered' })
      }),
      apiRoute.post('/api/session/asks/:askID/cancel', async ({ request }) => {
        requests.push({ path: new URL(request.url).pathname, body: await request.json() })
        return jsonResponse({ schema: 'ask.result.v1', id: 'ask/1', status: 'cancelled' })
      }),
    )

    await answerSessionAsk('session-a', 'ask/1', [{ question_id: 'q1', selected_option_ids: ['safe'] }])
    await cancelSessionAsk('session-a', 'ask/1')

    expect(requests).toEqual([
      { path: '/api/session/asks/ask%2F1/answer', body: { session_id: 'session-a', answers: [{ question_id: 'q1', selected_option_ids: ['safe'] }] } },
      { path: '/api/session/asks/ask%2F1/cancel', body: { session_id: 'session-a', reason: 'user_cancelled' } },
    ])
  })

  it('通过 MSW 获取会话和活跃任务', async () => {
    await expect(getMessagesPage()).resolves.toMatchObject({ messages: [] })
    await expect(getSessions()).resolves.toEqual([])
    await expect(getActiveChatTask('session-a')).resolves.toEqual({ active: false })
  })

  it('恢复已接受运行时只回传服务器投影的 identity', async () => {
    let requestBody: unknown
    apiMock.use(
      apiRoute.post('/api/chat/recovery', async ({ request }) => {
        requestBody = await request.json()
        return jsonResponse({
          task_id: 'recovery-task-1',
          status: 'running',
          stream_cursor: 0,
          cursor: 7,
          recovery_action: {
            kind: 'follow_up',
            command_id: 'follow-command-1',
            operation_id: 'operation-1',
          },
        }, { status: 202 })
      }),
    )
    const action = {
      kind: 'follow_up' as const,
      command_id: 'follow-command-1',
      operation_id: 'operation-1',
    }

    await expect(recoverChatAgentRuntime(action, 'session-a')).resolves.toMatchObject({ task_id: 'recovery-task-1' })
    expect(requestBody).toEqual({ session_id: 'session-a', action })
    expect(JSON.stringify(requestBody)).not.toContain('message')
  })

  it('覆盖会话 CRUD、切换和指定会话消息读取', async () => {
    const requests: Array<{ path: string; body?: unknown }> = []
    apiMock.use(
      apiRoute.get('/api/session/messages', ({ request }) => {
        requests.push({
          path: new URL(request.url).pathname + new URL(request.url).search,
        })
        return jsonResponse([
          {
            id: 'message-1',
            role: 'user',
            parts: [{ type: 'text', text: '会话消息' }],
          },
        ])
      }),
      apiRoute.get('/api/sessions', () =>
        jsonResponse({
          sessions: [
            {
              id: 'session-a',
              title: '会话 A',
              active: true,
              message_count: 1,
              created_at: '',
              updated_at: '',
            },
          ],
        }),
      ),
      apiRoute.post('/api/sessions', async ({ request }) => {
        const body = await request.json()
        requests.push({ path: '/api/sessions', body })
        return jsonResponse({
          id: 'session-b',
          title: '会话 B',
          active: true,
          message_count: 0,
          created_at: '',
          updated_at: '',
        })
      }),
      apiRoute.post('/api/sessions/switch', async ({ request }) => {
        const body = await request.json()
        requests.push({ path: '/api/sessions/switch', body })
        return jsonResponse({
          id: 'session-a',
          title: '会话 A',
          active: true,
          message_count: 1,
          created_at: '',
          updated_at: '',
        })
      }),
      apiRoute.post('/api/sessions/rename', async ({ request }) => {
        const body = await request.json()
        requests.push({ path: '/api/sessions/rename', body })
        return jsonResponse({ status: 'ok' })
      }),
      apiRoute.post('/api/sessions/delete', async ({ request }) => {
        const body = await request.json()
        requests.push({ path: '/api/sessions/delete', body })
        return jsonResponse({
          id: 'session-a',
          title: '会话 A',
          active: true,
          message_count: 1,
          created_at: '',
          updated_at: '',
        })
      }),
    )

    await expect(getSessions()).resolves.toHaveLength(1)
    await expect(createSession('会话 B')).resolves.toMatchObject({
      id: 'session-b',
      active: true,
    })
    await expect(switchSession('session-a')).resolves.toMatchObject({
      id: 'session-a',
    })
    await expect(renameSession('session-a', '新标题')).resolves.toBeUndefined()
    await expect(deleteSession('session-b')).resolves.toMatchObject({
      id: 'session-a',
    })
    await expect(getMessagesPage('session-a')).resolves.toMatchObject({
      messages: [
        {
          id: 'message-1',
          role: 'user',
          parts: [{ type: 'text', text: '会话消息' }],
        },
      ],
    })

    expect(requests).toEqual([
      { path: '/api/sessions', body: { title: '会话 B' } },
      { path: '/api/sessions/switch', body: { id: 'session-a' } },
      {
        path: '/api/sessions/rename',
        body: { id: 'session-a', title: '新标题' },
      },
      { path: '/api/sessions/delete', body: { id: 'session-b' } },
      { path: '/api/session/messages?session_id=session-a&limit=100' },
    ])
  })

  it('从最新消息向前分页读取会话展示历史', async () => {
    let requestPath = ''
    apiMock.use(
      apiRoute.get('/api/session/messages', ({ request }) => {
        requestPath = new URL(request.url).pathname + new URL(request.url).search
        return jsonResponse({
          messages: [
            {
              id: 'message-older',
              role: 'user',
              parts: [{ type: 'text', text: '更早消息' }],
            },
          ],
          page: { next_before: '25', has_more: true, total: 125 },
        })
      }),
    )

    await expect(getMessagesPage('session-ui', { limit: 50, before: '75' })).resolves.toEqual({
      messages: [
        {
          id: 'message-older',
          role: 'user',
          parts: [{ type: 'text', text: '更早消息' }],
        },
      ],
      nextBefore: '25',
      hasMore: true,
      total: 125,
    })
    expect(requestPath).toBe('/api/session/messages?session_id=session-ui&limit=50&before=75')
  })

  it('发送命令时返回后端结果', async () => {
    await expect(executeCommand('status')).resolves.toBe('executed:status')
  })

  it('保存 Skill 配置时可提交目标 scope、名称和基础 revision', async () => {
    let requestBody: unknown
    apiMock.use(
      apiRoute.put('/api/projects/project-demo/skills/document', async ({ request }) => {
        requestBody = await request.json()
        return jsonResponse({
          name: 'beat-plan',
          description: 'Beat planning',
          scope: 'workspace',
          path: '/books/demo/.nova/skills/beat-plan/SKILL.md',
          editable: true,
          active: true,
          content: '---\nname: beat-plan\ndescription: Beat planning\n---\n',
          revision: 'skill-r2',
        })
      }),
    )

    await expect(saveSkillDocument(projectSkillTarget('project-demo'), 'user', 'draft-plan', 'content', { scope: 'workspace', name: 'beat-plan' }, 'skill-r1')).resolves.toMatchObject({
      scope: 'workspace',
      name: 'beat-plan',
    })
    expect(requestBody).toEqual({
      scope: 'user',
      name: 'draft-plan',
      content: 'content',
      target_scope: 'workspace',
      target_name: 'beat-plan',
      base_revision: 'skill-r1',
    })
  })
})
