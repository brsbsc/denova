import { execFile, execFileSync, spawn } from 'node:child_process'
import { once } from 'node:events'
import { readFileSync } from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import { startDesktopRenderer } from '../../web/scripts/desktop-renderer.mjs'

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../..')
const binary = path.join(root, 'desktop/build', process.platform === 'win32' ? 'Denova.exe' : 'Denova')
const version = JSON.parse(readFileSync(path.join(root, 'desktop/package.json'))).version
execFileSync('go', ['build', '-ldflags', `-X denova/internal/buildinfo.Version=${version}`, '-o', binary, './cmd/denova-desktop'], { cwd: root, stdio: 'inherit' })
const renderer = process.argv.includes('--hmr') ? await startDesktopRenderer() : null
const env = { ...process.env, MYGO_DEV: '1', DENOVA_DESKTOP_RESOURCES: path.join(root, 'desktop/build'), ...(renderer ? { DENOVA_DESKTOP_RENDERER_URL: renderer.origin } : {}) }
try {
  for (;;) {
    const child = spawn(binary, ['--dev-mode'], { stdio: 'inherit', env })
    let stopping = false
    let forceKillTimer = null
    const forceKill = () => {
      if (child.exitCode === null && child.signalCode === null) child.kill('SIGTERM')
    }
    const stop = () => {
      if (process.platform !== 'win32' || stopping || !child.pid) {
        stopping = true
        forceKill()
        return
      }
      stopping = true
      forceKillTimer = setTimeout(forceKill, 15000)
      forceKillTimer.unref()
      execFile(binary, [`--quit-pid=${child.pid}`], { env, windowsHide: true }, error => {
        if (error) forceKill()
      })
    }
    process.on('SIGINT', stop)
    process.on('SIGTERM', stop)
    const [code] = await once(child, 'exit')
    if (forceKillTimer) clearTimeout(forceKillTimer)
    process.off('SIGINT', stop)
    process.off('SIGTERM', stop)
    if (stopping || code !== 75) { process.exitCode = code || 0; break }
    // MyGo requests a relaunch after selecting another data directory. Keep
    // the renderer alive and let the saved selection replace the initial env.
    delete env.DENOVA_DIR
  }
} finally { await renderer?.close() }
