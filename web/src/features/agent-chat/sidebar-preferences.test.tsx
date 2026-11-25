import { act, renderHook } from '@testing-library/react'
import { beforeEach, describe, expect, it } from 'vitest'
import type { AgentChatProject } from './api'
import { useAgentChatSidebarPreferences } from './sidebar-preferences'

const STORAGE_KEY = 'nova.agentchat.sidebarPreferences.v1'

function makeProject(id: string, path = `/books/${id}`): AgentChatProject {
  return {
    id,
    name: id,
    path,
    type: 'book',
    status: 'available',
    current: false,
    total: 0,
    sessions: [],
  }
}

describe('useAgentChatSidebarPreferences', () => {
  beforeEach(() => {
    window.localStorage.clear()
  })

  it('preserves stored project pins, manual order, and open timestamps while projects are still loading', () => {
    const initialStored = {
      sortMode: 'manual',
      pinnedProjects: ['proj-b'],
      manualProjectOrder: ['proj-b', 'proj-a'],
      projectOpenedAt: { 'proj-a': 1700000000000, 'proj-b': 1700000005000 },
    }
    window.localStorage.setItem(STORAGE_KEY, JSON.stringify(initialStored))

    const { result, rerender } = renderHook(
      ({ projects, loaded }: { projects: AgentChatProject[]; loaded: boolean }) =>
        useAgentChatSidebarPreferences(projects, loaded),
      { initialProps: { projects: [] as AgentChatProject[], loaded: false } },
    )

    expect(JSON.parse(window.localStorage.getItem(STORAGE_KEY) || '{}')).toEqual(initialStored)

    rerender({
      projects: [makeProject('proj-a'), makeProject('proj-b'), makeProject('proj-c')],
      loaded: true,
    })

    expect(result.current.isProjectPinned('proj-b')).toBe(true)
    expect(result.current.orderedProjects.map((project) => project.id)).toEqual(['proj-b', 'proj-a', 'proj-c'])
    expect(JSON.parse(window.localStorage.getItem(STORAGE_KEY) || '{}')).toEqual({
      sortMode: 'manual',
      pinnedProjects: ['proj-b'],
      manualProjectOrder: ['proj-b', 'proj-a', 'proj-c'],
      projectOpenedAt: { 'proj-a': 1700000000000, 'proj-b': 1700000005000 },
    })
  })

  it('normalizes legacy path keys and removes deleted projects once loaded', () => {
    window.localStorage.setItem(
      STORAGE_KEY,
      JSON.stringify({
        sortMode: 'opened',
        pinnedProjects: ['/books/alpha', 'deleted-project'],
        manualProjectOrder: ['/books/alpha', 'deleted-project'],
        projectOpenedAt: { '/books/alpha': 100, 'deleted-project': 200 },
      }),
    )

    const { result } = renderHook(() =>
      useAgentChatSidebarPreferences([makeProject('proj-alpha', '/books/alpha')], true),
    )

    expect(result.current.isProjectPinned('proj-alpha')).toBe(true)
    act(() => {
      result.current.toggleProjectPinned('proj-alpha')
    })
    expect(JSON.parse(window.localStorage.getItem(STORAGE_KEY) || '{}')).toEqual({
      sortMode: 'opened',
      pinnedProjects: [],
      manualProjectOrder: ['proj-alpha'],
      projectOpenedAt: { 'proj-alpha': 100 },
    })
  })
})
