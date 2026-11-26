# 上游 Release 同步与 GHCR 镜像

本 fork 的 `.github/workflows/sync-upstream-release.yml` 每小时第 23 分钟（UTC）检查 `alfredxw/denova` 最新正式 Release。GitHub 的定时任务可能延迟执行。只接受 `v主版本.次版本.补丁版本`，跳过预发布，不自动合并上游分支。

## 首次启用

1. 将本次改动推送到 `BRSBSC/denova` 的默认分支 `master`。
2. 在仓库 **Actions** 页面启用 fork 的工作流。若第一次推送没有触发，选择 **Sync upstream release and publish GHCR → Run workflow**。
3. 工作流使用内置 `GITHUB_TOKEN`，只需要仓库允许 `packages: write`，无需另存 PAT，也不会创建或修改 GitHub Releases。若组织策略限制写权限，需要管理员放行；若同名 GHCR 包已存在，需在该包的 Actions access 中授权此仓库。
4. 首次发布后，在 **Packages → denova → Package settings** 中检查可见性。希望其他人无需登录即可拉取时，将包设置为 Public；公开仓库不代表包一定公开。
5. 查看本次 Actions 运行日志和 Summary，确认镜像发布成功。公开仓库长时间无活动可能被 GitHub 停用定时工作流，需要在 Actions 中重新启用。

镜像地址：`ghcr.io/brsbsc/denova:v0.4.5`（版本示例），支持 `linux/amd64`、`linux/arm64`。上游同步只发布版本镜像，不创建 GitHub Releases；`ghcr.io/brsbsc/denova:latest` 只由本 fork 的源码发布移动，见下文。发布 job 仅允许在 `BRSBSC/denova` 执行；迁移到其他 fork 时须修改工作流仓库条件与 Compose 镜像地址。

## 发布与失败处理

- 仅下载上游 Release 的 `checksums.txt` 与 Linux 双架构安装包（`denova*-linux-*.tar.gz`），逐一校验 SHA-256；Linux 两种架构缺失、校验失败或归档路径不安全时停止。
- 使用已发布的 Linux 安装包构建镜像，保留前端、Skills、updater 和许可证，不重新编译应用源码。容器额外安装 Bash、Git、curl、Python、ripgrep；镜像保持轻量，不预装外部 Agent CLI（Antigravity CLI、Claude Code、Codex），可在运行后于网页设置中按需安装到数据卷；其他语言工具链可按需扩展镜像。
- 应用源码修复必须先进入镜像所使用的 Linux 安装包。只推送本 fork 的应用源码补丁或对原版本执行 `force` 重建，不会将补丁编译进镜像；应同步包含修复的上游 Release，或另行构建并验证包含补丁的安装包与镜像。
- 分别构建和测试两种架构：版本号、HTTP 页面、未登录访问拒绝、登录、设置读取、`/data/.local/bin` 权限以及重启后的配置与登录会话保留（本 fork 源码构建还会额外校验外部 Agent 运行时托管与网页 OAuth 能力）。测试通过后推送版本镜像，并写入 `io.denova.upstream.mirror` 与 `io.denova.upstream.digest` 两个 OCI 标签用于幂等去重。
- 同步仅检查 GHCR 镜像标签：若目标版本已存在且不含 `io.denova.upstream.mirror` 标签（即本 fork 源码发布的镜像，如 `v0.8.0`），永远跳过、不覆盖；同步也不会移动 `latest` 标签，更不会创建任何 GitHub Releases。
- 每次定时任务只处理当时最新正式 Release，不补齐所有历史版本。手动输入 `tag` 可补指定版本，勾选 `force` 可重建同一上游镜像。修改容器或同步流程并推送 `master` 时，也会触发检查并按需重建。

这条流程完全面向 Docker 镜像：不生成桌面安装包，也不写入 GitHub Releases。

## 运行容器

在 `docker/.env` 中设置首次登录账号（此文件已被 Git 忽略）：

```dotenv
DENOVA_USERNAME=admin
DENOVA_PASSWORD=replace-with-your-own-password
DENOVA_IMAGE_TAG=latest
```

密码需为 1–72 个 UTF-8 字节。在仓库根目录执行，PowerShell 与 Bash 命令相同：

```sh
docker compose --env-file docker/.env -f docker/compose.yml up -d
```

打开 `http://localhost:8080`，使用上述账号登录。Compose 默认只发布宿主机回环地址；需要局域网访问时，将端口映射改为 `8080:8080`，需要互联网访问时配合 HTTPS 反向代理。

