<p align="center">
  <img src="./web/public/favicon.svg" alt="Denova 图标" width="76" height="76">
</p>

<p align="center">
  <strong>Denova Docker 版 — 面向小说写作与 AI 角色扮演游戏的一体化 AI 创作平台容器镜像</strong>
</p>

<p align="center">
  <a href="README.en.md">English</a> | 中文
</p>

<p align="center">
  <a href="https://github.com/brsbsc/denova/pkgs/container/denova"><img alt="GHCR" src="https://img.shields.io/badge/GHCR-ghcr.io%2Fbrsbsc%2Fdenova-2496ED?style=flat-square&logo=docker&logoColor=white"></a>
  <img alt="Arch" src="https://img.shields.io/badge/Arch-linux%2Famd64%20%7C%20linux%2Farm64-0db7ed?style=flat-square">
  <a href="./LICENSE"><img alt="License" src="https://img.shields.io/github/license/alfredxw/denova?style=flat-square"></a>
</p>

<p align="center">
  当前镜像基线：<strong>v0.8.0fix2</strong> · 仅发布 Docker 镜像 · <a href="https://github.com/brsbsc/denova/pkgs/container/denova">查看 GHCR 镜像</a>
</p>

> [!NOTE]
> 本仓库是 [alfredxw/denova](https://github.com/alfredxw/denova) 的纯 **Docker** 版本，仅构建和发布 `ghcr.io/brsbsc/denova` 多架构容器镜像（`linux/amd64`、`linux/arm64`），不提供 Windows / macOS 桌面端安装包与 GitHub Releases，不接受议题（Issue）和拉取请求（Pull Request）。如需桌面端安装包或反馈应用问题，请前往上游仓库 [alfredxw/denova](https://github.com/alfredxw/denova)。

![Denova 写作](./img/ide.png)

<details>
<summary>查看更多界面截图</summary>

### 游戏

![Denova 游戏](./img/interactive.png)

### 资料库

![Denova 资料库](./img/setting.png)

### 创作方案

![Denova 创作方案](./img/story-teller.png)

### 修改审阅

![Denova 修改审阅](./img/review.png)

### 工作台

![Denova 工作台](./img/workspace.png)

支持局域网与公网访问、手机自适应布局及 PWA 添加到主屏幕。

<img src="./img/mobile.png" alt="Denova 手机布局" width="360">

</details>

## Docker 版特性

- **开箱即用的多架构镜像**：提供 `ghcr.io/brsbsc/denova:latest`（以及固定版本标签如 `v0.8.0fix2`），同时支持 `linux/amd64` 与 `linux/arm64`。
- **非 root 安全运行**：启动脚本自动修复受管数据目录属主后降权至 `UID/GID 10001:10001` 运行，默认启用远程访问密码保护。
- **容器内外部 Agent CLI 托管与网页 OAuth**：
  - 镜像保持轻量（内置 Bash、Git、curl、Python、ripgrep），不预装体积庞大的外部 Agent CLI。
  - 默认开启容器沙箱适配（`DENOVA_CONTAINER_SANDBOX=1`），在标准非特权容器（无需 `--privileged`）下即可运行 **Antigravity CLI（`agy`）**、**Claude Code（`claude`）** 与 **Codex（`codex`）**。
  - 可直接在 **设置 → Agents → 运行时** 中按需下载校验并安装/更新到持久化卷 `/data/.local/bin`，并通过统一 **网页 OAuth** 完成登录与退出登录（凭据持久保存在 `/data` 下，重建容器不丢失），也可在内置终端使用 `agy`、`claude`、`codex` 预设命令。
  - 支持在 **设置 → 更新** 中开关已安装 CLI 的后台自动更新（每 24 小时检查一次，仅更新已安装的 CLI）。
- **隐私默认关闭统计**：默认关闭使用统计且无首次启动弹窗，可在 **设置 → 常规** 中随时开关（口径见 [使用统计说明](./docs/analytics.md)）。

## 快速开始

首次启动必须通过环境变量设置登录账号：`DENOVA_USERNAME` 与 `DENOVA_PASSWORD`（密码需为 1–72 个 UTF-8 字节）。这两个变量仅在数据卷尚未生成 `config.toml` 时生效，初始化后如需修改密码请在网页设置中操作。

### 方式一：Docker Compose（推荐）

在仓库根目录创建 `docker/.env`（已被 Git 忽略）：

```dotenv
DENOVA_USERNAME=admin
DENOVA_PASSWORD=replace-with-your-own-password
DENOVA_IMAGE_TAG=latest
```

使用仓库内置的 [docker/compose.yml](docker/compose.yml) 启动：

```bash
docker compose --env-file docker/.env -f docker/compose.yml up -d
```

> [docker/compose.yml](docker/compose.yml) 默认只绑定宿主机 `127.0.0.1:8080`。如需局域网直接访问，可将端口映射改为 `8080:8080`；公网访问请配合 HTTPS 反向代理。

### 方式二：docker run

```bash
docker run -d --name denova \
  -p 8080:8080 \
  -e DENOVA_USERNAME=admin \
  -e DENOVA_PASSWORD=replace-with-your-own-password \
  -v denova-data:/data \
  --restart unless-stopped \
  ghcr.io/brsbsc/denova:latest
```

启动后访问 `http://localhost:8080` 并使用上述账号登录。

### 首次使用

1. 按引导添加语言模型的服务商连接、API Key 与模型名（同一连接可复用于多个模型；Google Gemini 可选「Gemini 原生协议」）。
2. 如需使用外部 CLI 引擎（Antigravity CLI / Claude Code / Codex），进入 **设置 → Agents → 运行时** 按需安装并通过网页 OAuth 登录。
3. 创建或导入作品，从「写作」开始创作，或从「游戏」开启互动故事；需要配图时再配置图像模型。

## 环境变量与数据卷

### 常用环境变量

| 变量 | 说明 | 默认值 |
| --- | --- | --- |
| `DENOVA_USERNAME` | 首次启动初始化登录用户名（已有 `config.toml` 时忽略） | 无（首次必填） |
| `DENOVA_PASSWORD` | 首次启动初始化登录密码，1–72 UTF-8 字节（已有 `config.toml` 时忽略） | 无（首次必填） |
| `DENOVA_IMAGE_TAG` | Compose 使用的镜像标签（如 `latest`、`v0.8.0fix2`） | `latest` |
| `DENOVA_CONTAINER_SANDBOX` | 容器非特权沙箱适配开关 | `1`（镜像内置） |
| `DENOVA_MANAGED_CLI_DIR` | 按需安装的外部 Agent CLI 持久化目录 | `/data/.local/bin`（镜像内置） |

### 数据目录与权限

所有持久化数据均位于 `/data`（默认使用命名卷 `denova-data`）：

- `/data/.denova`：应用配置（`config.toml`）、项目数据、会话与版本记录
- `/data/log`：运行日志
- `/data/.local/bin`：通过网页设置按需安装的外部 Agent CLI 二进制
- `/data/.gemini`、`/data/.claude`（及 `/data/.claude.json`）、`/data/.codex`：各外部 CLI 的 OAuth 登录凭据与配置

权限与挂载注意事项：

- 容器默认以 root 启动入口脚本，仅调整上述受管目录属主为 `10001:10001`（不递归修改 `/data` 下其他用户目录，拒绝符号链接），随后降权至 `10001:10001` 运行 Denova。
- 使用宿主目录绑定挂载（如 `-v /path/to/denova:/data`）时，文件系统需允许 `chown`；若是禁止 `chown` 的 NFS / NAS，请先在宿主机赋予 UID `10001` 读写权限，再为容器指定 `--user 10001:10001`（Compose 为 `user: "10001:10001"`）跳过权限初始化。
- 如需打开外部项目目录，请将其额外挂载到容器内（例如 `-v /host/projects:/projects`）。
- 导入已有 `config.toml` 时，必须确保已启用 `allow_lan_access` 并配置 `remote_access_username` 与 `remote_access_password_hash`；切勿在容器内关闭局域网访问，否则端口将不可达。

## 反向代理与 HTTPS

公网或域名部署时，请在容器前配置 Caddy、Nginx 等 HTTPS 反向代理，避免明文传输登录凭据。

当反向代理与 Denova 运行在同一台宿主机时，**必须转发原始 `Host` 或 `X-Forwarded-For`**，否则 Denova 会将代理请求误判为本机访问并跳过登录校验。Nginx 最小示例：

```nginx
location / {
    proxy_pass http://127.0.0.1:8080;
    proxy_http_version 1.1;
    proxy_set_header Upgrade $http_upgrade;
    proxy_set_header Connection "upgrade";
    proxy_set_header Host $host;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto $scheme;
}
```

## 更新、回退与备份

**Docker Compose 更新：**

```bash
docker compose --env-file docker/.env -f docker/compose.yml pull
docker compose --env-file docker/.env -f docker/compose.yml up -d
```

**docker run 更新：**

```bash
docker pull ghcr.io/brsbsc/denova:latest
docker rm -f denova
# 使用与之前相同的 -v denova-data:/data 参数重新运行容器
```

- 升级前建议先备份 `denova-data` 数据卷。
- **不要**在容器内使用应用内置更新器替换程序，也**不要**执行 `docker compose down -v`（会删除数据卷）。
- 回退版本时，将 `DENOVA_IMAGE_TAG` 改回旧标签并重建容器；注意回退镜像不会自动回退已升级的数据格式。

## Denova 核心能力

- **小说写作**：Markdown 文档与源码编辑、多文档 Tab、查找替换、大纲与章节细纲、进度追踪、正文评论、修改审阅和现有小说导入。
- **创作 Agent**：支持 Native、Codex、Claude Code、Grok Build 和 Antigravity CLI，结合选区、项目文件和资料库创作，支持多会话与暂停恢复。详见 [CLI 运行时说明](docs/cli-runtimes.md)。
- **互动游戏**：分支剧情、故事线切换、行动建议、角色与世界状态、规则检定、剧情规划、语音朗读及资料库舞台演出。
- **资料库**：自定义分类、文档式索引、`@` 引用与反向引用、正文提及检索，以及多图、音频与封面管理。
- **创作方案、Skills 与资源中心**：复用叙事/图像风格与规则，通过 Skills 扩展 Agent 能力，支持资源导入导出、版本更新审阅与备份恢复。
- **插件、图像创作、版本与自动化**：支持插件与自定义游戏扩展、多服务商图像生成、本地版本差异/回滚及定时自动化任务。

## 镜像构建与发布说明

关于多架构镜像构建、CI 校验、上游版本同步及本地构建步骤，请参阅 [容器发布说明](docs/container-publishing.md)。

## License

[Apache-2.0](./LICENSE)
