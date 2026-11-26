import { useCallback, useEffect, useId, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Check, Copy, ExternalLink, Loader2 } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { ToggleGroup, ToggleGroupItem } from '@/components/ui/toggle-group'
import { cancelEngineAuth, fetchEngineAuthSession, startEngineAuth, submitEngineAuth } from '@/features/agent-runtime/api'
import { resolveAgentRuntimeError } from '@/features/agent-runtime/errors'
import type { AgentEngineID, AuthSessionSnapshot, AuthSessionStatus, ClaudeAuthMethod, EngineDescriptor } from '@/features/agent-runtime/types'
import { APIError } from '@/lib/api-client'

const AUTH_POLL_INTERVAL_MS = 1500
const MAX_CONSECUTIVE_POLL_FAILURES = 3

export interface AgentEngineAuthDialogProps {
  engineId: AgentEngineID
  open: boolean
  onOpenChange: (open: boolean) => void
  onAuthenticated: (engine?: EngineDescriptor) => Promise<void> | void
}

/** Unified browser OAuth dialog for Codex device code and Claude/Antigravity code-paste flows. */
export function AgentEngineAuthDialog({ engineId, open, onOpenChange, onAuthenticated }: AgentEngineAuthDialogProps) {
  const { t, i18n } = useTranslation()
  const codeInputId = useId()
  const [method, setMethod] = useState<ClaudeAuthMethod>('claudeai')
  const [session, setSession] = useState<AuthSessionSnapshot | null>(null)
  const [starting, setStarting] = useState(false)
  const [submitting, setSubmitting] = useState(false)
  const [codeInput, setCodeInput] = useState('')
  const [error, setError] = useState('')
  const [submitError, setSubmitError] = useState('')
  const [copiedField, setCopiedField] = useState<'url' | 'code' | null>(null)
  const [copyError, setCopyError] = useState('')

  const openRef = useRef(open)
  openRef.current = open
  const attemptRef = useRef(0)
  const sessionRef = useRef<AuthSessionSnapshot | null>(null)
  const cancelledIdsRef = useRef(new Set<string>())
  const completedIdsRef = useRef(new Set<string>())
  const copyTimerRef = useRef<ReturnType<typeof setTimeout> | undefined>(undefined)
  const callbacksRef = useRef({ onAuthenticated, onOpenChange })
  callbacksRef.current = { onAuthenticated, onOpenChange }

  const cancelIfActive = useCallback((target: AuthSessionSnapshot | null) => {
    if (!target || !isActiveStatus(target.status)) return
    if (cancelledIdsRef.current.has(target.session_id)) return
    cancelledIdsRef.current.add(target.session_id)
    void cancelEngineAuth(target.engine, target.session_id).catch((cause) => {
      console.warn('[agent-runtime] failed to cancel auth session', { engine: target.engine, cause })
    })
  }, [])

  const completeSession = useCallback(async (snapshot: AuthSessionSnapshot) => {
    if (completedIdsRef.current.has(snapshot.session_id)) return
    completedIdsRef.current.add(snapshot.session_id)
    await callbacksRef.current.onAuthenticated(snapshot.engine_state)
    callbacksRef.current.onOpenChange(false)
  }, [])

  const resolveSnapshotFailure = useCallback((snapshot: AuthSessionSnapshot) => {
    if (snapshot.status === 'cancelled') return t('agentRuntime.auth.cancelled')
    if (snapshot.error_key && i18n.exists(snapshot.error_key)) return t(snapshot.error_key)
    return snapshot.error_key || t('agentRuntime.auth.failed')
  }, [i18n, t])

  const beginAuth = useCallback(async (chosenMethod?: ClaudeAuthMethod) => {
    cancelIfActive(sessionRef.current)
    const attempt = ++attemptRef.current
    sessionRef.current = null
    setSession(null)
    setError('')
    setSubmitError('')
    setCopyError('')
    setCodeInput('')
    setSubmitting(false)
    setStarting(true)
    try {
      const next = await startEngineAuth(
        engineId,
        engineId === 'claude' ? { method: chosenMethod ?? method } : undefined,
      )
      if (!openRef.current || attempt !== attemptRef.current) {
        cancelIfActive(next)
        return
      }
      sessionRef.current = next
      setSession(next)
      setStarting(false)
      if (next.status === 'succeeded') {
        void completeSession(next)
      } else if (next.status === 'failed' || next.status === 'cancelled') {
        setError(resolveSnapshotFailure(next))
      }
    } catch (cause) {
      if (!openRef.current || attempt !== attemptRef.current) return
      console.warn('[agent-runtime] failed to start auth session', { engine: engineId, cause })
      setStarting(false)
      setError(resolveAgentRuntimeError(cause, t, i18n, 'agentRuntime.auth.failed'))
    }
  }, [cancelIfActive, completeSession, engineId, i18n, method, resolveSnapshotFailure, t])

  useEffect(() => {
    openRef.current = open
    if (!open) {
      attemptRef.current += 1
      cancelIfActive(sessionRef.current)
      sessionRef.current = null
      return
    }
    setMethod('claudeai')
    setSession(null)
    sessionRef.current = null
    setStarting(false)
    setSubmitting(false)
    setCodeInput('')
    setError('')
    setSubmitError('')
    setCopiedField(null)
    setCopyError('')

    if (engineId !== 'claude') {
      void beginAuth()
    }

    return () => {
      openRef.current = false
      attemptRef.current += 1
      cancelIfActive(sessionRef.current)
      sessionRef.current = null
      if (copyTimerRef.current !== undefined) clearTimeout(copyTimerRef.current)
    }
    // Intentionally scoped to open + engineId so selecting Claude method does not re-trigger reset.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, engineId])

  const activeSessionID = session?.session_id ?? ''
  const shouldPoll = Boolean(open && session && isActiveStatus(session.status))

  useEffect(() => {
    if (!shouldPoll || !activeSessionID) return
    let active = true
    let timer: ReturnType<typeof setTimeout> | undefined
    let consecutiveFailures = 0

    const poll = async () => {
      try {
        const next = await fetchEngineAuthSession(engineId, activeSessionID)
        if (!active || !openRef.current || sessionRef.current?.session_id !== activeSessionID) return
        if (!next || typeof next.status !== 'string') {
          throw new Error('invalid auth session snapshot')
        }
        consecutiveFailures = 0
        const currentStatus = sessionRef.current?.status
        if (!(currentStatus === 'completing' && (next.status === 'awaiting_user' || next.status === 'starting'))) {
          sessionRef.current = next
          setSession(next)
        }
        if (next.status === 'succeeded') {
          void completeSession(next)
          return
        }
        if (next.status === 'failed' || next.status === 'cancelled') {
          setError(resolveSnapshotFailure(next))
          return
        }
      } catch (cause) {
        if (!active || !openRef.current) return
        if (cause instanceof APIError && cause.status === 404) {
          sessionRef.current = null
          setSession(null)
          setError(t('agentRuntime.auth.expired'))
          return
        }
        consecutiveFailures += 1
        console.warn('[agent-runtime] failed to poll auth session', { engine: engineId, cause })
        if (consecutiveFailures >= MAX_CONSECUTIVE_POLL_FAILURES) {
          setError(resolveAgentRuntimeError(cause, t, i18n, 'agentRuntime.auth.failed'))
          return
        }
      }
      if (active && openRef.current) {
        timer = setTimeout(() => { void poll() }, AUTH_POLL_INTERVAL_MS)
      }
    }

    timer = setTimeout(() => { void poll() }, AUTH_POLL_INTERVAL_MS)
    return () => {
      active = false
      if (timer !== undefined) clearTimeout(timer)
    }
  }, [activeSessionID, completeSession, engineId, i18n, resolveSnapshotFailure, shouldPoll, t])

  const handleOpenChange = (nextOpen: boolean) => {
    if (!nextOpen) {
      openRef.current = false
      attemptRef.current += 1
      cancelIfActive(sessionRef.current)
      sessionRef.current = null
    }
    onOpenChange(nextOpen)
  }

  const handleClaudeMethodChange = (next: string) => {
    if (next !== 'claudeai' && next !== 'console') return
    if (next === method) return
    setMethod(next)
    if (session || error) {
      void beginAuth(next)
    }
  }

  const handleSubmit = async () => {
    const trimmed = codeInput.trim()
    if (!trimmed || !session || submitting) return
    const attempt = attemptRef.current
    const targetSessionId = session.session_id
    setSubmitting(true)
    setSubmitError('')
    try {
      const next = await submitEngineAuth(engineId, targetSessionId, trimmed)
      if (!openRef.current || attempt !== attemptRef.current || sessionRef.current?.session_id !== targetSessionId) return
      sessionRef.current = next
      setSession(next)
      if (next.status === 'succeeded') {
        void completeSession(next)
      } else if (next.status === 'failed' || next.status === 'cancelled') {
        setError(resolveSnapshotFailure(next))
      }
    } catch (cause) {
      if (!openRef.current || attempt !== attemptRef.current || sessionRef.current?.session_id !== targetSessionId) return
      console.warn('[agent-runtime] failed to submit auth input', { engine: engineId, cause })
      if (cause instanceof APIError && cause.status === 404) {
        sessionRef.current = null
        setSession(null)
        setError(t('agentRuntime.auth.expired'))
      } else {
        setSubmitError(resolveAgentRuntimeError(cause, t, i18n, 'agentRuntime.auth.invalidInput'))
      }
    } finally {
      if (attempt === attemptRef.current) {
        setSubmitting(false)
      }
    }
  }

  const copyValue = async (field: 'url' | 'code', value: string) => {
    setCopyError('')
    try {
      if (!navigator.clipboard?.writeText) throw new Error('clipboard unavailable')
      await navigator.clipboard.writeText(value)
      setCopiedField(field)
      if (copyTimerRef.current !== undefined) clearTimeout(copyTimerRef.current)
      copyTimerRef.current = setTimeout(() => setCopiedField(null), 2000)
    } catch {
      console.warn('[agent-runtime] failed to copy auth field', { engine: engineId, field })
      setCopyError(t('agentRuntime.auth.copyFailed'))
    }
  }

  const authUrl = isSafeExternalURL(session?.auth_url) ? session!.auth_url! : ''
  const hintKey = session?.hint_key || (engineId === 'codex' && session?.mode === 'device_code' ? 'agentRuntime.auth.codexDeviceHint' : '')
  const hintText = hintKey ? (i18n.exists(hintKey) ? t(hintKey) : hintKey) : ''
  const isClaudeIdle = engineId === 'claude' && !session && !starting && !error
  const isWaitingForSession = starting || session?.status === 'starting'
  const showInteractiveSession = Boolean(session && (session.status === 'awaiting_user' || session.status === 'completing'))

  return (
    <Dialog open={open} onOpenChange={handleOpenChange}>
      <DialogContent aria-describedby={undefined} className="max-w-[min(calc(100vw-2rem),30rem)]">
        <DialogHeader>
          <DialogTitle>{t('agentRuntime.auth.dialogTitle', { engine: t(`agentRuntime.${engineId}`) })}</DialogTitle>
        </DialogHeader>

        <div className="flex min-w-0 flex-col gap-3">
          {engineId === 'claude' && (
            <div className="flex min-w-0 flex-col gap-1.5">
              <span className="text-xs font-medium text-muted-foreground">{t('agentRuntime.auth.accountType')}</span>
              <ToggleGroup
                type="single"
                variant="outline"
                size="sm"
                value={method}
                disabled={starting || submitting || session?.status === 'completing'}
                aria-label={t('agentRuntime.auth.accountType')}
                className="max-w-full flex-wrap"
                onValueChange={handleClaudeMethodChange}
              >
                <ToggleGroupItem value="claudeai">{t('agentRuntime.auth.claudeSubscription')}</ToggleGroupItem>
                <ToggleGroupItem value="console">{t('agentRuntime.auth.claudeConsole')}</ToggleGroupItem>
              </ToggleGroup>
            </div>
          )}

          {isWaitingForSession && (
            <div role="status" className="flex items-center gap-2 py-2 text-xs text-muted-foreground">
              <Loader2 className="size-4 shrink-0 animate-spin" />
              <span>{t('agentRuntime.auth.starting')}</span>
            </div>
          )}

          {showInteractiveSession && session && (
            <>
              {authUrl && (
                <div className="flex min-w-0 flex-col gap-2">
                  <div className="flex flex-wrap items-center gap-2">
                    <Button asChild size="sm">
                      <a href={authUrl} target="_blank" rel="noopener noreferrer">
                        <ExternalLink data-icon="inline-start" />
                        {t('agentRuntime.auth.openLink')}
                      </a>
                    </Button>
                    <Button type="button" size="sm" variant="outline" onClick={() => void copyValue('url', authUrl)}>
                      {copiedField === 'url' ? <Check data-icon="inline-start" /> : <Copy data-icon="inline-start" />}
                      {t(copiedField === 'url' ? 'agentRuntime.auth.copied' : 'agentRuntime.auth.copyLink')}
                    </Button>
                  </div>
                  <code className="block truncate rounded-md border border-border bg-muted/40 px-2.5 py-1.5 font-mono text-xs text-muted-foreground select-all" title={authUrl}>
                    {authUrl}
                  </code>
                </div>
              )}

              {session.mode === 'device_code' && (
                <>
                  {session.user_code && (
                    <div className="flex min-w-0 flex-col gap-1.5 rounded-lg border border-border bg-muted/40 p-3">
                      <span className="text-xs text-muted-foreground">{t('agentRuntime.auth.deviceCodeLabel')}</span>
                      <div className="flex flex-wrap items-center justify-between gap-2">
                        <code className="select-all font-mono text-xl font-semibold tracking-[0.18em] text-foreground break-all">
                          {session.user_code}
                        </code>
                        <Button type="button" size="sm" variant="outline" onClick={() => void copyValue('code', session.user_code!)}>
                          {copiedField === 'code' ? <Check data-icon="inline-start" /> : <Copy data-icon="inline-start" />}
                          {t(copiedField === 'code' ? 'agentRuntime.auth.copied' : 'agentRuntime.auth.copyCode')}
                        </Button>
                      </div>
                    </div>
                  )}
                  <div role="status" className="flex items-center gap-2 text-xs text-muted-foreground">
                    <Loader2 className="size-3.5 shrink-0 animate-spin" />
                    <span>{t(session.status === 'completing' ? 'agentRuntime.auth.completing' : 'agentRuntime.auth.waitingConfirmation')}</span>
                  </div>
                  {hintText && (
                    <p className="rounded-md border border-border bg-muted/30 px-2.5 py-2 text-xs leading-relaxed text-muted-foreground">
                      {hintText}
                    </p>
                  )}
                </>
              )}

              {session.mode === 'code_paste' && (
                <form
                  className="flex min-w-0 flex-col gap-2"
                  onSubmit={(event) => {
                    event.preventDefault()
                    void handleSubmit()
                  }}
                >
                  <label htmlFor={codeInputId} className="text-xs font-medium text-foreground">
                    {t('agentRuntime.auth.codeLabel')}
                  </label>
                  <div className="flex min-w-0 flex-col gap-2 sm:flex-row">
                    <Input
                      id={codeInputId}
                      value={codeInput}
                      autoComplete="off"
                      spellCheck={false}
                      disabled={submitting || session.status === 'completing'}
                      placeholder={t('agentRuntime.auth.codePlaceholder')}
                      onChange={(event) => {
                        setCodeInput(event.target.value)
                        if (submitError) setSubmitError('')
                      }}
                      className="min-w-0 flex-1 font-mono text-xs"
                    />
                    <Button type="submit" size="sm" disabled={!codeInput.trim() || submitting || session.status === 'completing'}>
                      {(submitting || session.status === 'completing') && <Loader2 data-icon="inline-start" className="animate-spin" />}
                      {t(session.status === 'completing' ? 'agentRuntime.auth.completing' : 'agentRuntime.auth.submit')}
                    </Button>
                  </div>
                  {submitError && <p role="alert" className="text-xs text-destructive">{submitError}</p>}
                </form>
              )}

              {copyError && <p role="alert" className="text-xs text-destructive">{copyError}</p>}
            </>
          )}

          {error && <p role="alert" className="text-xs text-destructive">{error}</p>}
        </div>

        <DialogFooter>
          <Button type="button" variant="outline" size="sm" onClick={() => handleOpenChange(false)}>
            {t('common.cancel')}
          </Button>
          {isClaudeIdle && (
            <Button type="button" size="sm" onClick={() => void beginAuth(method)}>
              {t('agentRuntime.auth.start')}
            </Button>
          )}
          {error && (
            <Button type="button" size="sm" onClick={() => void beginAuth(method)}>
              {t('common.retry')}
            </Button>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

function isActiveStatus(status: AuthSessionStatus): boolean {
  return status === 'starting' || status === 'awaiting_user' || status === 'completing'
}

function isSafeExternalURL(value?: string): boolean {
  if (!value) return false
  return /^https?:\/\//i.test(value.trim())
}
