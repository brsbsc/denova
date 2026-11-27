import type { EditorFlushHandler } from '@/components/Editor/useEditorDraftPersistence'
import type { WorkbenchNotice } from '@/features/notices/use-workbench-notice'
import type { FileNode } from '@/hooks/useWorkspace'
import type {
  BookRecord,
  BookSortMode,
  ChapterSummary,
  LoreItem,
  TextSelection,
  WorkspaceSearchResult,
  WorkspaceSummary,
} from '@/lib/api'
import type { RightPanel, WorkspaceMode } from '@/stores/workspace-store'
import type { Tab } from './TabController'
import type { WorkspaceChangeMetadata } from '@/features/changes/types'
import type { ProjectFileDocument } from '@/lib/api-client/project-files'
import type { AgentChatConversationState } from '@/features/agent-chat/AgentChatConversationTab'
import type { AgentSubAgentSessionTarget } from '@/components/Chat/AgentSubAgentSessionPanel'

/** Public composition contract for the workbench route host. */
export interface ModeRouterProps {
  mode: WorkspaceMode
  lastCreationRoute: 'ide' | 'interactive'
  currentBookName: string
  workspace: string
  projectId: string
  summary: WorkspaceSummary | null
  currentChapter?: ChapterSummary
  isStreaming: boolean
  projectVisible: boolean
  activityBarExpanded: boolean
  rightPanel: RightPanel
  settingsOpen: boolean
  developerMode?: boolean
  interactiveRightVisible: boolean
  novaDir: string
  books: BookRecord[]
  bookSortMode: BookSortMode
  tree: FileNode[]
  loading: boolean
  selectedFile: string | null
  fileDocument: ProjectFileDocument | null
  fileDocuments?: ReadonlyMap<string, ProjectFileDocument>
  openTabs: Tab[]
  activeTabKey: string | null
  sidebarView: 'outline' | 'files' | 'search'
  editorSearchIntent: { path: string; query: string; line: number; nonce: number } | null
  saveSignal: number
  editorAutoSaveEnabled: boolean
  editorAutoSaveDelayMs: number
  projectExplorerRefreshSignal: number
  versionRefreshSignal: number
  references: string[]
  loreReferences: string[]
  loreItems: LoreItem[]
  textSelections: TextSelection[]
  writingAgentConversation: AgentChatConversationState
  onWritingAgentConversationStateChange?: (state: AgentChatConversationState) => void
  onOpenWritingSubAgentSession: (target: AgentSubAgentSessionTarget) => void | Promise<void>
  notice?: WorkbenchNotice | null
  onSetMode: (mode: WorkspaceMode) => void
  onToggleActivityBarExpanded: () => void
  onToggleProjectVisible: () => void
  onSetRightPanel: (panel: RightPanel) => void
  onToggleSettings: () => void
  onCloseSettings: () => void
  onToggleInteractiveRightPanel: () => void
  onSwitchBook: (path: string) => void | Promise<void>
  onQuickSwitchBook: (path: string) => Promise<boolean>
  onBeforeWorkspaceSwitch: EditorFlushHandler
  onBooksChange: () => void | Promise<void>
  /** Synchronizes a newly created Book without leaving the current route. */
  onBookCreated: (workspace: string) => void | Promise<void>
  onOpenCharacterCardImport: () => void
  onSetSidebarView: (view: 'outline' | 'files' | 'search') => void
  onSelectSearchResult: (result: WorkspaceSearchResult, query: string) => void | Promise<void>
  onSelectFile: (path: string) => boolean | void | Promise<boolean | void>
  onSetChapterConfirmed: (path: string, confirmed: boolean) => void | Promise<void>
  onReferenceFile: (path: string) => void
  onCreateItem: (path: string, type: 'file' | 'dir') => Promise<void>
  onDeleteItem: (path: string) => Promise<void>
  onRenameItem: (path: string, newName: string) => Promise<void>
  onCopyItem: (from: string, to: string) => Promise<void>
  onMoveItem: (from: string, to: string) => Promise<void>
  onRefreshWorkspace: () => void | Promise<void>
  onActivateTab: (tab: Tab) => void
  onCloseTab: (tab: Tab) => void
  onToggleTabPin: (tab: Tab) => void
  onMoveTab: (sourceKey: string, targetKey: string) => void
  onOpenLoreTab: () => Promise<boolean>
  onSaveCurrentFile: (path: string, content: string, baseRevision: string) => Promise<{ revision?: string }>
  onEditorFlushHandlerChange: (handler: EditorFlushHandler | null) => void
  onWorkspaceChanged: (paths: string[], metadata: WorkspaceChangeMetadata) => void | Promise<void>
  onQuoteSelection: (selection: TextSelection) => void
  onReferenceRemove: (path: string) => void
  onLoreReferenceAdd: (id: string) => void
  onLoreReferenceRemove: (id: string) => void
  onTextSelectionRemove: (index: number) => void
  onDismissNotice?: () => void
}