容器启动时先准备数据卷权限，再以 UID/GID `10001:10001` 运行应用。命名卷 `denova-data` 保存 `/data`，其中 `/data/.denova` 是应用数据、`/data/log` 是日志、`/data/.local/bin` 保存按需安装的外部 Agent CLI 二进制，`/data/.gemini`、`/data/.claude`、`/data/.codex`（及已存在时的 `/data/.local/share/keyrings`）保存对应 CLI 凭据。初始化会拒绝上述受管路径为符号链接，确保 `/data/.local/bin` 存在，并仅调整受管目录内文件的属主，保留文件内容和原有权限位，不会递归修改 `/data` 下其他用户目录或跟随符号链接。`DENOVA_DIR` 必须位于 `/data` 内。手动绑定宿主目录时，需要允许初始化修改文件属主；只读挂载或限制 chown 的 NFS/NAS 文件系统需要在宿主机预先设置该 UID 的读写权限，再通过 Compose `user: "10001:10001"` 跳过权限初始化。外部 Project 如需使用，应另外挂载其目录；命名卷不包含未挂载的宿主文件。

镜像默认开启容器沙箱适配（`DENOVA_CONTAINER_SANDBOX=1`）与托管 CLI 目录（`DENOVA_MANAGED_CLI_DIR=/data/.local/bin`），在标准 `docker compose up`（非特权 UID `10001` 与默认 seccomp）下即可运行 Antigravity CLI（`agy`）、Claude Code（`claude`）和 Codex（`codex`），无需 `--privileged`。如需使用外部引擎，可在 **设置 → Agents → 运行时** 中按需从官方源校验下载安装到 `/data/.local/bin`、检查更新或一键更新，并直接通过网页 OAuth 完成登录与退出登录（凭据保存在 `/data` 下，重建容器不丢失），也可在内置终端中使用 `codex`、`claude`、`agy` 快捷预设。后台自动更新仅覆盖已安装的托管 CLI，可在 **设置 → 更新** 中开关；容器已禁用各 CLI 自身后台自更新（`AGY_CLI_DISABLE_AUTO_UPDATE=true`、`DISABLE_AUTOUPDATER=1`），避免与 Denova 托管更新器冲突。

首次启动仅在数据目录没有 `config.toml` 时写入网络监听设置和 bcrypt 密码摘要；已有配置不会被覆盖。账号环境变量只用于首次初始化，之后更改密码应使用应用设置。导入旧数据时，需要确保其用户配置已启用 `allow_lan_access`，并设置 `remote_access_username` 与 `remote_access_password_hash`。不要在容器设置中关闭局域网访问，否则容器端口不可达。

更新容器使用以下命令；数据卷保留。不要使用应用内 updater 修改镜像内程序，也不要执行 `down -v`，后者会删除数据卷。

```sh
docker compose --env-file docker/.env -f docker/compose.yml pull
docker compose --env-file docker/.env -f docker/compose.yml up -d
```

回退时将 `DENOVA_IMAGE_TAG` 改为旧版本后重新创建容器。应用数据格式可能随版本变化，升级前应备份数据卷；回退镜像不等于回退数据。

## 本 fork 的源码修复版

`Release` 工作流按 `v*` 标签构建当前标签源码，不使用上游安装包。先验证相同提交的完整 Linux CI，再仅生成 `linux-amd64` 与 `linux-arm64` 安装包用于容器打包，完成双架构镜像构建与冒烟测试后推送版本标签和 `latest`（不再构建 Windows/macOS 安装包或桌面端）。镜像的 revision 标签记录实际源码提交。

`v0.8.0fix2` 对应镜像为 `ghcr.io/brsbsc/denova:v0.8.0fix2`。在 `docker/.env` 中将 `DENOVA_IMAGE_TAG` 设置为 `v0.8.0fix2`，再执行上文更新命令。`latest` 始终指向最近一次源码发布，不受上游同步影响；需要运行未修改的上游构建时，将 `DENOVA_IMAGE_TAG` 设为上游版本号（例如 `v0.5.1`）。本 fork 自己发布过的版本号（例如 `v0.8.0`）始终是本 fork 的源码构建，上游同步会跳过同名版本。

上游同步仍只处理上游正式版；后续发现新上游版本时，会继续发布其版本镜像，但不改变 `latest`，也不创建 GitHub Releases。

## 验证入口

```sh
python -B -m unittest discover -s scripts -p test_sync_upstream_release.py
python -B -m unittest discover -s docker -p test_entrypoint.py
bash -n docker/smoke-test.sh
sh -n docker/prepare-data.sh
```

真实容器构建和双架构冒烟测试由上述发布工作流执行，使用隔离数据卷，不使用开发服务、真实模型配置或真实用户数据。PR 仅执行脚本测试与语法检查，不发布镜像。

参考：[GitHub 容器仓库权限与可见性](https://docs.github.com/en/packages/working-with-a-github-packages-registry/working-with-the-container-registry)、[Docker 多平台构建](https://docs.docker.com/build/building/multi-platform/)、[GitHub 定时事件](https://docs.github.com/en/actions/reference/workflows-and-actions/events-that-trigger-workflows#schedule)。
