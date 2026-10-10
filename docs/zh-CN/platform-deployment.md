# 平台部署边界与落地方式

## 先给结论

| 平台 | 能做什么 | 不能直接做什么 | 本项目的落地方式 |
| --- | --- | --- | --- |
| GitHub 仓库 | 保存源码、配置 Compose、触发审计 | 不能把仓库本身当作常驻 Go/Docker 主机 | GitHub Actions 构建并验证镜像，GHCR 保存发布镜像 |
| GitHub Actions | 每次提交构建控制面和 8 个工具族镜像 | Runner 是任务执行环境，不是 24 小时业务服务 | 用于 CI、发布和供应链证明 |
| 任意 Docker 主机/VM | 运行控制面、MCP 和扫描器 | 需要自行提供磁盘、网络和授权边界 | `deploy/start-stack.sh` 一条命令启动 |
| Cloudflare Tunnel | 给已有 Docker 主机提供 HTTPS、Access 和无公网 IP 入口 | 不承载 Docker Compose 或扫描器进程 | VM 上运行本项目，Cloudflare 只做入口和访问控制 |
| Cloudflare Workers/Pages | Worker、静态页面和轻量 API | 不能直接运行当前 Go + Compose + Kali 工具平面 | 不作为当前版本的运行平台 |
| Cloudflare Containers | 可以运行单个 Linux/amd64 容器 | 当前多容器 Compose、持久化目录、工具网络/权限仍需适配 | 作为后续适配目标，不宣称当前版本可直接部署 |

GitHub 是代码和镜像交付面，实际扫描必须落在有明确网络出口、磁盘和授权控制的运行主机上。不要把 GitHub Actions Runner 当成生产扫描节点。

## 本地或 Linux VM：一条命令启动

先准备 Docker Compose、一个 AI 通道和已授权目标范围：

```bash
git clone https://github.com/xiaochen201807/CyberStrikeAI.git
cd CyberStrikeAI
cp deploy/tools/.env.example deploy/tools/.env
openssl rand -hex 32
```

编辑 `deploy/tools/.env`，至少设置：

- `TOOL_MCP_TOKEN`：上一步生成的随机值；
- `TOOL_ALLOWED_HOSTS`：精确主机或限定子域名，例如 `authorized.example.com,*.authorized.example.com`，不能写 `*`；
- `RUNTIME_PROFILES` 不写时默认只启动 `recon` 和 `web`。

然后执行：

```bash
./deploy/start-stack.sh
```

脚本先启动控制平面建立共享网络，再启动选中的工具族。要启用全部已经通过镜像验收的工具族：

```bash
RUNTIME_PROFILES="runtime-recon runtime-web runtime-identity runtime-binary runtime-cloud runtime-exploitation runtime-external-intel runtime-utility" \
  ./deploy/start-stack.sh
```

检查状态：

```bash
docker compose --env-file deploy/tools/.env -f deploy/control/docker-compose.yml ps
docker compose --env-file deploy/tools/.env -f deploy/tools/docker-compose.runtime.yml ps
curl -kfsS https://127.0.0.1:8080/healthz
```

控制台默认只绑定 `127.0.0.1`。首次启动后在 Web 界面完成管理员密码和 AI 通道配置，再登记外部 MCP 服务。工具容器不发布宿主机端口，控制面通过 Docker 内部网络访问它们。

## GitHub Actions + GHCR：构建和发布

提交到 `main` 会触发工具平面契约验证和真实 BuildKit 构建。发布镜像需要打版本标签：

```bash
git tag v0.1.0
git push origin v0.1.0
```

`.github/workflows/publish-tool-images.yml` 会发布：

- 控制面镜像 `ghcr.io/<owner>/cyberstrike-ai`；
- 8 个工具族镜像 `ghcr.io/<owner>/cyberstrike-tool-runtime-<family>`；
- Nuclei、ZAP 固定适配器镜像。

在实际 Docker 主机上使用 GHCR 镜像时：

1. `docker login ghcr.io`；
2. 在 `deploy/tools/.env` 填写 `CYBERSTRIKE_IMAGE` 和需要启用的族镜像，优先使用带 `sha256` 的不可变引用；
3. 保持 `TOOL_MCP_TOKEN`、`TOOL_ALLOWED_HOSTS` 和控制面配置一致；
4. 使用 `BUILD_IMAGES=0 ./deploy/start-stack.sh`，让 Compose 拉取已经构建好的镜像。

GitHub Actions 的构建成功只代表镜像和契约通过；它不会替你保存 `config.yaml`、SQLite 数据库、报告或长期运行服务。

## Cloudflare：推荐 Tunnel，而不是把扫描器放进 Worker

推荐拓扑：

```text
浏览器 ── Cloudflare Access/Tunnel ──> Docker VM
                                      ├─ CyberStrikeAI 控制面
                                      └─ 独立工具族 MCP 容器
```

在 VM 上先按上一节启动，并让控制面只监听本机：

```bash
CYBERSTRIKE_BIND=127.0.0.1 ./deploy/start-stack.sh
```

再在同一台 VM 安装并运行 `cloudflared`，把隧道服务指向 `http://127.0.0.1:8080`，在 Cloudflare Access 中只允许测试团队身份。工具 MCP 端口不要发布到公网。扫描器访问授权目标时使用 VM 的出口网络，不会自动经过 Cloudflare Tunnel。

Cloudflare Tunnel 是入口层；它不替换 Docker、网络隔离、HITL、目标白名单或工具运行审计。

## Cloudflare Containers 的当前边界

Cloudflare Containers 可以通过 Worker/ Durable Object 拉起 Linux/amd64 镜像，但需要额外的 Worker 路由、容器生命周期、密钥和持久化设计。当前仓库没有把控制面、8 个 MCP 服务和共享 Compose 网络转换为 Cloudflare Containers 部署单元，因此不能用 `wrangler deploy` 直接启动本项目。

此外，工具族镜像含 Kali 用户态和安全工具，实际能力还取决于容器平台是否允许所需的出站网络、文件持久化和内核/套接字行为。若要做 Cloudflare Containers 适配，应单独验收每个族，不应把 Workers/Pages 的部署成功等同于扫描器可用。

## 发布前验收清单

```bash
go test ./...
go vet ./cmd/... ./internal/...
REQUIRE_IMAGE_DIGESTS=1 ./deploy/tools/scripts/validate.sh
docker compose -f deploy/control/docker-compose.yml config --quiet
docker compose -f deploy/tools/docker-compose.runtime.yml --env-file deploy/tools/.env config --quiet
```

每次新增或修改 `tools/*.yaml` 后，先更新 `deploy/tools/inventory.yaml`，再让 GitHub Actions 构建对应工具族；只有族镜像中的 `toolcatalog doctor --strict` 通过，才把它当成已落地执行能力。
