import { expect, test } from '../support/fixtures'
import { createAndOpenBook } from '../support/api'

test('Docker-managed CLI install/update and browser OAuth flows render cleanly across themes and viewports', async ({ page, request }, testInfo) => {
  test.setTimeout(90_000)
  await page.emulateMedia({ reducedMotion: 'reduce' })
  await createAndOpenBook(request, 'Docker CLI OAuth Browser Book')

  const initial = await (await request.get('/api/settings')).json()
  const seeded = await request.patch('/api/settings', {
    data: {
      layer: 'user',
      base_revision: initial.revisions.user,
      changes: {
        language: 'zh-CN',
        theme: 'dark',
        agent_runtimes: { ide: { selected: 'codex' } },
      },
    },
  })
  expect(seeded.ok(), await seeded.text()).toBe(true)

  const baseCatalog = await (await request.get('/api/agent-runtimes')).json()
  let codexInstalled = false
  let codexStatus: 'not_installed' | 'auth_required' | 'ready' = 'not_installed'
  let installPollCount = 0
  let codexAuthPollCount = 0
  let agyStatus: 'ready' | 'auth_required' = 'ready'

  const buildCatalogItems = () => baseCatalog.items.map((item: Record<string, unknown>) => {
    if (item.id === 'codex') {
      return {
        ...item,
        status: codexStatus,
        version: codexInstalled ? '0.135.0' : '',
        auth_supported: true,
        managed: {
          managed: true,
          installed: codexInstalled,
          installed_version: codexInstalled ? '0.135.0' : undefined,
          update_available: false,
          state: 'idle',
        },
      }
    }
    if (item.id === 'claude') {
      return {
        ...item,
        status: 'auth_required',
        version: '2.1.320',
        auth_supported: true,
        managed: {
          managed: true,
          installed: true,
          installed_version: '2.1.320',
          latest_version: '2.1.332',
          update_available: true,
          pending_reload: true,
          state: 'idle',
          last_checked_at: '2026-11-26T14:00:00Z',
        },
      }
    }
    if (item.id === 'antigravity') {
      return {
        ...item,
        status: agyStatus,
        version: '1.5.18',
        auth_supported: true,
        managed: {
          managed: true,
          installed: true,
          installed_version: '1.5.18',
          update_available: false,
          state: 'idle',
          last_checked_at: '2026-11-26T14:00:00Z',
        },
      }
    }
    return item
  })

  await page.route('**/api/agent-runtimes', async (route) => {
    await route.fulfill({ json: { items: buildCatalogItems() } })
  })
  await page.route('**/api/agent-runtimes/*/models', async (route) => {
    await route.fulfill({ json: { items: [{ id: 'cli-model', display_name: 'CLI Model', efforts: [] }], default_id: 'cli-model' } })
  })
  await page.route('**/api/agent-runtimes/codex/cli/install', async (route) => {
    installPollCount = 0
    await route.fulfill({
      status: 202,
      json: {
        managed: true,
        installed: false,
        update_available: false,
        state: 'installing',
        bytes_downloaded: 10 * 1024 * 1024,
        total_bytes: 100 * 1024 * 1024,
      },
    })
  })
  await page.route('**/api/agent-runtimes/codex/cli/status', async (route) => {
    installPollCount += 1
    if (installPollCount === 1) {
      await route.fulfill({
        json: {
          managed: {
            managed: true,
            installed: false,
            update_available: false,
            state: 'installing',
            bytes_downloaded: 60 * 1024 * 1024,
            total_bytes: 100 * 1024 * 1024,
          },
        },
      })
      return
    }
    codexInstalled = true
    codexStatus = 'auth_required'
    const engine = buildCatalogItems().find((it: Record<string, unknown>) => it.id === 'codex')
    await route.fulfill({ json: { managed: engine.managed, engine } })
  })
  await page.route('**/api/agent-runtimes/codex/auth/start', async (route) => {
    codexAuthPollCount = 0
    await route.fulfill({
      json: {
        session_id: 'sess-codex-browser',
        engine: 'codex',
        mode: 'device_code',
        status: 'awaiting_user',
        auth_url: 'https://auth.openai.com/codex/device?very_long_query_param=1234567890abcdefghijklmnopqrstuvwxyz1234567890',
        user_code: 'ABCD-1234',
        hint_key: 'agentRuntime.auth.codexDeviceHint',
        expires_at: '2026-11-26T16:00:00Z',
      },
    })
  })
  await page.route('**/api/agent-runtimes/codex/auth/sess-codex-browser', async (route) => {
    codexAuthPollCount += 1
    if (codexAuthPollCount < 2) {
      await route.fulfill({
        json: {
          session_id: 'sess-codex-browser',
          engine: 'codex',
          mode: 'device_code',
          status: 'awaiting_user',
          auth_url: 'https://auth.openai.com/codex/device?very_long_query_param=1234567890abcdefghijklmnopqrstuvwxyz1234567890',
          user_code: 'ABCD-1234',
          hint_key: 'agentRuntime.auth.codexDeviceHint',
          expires_at: '2026-11-26T16:00:00Z',
        },
      })
      return
    }
    codexStatus = 'ready'
    const engine = buildCatalogItems().find((it: Record<string, unknown>) => it.id === 'codex')
    await route.fulfill({
      json: {
        session_id: 'sess-codex-browser',
        engine: 'codex',
        mode: 'device_code',
        status: 'succeeded',
        engine_state: engine,
        expires_at: '2026-11-26T16:00:00Z',
      },
    })
  })
  await page.route('**/api/agent-runtimes/claude/auth/start', async (route) => {
    const payload = route.request().postDataJSON() as { method?: string }
    await route.fulfill({
      json: {
        session_id: 'sess-claude-browser',
        engine: 'claude',
        mode: 'code_paste',
        status: 'awaiting_user',
        auth_url: `https://claude.com/cai/oauth/authorize?method=${payload?.method ?? 'claudeai'}&redirect_uri=http%3A%2F%2Flocalhost%3A48123%2Fcallback`,
        expires_at: '2026-11-26T16:00:00Z',
      },
    })
  })
  await page.route('**/api/agent-runtimes/claude/auth/sess-claude-browser', async (route) => {
    await route.fulfill({
      json: {
        session_id: 'sess-claude-browser',
        engine: 'claude',
        mode: 'code_paste',
        status: 'awaiting_user',
        auth_url: 'https://claude.com/cai/oauth/authorize?method=console&redirect_uri=http%3A%2F%2Flocalhost%3A48123%2Fcallback',
        expires_at: '2026-11-26T16:00:00Z',
      },
    })
  })
  await page.route('**/api/agent-runtimes/claude/auth/sess-claude-browser/cancel', async (route) => {
    await route.fulfill({
      json: {
        session_id: 'sess-claude-browser',
        engine: 'claude',
        mode: 'code_paste',
        status: 'cancelled',
        expires_at: '2026-11-26T16:00:00Z',
      },
    })
  })
  await page.route('**/api/agent-runtimes/antigravity/auth/logout', async (route) => {
    agyStatus = 'auth_required'
    const engine = buildCatalogItems().find((it: Record<string, unknown>) => it.id === 'antigravity')
    await route.fulfill({ json: engine })
  })

  await page.goto('/')
  await page.getByLabel('工作台侧边栏').getByRole('button', { name: 'Agents', exact: true }).click()
  await page.getByRole('tab', { name: '运行时', exact: true }).click()

  const runtime = page.locator('[data-agent-configuration-section="runtime"]')
  await expect(runtime.getByRole('button', { name: '安装', exact: true })).toBeVisible()
  await expect(runtime.getByRole('button', { name: '登录', exact: true })).toHaveCount(0)

  // Trigger Codex install -> progress -> installed & ready to sign in.
  await runtime.getByRole('button', { name: '安装', exact: true }).click()
  await expect(runtime.getByText('正在下载并安装…', { exact: true })).toBeVisible()
  await expect(runtime.getByText('v0.135.0', { exact: true })).toBeVisible({ timeout: 10_000 })
  await expect(runtime.getByRole('button', { name: '登录', exact: true })).toBeVisible()

  // Open Codex device code dialog, verify code + long URL + dark wide screenshot, then allow completion.
  await runtime.getByRole('button', { name: '登录', exact: true }).click()
  const dialog = page.getByRole('dialog')
  await expect(dialog.getByText('ABCD-1234', { exact: true })).toBeVisible()
  await expect(dialog.getByRole('link', { name: '打开登录链接', exact: true })).toBeVisible()
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true)
  await page.screenshot({ path: testInfo.outputPath('codex-auth-dialog-dark-wide.png'), animations: 'disabled' })
  await expect(dialog).toBeHidden({ timeout: 10_000 })
  await expect(runtime.getByRole('button', { name: '退出登录', exact: true })).toBeVisible()

  // Switch to Claude Code: verify update button + pending reload + Claude OAuth dialog across light & narrow viewports.
  const engineSelect = page.getByRole('combobox', { name: '执行引擎', exact: true })
  await engineSelect.click()
  await page.getByRole('option', { name: 'Claude Code', exact: true }).click()
  await expect(runtime.getByRole('button', { name: '更新至 v2.1.332', exact: true })).toBeVisible()
  await expect(runtime.getByText('新版本已安装，当前回合结束后自动生效。', { exact: true })).toBeVisible()

  await runtime.getByRole('button', { name: '登录', exact: true }).click()
  await expect(dialog.getByRole('radio', { name: 'Claude 订阅', exact: true })).toBeVisible()
  await dialog.getByRole('radio', { name: 'Anthropic Console', exact: true }).click()
  await dialog.getByRole('button', { name: '开始登录', exact: true }).click()
  await expect(dialog.getByLabel('授权码或回调链接', { exact: true })).toBeVisible()

  // Check narrow dark & narrow light layout on the code-paste dialog without horizontal overflow.
  await page.setViewportSize({ width: 390, height: 844 })
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true)
  await page.screenshot({ path: testInfo.outputPath('claude-auth-dialog-dark-narrow.png'), animations: 'disabled' })
  await dialog.getByRole('button', { name: '取消', exact: true }).click()
  await expect(dialog).toBeHidden()

  // Switch theme to light and verify Antigravity runtime controls + sign-out in both wide and narrow viewports.
  const latest = await (await request.get('/api/settings')).json()
  expect((await request.patch('/api/settings', {
    data: {
      layer: 'user',
      base_revision: latest.revisions.user,
      changes: { theme: 'light', agent_runtimes: { ide: { selected: 'antigravity' } } },
    },
  })).ok()).toBe(true)
  await page.setViewportSize({ width: 1280, height: 900 })
  await page.reload()
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'light')
  await page.getByRole('tab', { name: '运行时', exact: true }).click()
  await expect(runtime.getByText('v1.5.18', { exact: true })).toBeVisible()
  await expect(runtime.getByText('已是最新', { exact: true })).toBeVisible()
  await expect(runtime.getByRole('button', { name: '退出登录', exact: true })).toBeVisible()
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true)
  await page.screenshot({ path: testInfo.outputPath('antigravity-runtime-light-wide.png'), animations: 'disabled' })

  await runtime.getByRole('button', { name: '退出登录', exact: true }).click()
  await expect(runtime.getByRole('button', { name: '登录', exact: true })).toBeVisible()
  await page.setViewportSize({ width: 390, height: 844 })
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true)
  await page.screenshot({ path: testInfo.outputPath('antigravity-runtime-light-narrow.png'), animations: 'disabled' })
})

