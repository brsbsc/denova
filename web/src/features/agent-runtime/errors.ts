import { APIError } from '@/lib/api-client'

type Translator = (key: string) => string
type I18nLookup = { exists: (key: string) => boolean }

/** Translates a backend i18n key when available, otherwise keeps the raw message. */
export function resolveLocalizedRuntimeText(value: string, t: Translator, i18n: I18nLookup): string {
  return i18n.exists(value) ? t(value) : value
}

/** Resolves an agent-runtime error into a single localized message, preferring APIError.code. */
export function resolveAgentRuntimeError(cause: unknown, t: Translator, i18n: I18nLookup, fallbackKey: string): string {
  if (cause instanceof APIError && cause.code && i18n.exists(cause.code)) {
    return t(cause.code)
  }
  if (cause instanceof Error && cause.message.trim()) {
    return resolveLocalizedRuntimeText(cause.message.trim(), t, i18n)
  }
  return t(fallbackKey)
}
