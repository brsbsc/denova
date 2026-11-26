import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Check, Download, Loader2, RefreshCw } from 'lucide-react'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Progress } from '@/components/ui/progress'
import { checkEngineCLIUpdate, fetchEngineCLIStatus, installEngineCLI } from '@/features/agent-runtime/api'
import { resolveAgentRuntimeError, resolveLocalizedRuntimeText } from '@/features/agent-runtime/errors'
import type { AgentEngineID, EngineDescriptor, ManagedCLIStatus } from '@/features/agent-runtime/types'
import { Field } from './agent-form-controls'

const CLI_POLL_INTERVAL_MS = 1500
const MAX_CONSECUTIVE_POLL_FAILURES = 3

export interface AgentEngineCliActionsProps {
  engine: EngineDescriptor
  disabled?: boolean
  onEngineUpdated: (engine: EngineDescriptor) => void
  onRefresh: () => Promise<void>
}

/** Manages install, update check, and progress polling for Docker-managed external CLIs. */
export function AgentEngineCliActions({ engine, disabled, onEngineUpdated, onRefresh }: AgentEngineCliActionsProps) {
  const { t, i18n } = useTranslation()
  const managed = engine.managed
  const [live, setLive] = useState<{ engineId: AgentEngineID; status: ManagedCLIStatus } | null>(null)
  const [checking, setChecking] = useState(false)
  const [startingInstall, setStartingInstall] = useState(false)
  const [actionError, setActionError] = useState('')
  const mountedRef = useRef(true)
  const engineRef = useRef(engine)
  engineRef.current = engine
  const managedRef = useRef(managed)
  managedRef.current = managed
  const actionSeqRef = useRef(0)
  const callbacksRef = useRef({ onEngineUpdated, onRefresh })
  callbacksRef.current = { onEngineUpdated, onRefresh }

  useEffect(() => {
    mountedRef.current = true
    return () => {
      mountedRef.current = false
      actionSeqRef.current += 1
    }
  }, [])

  useEffect(() => {
    actionSeqRef.current += 1
    setLive(null)
    setActionError('')
    setChecking(false)
    setStartingInstall(false)
  }, [engine.id])

  const status = live && live.engineId === engine.id ? live.status : managed
  const isTransferring = Boolean(status?.managed && (status.state === 'installing' || status.state === 'updating'))

  useEffect(() => {
    if (!isTransferring) return
    const targetEngineId = engine.id
    let active = true
    let timer: ReturnType<typeof setTimeout> | undefined
    let consecutiveFailures = 0

    const poll = async () => {
      try {
        const next = await fetchEngineCLIStatus(targetEngineId)
        if (!active || !mountedRef.current || engineRef.current.id !== targetEngineId) return
        const nextStatus = next?.managed
        if (!nextStatus) throw new Error('invalid managed CLI status')
        consecutiveFailures = 0
        setActionError('')
        if (nextStatus.state !== 'installing' && nextStatus.state !== 'updating') {
          const updatedEngine = next.engine ?? {
            ...engineRef.current,
            version: nextStatus.installed_version || engineRef.current.version,
            managed: nextStatus,
          }
          callbacksRef.current.onEngineUpdated(updatedEngine)
          setLive(null)
          if (nextStatus.state === 'idle') {
            try {
              await callbacksRef.current.onRefresh()
            } catch (refreshCause) {
              console.warn('[agent-runtime] failed to refresh engines after CLI operation', { engine: targetEngineId, cause: refreshCause })
            }
          }
          return
        }
        setLive({ engineId: targetEngineId, status: nextStatus })
      } catch (cause) {
        if (!active || !mountedRef.current || engineRef.current.id !== targetEngineId) return
        consecutiveFailures += 1
        console.warn('[agent-runtime] failed to poll managed CLI status', { engine: targetEngineId, cause })
        if (consecutiveFailures >= MAX_CONSECUTIVE_POLL_FAILURES) {
          setActionError(resolveAgentRuntimeError(cause, t, i18n, 'agentRuntime.cli.actionFailed'))
          setLive((current) => {
            const base = current && current.engineId === targetEngineId ? current.status : managedRef.current
            return base ? { engineId: targetEngineId, status: { ...base, state: 'error' } } : null
          })
          return
        }
      }
      if (active) {
        timer = setTimeout(() => { void poll() }, CLI_POLL_INTERVAL_MS)
      }
    }

    timer = setTimeout(() => { void poll() }, CLI_POLL_INTERVAL_MS)
    return () => {
      active = false
      if (timer !== undefined) clearTimeout(timer)
    }
  }, [engine.id, isTransferring, i18n, t])

  if (!status?.managed) return null

  const isBusy = checking || startingInstall || isTransferring || status.state === 'checking'
  const version = status.installed_version || engine.version
  const percent = computePercent(status.bytes_downloaded, status.total_bytes)
  const transferredLabel = formatTransferProgress(status.bytes_downloaded, status.total_bytes)
  const errorText = actionError || (status.state === 'error' && status.last_error ? resolveLocalizedRuntimeText(status.last_error, t, i18n) : '')

  const handleCheckUpdate = async () => {
    const targetEngine = engineRef.current
    const seq = ++actionSeqRef.current
    setChecking(true)
    setActionError('')
    try {
      const next = await checkEngineCLIUpdate(targetEngine.id)
      if (!mountedRef.current || engineRef.current.id !== targetEngine.id || actionSeqRef.current !== seq) return
      setLive(null)
      callbacksRef.current.onEngineUpdated({
        ...engineRef.current,
        version: next.installed_version || engineRef.current.version,
        managed: next,
      })
    } catch (cause) {
      if (!mountedRef.current || engineRef.current.id !== targetEngine.id || actionSeqRef.current !== seq) return
      console.warn('[agent-runtime] managed CLI update check failed', { engine: targetEngine.id, cause })
      setActionError(resolveAgentRuntimeError(cause, t, i18n, 'agentRuntime.cli.actionFailed'))
    } finally {
      if (mountedRef.current && engineRef.current.id === targetEngine.id && actionSeqRef.current === seq) {
        setChecking(false)
      }
    }
  }

  const handleInstall = async () => {
    const targetEngineId = engineRef.current.id
    const seq = ++actionSeqRef.current
    setStartingInstall(true)
    setActionError('')
    try {
      const next = await installEngineCLI(targetEngineId)
      if (!mountedRef.current || engineRef.current.id !== targetEngineId || actionSeqRef.current !== seq) return
      if (next.state === 'installing' || next.state === 'updating') {
        setLive({ engineId: targetEngineId, status: next })
      } else {
        setLive(null)
        callbacksRef.current.onEngineUpdated({
          ...engineRef.current,
          version: next.installed_version || engineRef.current.version,
          managed: next,
        })
        if (next.state === 'idle') {
          await callbacksRef.current.onRefresh()
        }
      }
    } catch (cause) {
      if (!mountedRef.current || engineRef.current.id !== targetEngineId || actionSeqRef.current !== seq) return
      console.warn('[agent-runtime] managed CLI install failed', { engine: targetEngineId, cause })
      setActionError(resolveAgentRuntimeError(cause, t, i18n, 'agentRuntime.cli.actionFailed'))
    } finally {
      if (mountedRef.current && engineRef.current.id === targetEngineId && actionSeqRef.current === seq) {
        setStartingInstall(false)
      }
    }
  }

  return (
    <Field label={t('agentRuntime.cli.section')}>
      <div className="flex w-full min-w-0 flex-col gap-2">
        <div className="flex min-w-0 flex-wrap items-center gap-2">
          {status.installed && version && (
            <Badge variant="outline" className="font-mono text-[11px]">
              {formatVersion(version)}
            </Badge>
          )}
          {!status.installed ? (
            <Button type="button" size="sm" disabled={disabled || isBusy} onClick={() => void handleInstall()}>
              {isTransferring || startingInstall ? <Loader2 data-icon="inline-start" className="animate-spin" /> : <Download data-icon="inline-start" />}
              {t(isTransferring || startingInstall ? 'agentRuntime.cli.installing' : 'agentRuntime.cli.install')}
            </Button>
          ) : (
            <>
              <Button type="button" size="sm" variant="outline" disabled={disabled || isBusy} onClick={() => void handleCheckUpdate()}>
                <RefreshCw data-icon="inline-start" className={checking ? 'animate-spin' : ''} />
                {t(checking ? 'agentRuntime.cli.checkingUpdate' : 'agentRuntime.cli.checkUpdate')}
              </Button>
              {status.update_available && (
                <Button type="button" size="sm" disabled={disabled || isBusy} onClick={() => void handleInstall()}>
                  {isTransferring || startingInstall ? <Loader2 data-icon="inline-start" className="animate-spin" /> : <Download data-icon="inline-start" />}
                  {isTransferring || startingInstall
                    ? t('agentRuntime.cli.updating')
                    : status.latest_version
                      ? t('agentRuntime.cli.updateTo', { version: formatVersion(status.latest_version) })
                      : t('agentRuntime.cli.update')}
                </Button>
              )}
              {!status.update_available && status.last_checked_at && !isBusy && status.state !== 'error' && (
                <Badge variant="secondary">
                  <Check className="text-[var(--nova-success)]" />
                  {t('agentRuntime.cli.upToDate')}
                </Badge>
              )}
            </>
          )}
        </div>
        {isTransferring && (
          <div className="flex min-w-0 flex-col gap-1.5 rounded-[var(--nova-radius)] border border-[var(--nova-border)] bg-[var(--nova-surface-2)] px-2.5 py-2">
            <div className="flex flex-wrap items-center justify-between gap-2 text-xs text-[var(--nova-text-muted)]">
              <span>{t(status.state === 'updating' ? 'agentRuntime.cli.updatingProgress' : 'agentRuntime.cli.installingProgress')}</span>
              {percent !== undefined && <span className="font-mono">{percent}%</span>}
            </div>
            <Progress value={percent ?? 0} aria-label={t('agentRuntime.cli.progressAria')} />
            {transferredLabel && (
              <div className="font-mono text-[11px] text-[var(--nova-text-faint)]">
                {transferredLabel}
              </div>
            )}
          </div>
        )}
        {status.pending_reload && (
          <p role="status" className="text-xs leading-relaxed text-[var(--nova-text-muted)]">
            {t('agentRuntime.cli.pendingReload')}
          </p>
        )}
        {errorText && (
          <p role="alert" className="text-xs text-destructive">
            {errorText}
          </p>
        )}
      </div>
    </Field>
  )
}

function formatVersion(version: string): string {
  const trimmed = version.trim()
  if (!trimmed) return ''
  return trimmed.startsWith('v') ? trimmed : `v${trimmed}`
}

function computePercent(downloaded?: number, total?: number): number | undefined {
  if (!total || !Number.isFinite(total) || total <= 0) return undefined
  const value = downloaded && Number.isFinite(downloaded) ? downloaded : 0
  return Math.min(100, Math.max(0, Math.round((value / total) * 100)))
}

function formatTransferProgress(downloaded?: number, total?: number): string {
  const hasTotal = Boolean(total && Number.isFinite(total) && total > 0)
  const hasDownloaded = Boolean(downloaded && Number.isFinite(downloaded) && downloaded > 0)
  if (hasTotal) return `${formatBytes(downloaded ?? 0)} / ${formatBytes(total!)}`
  if (hasDownloaded) return formatBytes(downloaded!)
  return ''
}

function formatBytes(value: number): string {
  if (!Number.isFinite(value) || value <= 0) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB']
  let size = value
  let index = 0
  while (size >= 1024 && index < units.length - 1) {
    size /= 1024
    index += 1
  }
  return `${size.toFixed(index === 0 ? 0 : 1)} ${units[index]}`
}