test('Settings exposes agent_cli_auto_update only in managed CLI mode and lists Antigravity CLI terminal preset', async ({ page, request }, testInfo) => {
  test.setTimeout(90_000)
  await page.emulateMedia({ reducedMotion: 'reduce' })
  await createAndOpenBook(request, 'Settings CLI Preset Browser Book')

  let managedFlag = false
  await page.route('**/api/settings', async (route) => {
    const response = await route.fetch()
    const body = await response.json()
    await route.fulfill({
      response,
      json: {
        ...body,
        runtime: { ...body.runtime, managed_agent_clis: managedFlag },
      },
    })
  })

  await page.goto('/')
  const sidebar = page.getByLabel('工作台侧边栏')

  // Verify Writing <-> Game <-> Settings navigation works cleanly.
  await sidebar.getByRole('button', { name: '游戏', exact: true }).click()
  await expect(sidebar.getByRole('button', { name: '游戏', exact: true })).toHaveAttribute('aria-current', 'page')
  await sidebar.getByRole('button', { name: '设置', exact: true }).click()

  // Terminal presets include Antigravity CLI (agy) alongside Codex and Claude Code.
  await page.getByRole('button', { name: '终端', exact: true }).click()
  const cards = page.getByTestId('terminal-command-card')
  await expect(cards.filter({ hasText: 'Antigravity CLI' })).toBeVisible()
  await expect(cards.filter({ hasText: 'Antigravity CLI' })).toContainText('$ agy')
  await expect(cards.filter({ hasText: 'Antigravity CLI' })).toContainText('预置')
  await page.screenshot({ path: testInfo.outputPath('terminal-presets-agy.png'), animations: 'disabled' })

  // Updates hides external CLI auto-update toggle when managed_agent_clis is false.
  await page.getByRole('button', { name: '软件更新', exact: true }).click()
  await expect(page.getByRole('heading', { name: '外部 Agent CLI', exact: true })).toHaveCount(0)

  // Enable managed_agent_clis and reload: toggle appears and persists real PATCH /api/settings.
  managedFlag = true
  await page.reload()
  await page.getByRole('button', { name: '软件更新', exact: true }).click()
  await expect(page.getByRole('heading', { name: '外部 Agent CLI', exact: true })).toBeVisible()
  const cliAutoUpdate = page.getByRole('combobox', { name: '自动更新已安装的 CLI', exact: true })
  await expect(cliAutoUpdate).toBeVisible()
  await cliAutoUpdate.click()
  const saved = page.waitForResponse((res) => res.url().endsWith('/api/settings') && res.request().method() === 'PATCH')
  await page.getByRole('option', { name: '关闭', exact: true }).click()
  expect((await saved).ok()).toBe(true)
  await expect.poll(async () => (await (await request.get('/api/settings')).json()).user.agent_cli_auto_update).toBe(false)
  await page.screenshot({ path: testInfo.outputPath('updates-agent-cli-auto-update.png'), animations: 'disabled' })
})
