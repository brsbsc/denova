import { render } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { AdaptiveResizablePanes } from './adaptive-resizable-panes'
import type { AdaptivePanelSizing } from './adaptive-panel-layout'

const setLayoutSpy = vi.fn()
let currentLayout: Record<string, number> = {}

vi.mock('react-resizable-panels', () => ({
  useGroupRef: () => ({ current: null }),
  Group: ({
    children,
    elementRef,
    groupRef,
  }: {
    children: React.ReactNode
    elementRef?: React.RefObject<HTMLDivElement | null>
    groupRef?: React.RefObject<{ getLayout: () => Record<string, number>; setLayout: (layout: Record<string, number>) => void } | null>
  }) => {
    if (groupRef && !groupRef.current) {
      groupRef.current = {
        getLayout: () => currentLayout,
        setLayout: (next: Record<string, number>) => {
          currentLayout = next
          setLayoutSpy(next)
        },
      }
    }
    return (
      <div
        ref={(node) => {
          if (node) {
            Object.defineProperty(node, 'clientWidth', { configurable: true, value: 1200 })
          }
          if (elementRef) elementRef.current = node
        }}
      >
        {children}
      </div>
    )
  },
  Panel: ({ children }: { children: React.ReactNode }) => <div>{children}</div>,
  Separator: () => <div />,
}))

const baseSizing: AdaptivePanelSizing = {
  left: { layoutKey: 'test-left', label: 'Left', defaultSize: '320px', minSize: '220px', maxSize: '42%' },
  right: { layoutKey: 'test-right', label: 'Right', defaultSize: '420px', minSize: '300px', maxSize: '65%', mainMinSize: '240px' },
  leftVisible: true,
  rightVisible: false,
  rightExpanded: false,
}

describe('AdaptiveResizablePanes', () => {
  beforeEach(() => {
    window.localStorage.clear()
    setLayoutSpy.mockReset()
    currentLayout = {}
  })

  it('does not repeatedly invoke setLayout on unrelated child re-renders when geometry is unchanged', () => {
    const leftPane = { id: 'left', title: 'Directory', side: 'left' as const, content: <div>dir</div> }
    const { rerender } = render(
      <AdaptiveResizablePanes left={leftPane} right={null} sizing={baseSizing}>
        <div>initial</div>
      </AdaptiveResizablePanes>,
    )
    expect(setLayoutSpy).toHaveBeenCalledTimes(1)

    rerender(
      <AdaptiveResizablePanes left={{ ...leftPane, content: <div>dir-2</div> }} right={null} sizing={{ ...baseSizing }}>
        <div>updated</div>
      </AdaptiveResizablePanes>,
    )
    rerender(
      <AdaptiveResizablePanes left={{ ...leftPane, content: <div>dir-3</div> }} right={null} sizing={{ ...baseSizing }}>
        <div>updated again</div>
      </AdaptiveResizablePanes>,
    )

    expect(setLayoutSpy).toHaveBeenCalledTimes(1)
  })
})
