import { render } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { LoreIndexSortableCard } from './LoreIndexSorting'

const setNodeRefSpy = vi.fn()
const setActivatorNodeRefSpy = vi.fn()

vi.mock('@dnd-kit/sortable', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@dnd-kit/sortable')>()
  return {
    ...actual,
    useSortable: () => ({
      attributes: { role: 'button', tabIndex: 0 },
      listeners: undefined,
      setNodeRef: setNodeRefSpy,
      setActivatorNodeRef: setActivatorNodeRefSpy,
      transform: null,
      transition: undefined,
      isDragging: false,
    }),
  }
})

describe('LoreIndexSortableCard', () => {
  beforeEach(() => {
    setNodeRefSpy.mockReset()
    setActivatorNodeRefSpy.mockReset()
  })

  it('keeps a stable DOM ref callback across re-renders instead of detaching and reattaching dnd-kit refs', () => {
    const { rerender } = render(
      <LoreIndexSortableCard id="card-1" name="Alpha" disabled={false}>
        <div>body 1</div>
      </LoreIndexSortableCard>,
    )
    expect(setNodeRefSpy).toHaveBeenCalledTimes(1)
    expect(setActivatorNodeRefSpy).toHaveBeenCalledTimes(1)

    rerender(
      <LoreIndexSortableCard id="card-1" name="Alpha" disabled={false}>
        <div>body 2</div>
      </LoreIndexSortableCard>,
    )
    rerender(
      <LoreIndexSortableCard id="card-1" name="Alpha" disabled={false}>
        <div>body 3</div>
      </LoreIndexSortableCard>,
    )

    expect(setNodeRefSpy).toHaveBeenCalledTimes(1)
    expect(setActivatorNodeRefSpy).toHaveBeenCalledTimes(1)
  })
})
