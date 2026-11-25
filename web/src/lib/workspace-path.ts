export const LORE_ITEMS_PATH = 'setting/lore/items.json'

export function isLoreItemsPath(path: string) {
  return normalizeWorkspacePath(path) === LORE_ITEMS_PATH
}

/** Returns the display-safe final segment while preserving the caller's canonical path separately. */
export function workspaceFileName(path: string) {
  const normalized = path.replace(/\\/g, '/')
  return normalized.split('/').pop() || path
}

function normalizeWorkspacePath(path: string) {
  return path.trim().replaceAll('\\', '/').replace(/^\.\/+/, '').replace(/\/{2,}/g, '/')
}
