import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { getUpdateNotes } from '@/features/settings/api'
import { UpdatePanel } from '@/features/settings/UpdateSettings'
import { DENOVA_GITHUB_URL } from '@/lib/product-links'
import { UpdateNotesDialog, UpdateNotesView } from './UpdateNotes'

vi.mock('@/features/settings/api', () => ({
  getUpdateNotes: vi.fn(),
  applyUpdate: vi.fn(),
  checkForUpdate: vi.fn(),
  getUpdateStatus: vi.fn(),
  installUpdateStream: vi.fn(),
  uploadUpdate: vi.fn(),
}))

function renderWithQuery(ui: React.ReactElement) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  return render(<QueryClientProvider client={client}>{ui}</QueryClientProvider>)
}

describe('UpdateNotes fork releases URL fallback', () => {
  beforeEach(() => {
    vi.mocked(getUpdateNotes).mockReset()
  })

  it('links to the fork releases list when UpdateNotesView fails to load notes', async () => {
    vi.mocked(getUpdateNotes).mockRejectedValueOnce(new Error('Network unavailable'))
    renderWithQuery(
      <UpdateNotesView
        range={{ fromVersion: '0.6.3', toVersion: '0.8.0' }}
        releasesURL="https://github.com/BRSBSC/denova/releases/latest"
      />,
    )
    const link = await waitFor(() => screen.getByRole('link', { name: /查看发布记录|View release history/i }))
    expect(link).toHaveAttribute('href', 'https://github.com/BRSBSC/denova/releases')
  })

  it('forwards fork releasesURL through UpdateNotesDialog on load failure', async () => {
    vi.mocked(getUpdateNotes).mockRejectedValueOnce(new Error('Network unavailable'))
    renderWithQuery(
      <UpdateNotesDialog
        range={{ fromVersion: '0.6.3', toVersion: '0.8.0' }}
        releasesURL="https://github.com/owner/fork/releases"
        open
        onOpenChange={() => {}}
      />,
    )
    const link = await waitFor(() => screen.getByRole('link', { name: /查看发布记录|View release history/i }))
    expect(link).toHaveAttribute('href', 'https://github.com/owner/fork/releases')
  })

  it('falls back to DENOVA_GITHUB_URL releases when releasesURL is omitted', async () => {
    vi.mocked(getUpdateNotes).mockRejectedValueOnce(new Error('Network unavailable'))
    renderWithQuery(<UpdateNotesView range={{ fromVersion: '0.6.3', toVersion: '0.8.0' }} />)
    const link = await waitFor(() => screen.getByRole('link', { name: /查看发布记录|View release history/i }))
    expect(link).toHaveAttribute('href', `${DENOVA_GITHUB_URL}/releases`)
  })

  it('passes releasesURL from UpdatePanel to UpdateNotesView preview on failure', async () => {
    vi.mocked(getUpdateNotes).mockRejectedValueOnce(new Error('Network unavailable'))
    renderWithQuery(
      <UpdatePanel
        distribution="web"
        activeWork={false}
        completedUpdate={null}
        status={{
          current_version: '0.6.3',
          latest_version: '0.8.0',
          update_available: true,
          can_install: true,
          platform: 'windows/amd64',
          release_url: 'https://github.com/BRSBSC/denova/releases/tag/v0.8.0',
          published_at: '2026-11-22T00:00:00Z',
        }}
        installResult={null}
        installProgress={null}
        operation="idle"
        error={null}
        releasesURL="https://github.com/BRSBSC/denova/releases/latest"
        onCheck={() => {}}
        onInstall={() => {}}
        onApply={() => {}}
        onWait={() => {}}
        onCancelWait={() => {}}
        onUpload={() => {}}
      />,
    )
    const link = await waitFor(() => screen.getByRole('link', { name: /查看发布记录|View release history/i }))
    expect(link).toHaveAttribute('href', 'https://github.com/BRSBSC/denova/releases')
  })
})
