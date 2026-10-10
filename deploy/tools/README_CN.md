# CyberStrikeAI 工具平面

工具平面与 CyberStrikeAI 控制平面分开部署。控制平面保存项目、审批、任务、结果和完整的
`tools/*.yaml` 工具注册表；扫描器二进制使用独立镜像，通过 Compose profile 按需启动。

## 目录约定

```text
deploy/tools/
  catalog.yaml              # 工具能力、风险、目标和报告契约
  inventory.yaml            # 当前仓库 YAML 定义的完整迁移状态（精确计数与官方 100+ 口径分开记录）
  docker-compose.yml        # 工具容器运行定义
  docker-compose.runtime.yml # 8 个独立工具族运行时
  runtime/                  # 运行时构建脚本和族清单
  scripts/                  # 只读包装脚本
  data/targets/             # 目标输入
  data/reports/             # 扫描输出
```

`catalog.yaml` 是工具政策清单，`docker-compose.yml` 是运行清单。两者不能
互相替代：Compose 不表达项目白名单、人工审批和结果格式。

当前仓库提供 Nuclei/ZAP 固定适配器，以及按 recon、web、identity、binary-forensics、cloud、
exploitation、external-intel、utility 拆分的 8 个独立运行时镜像。仓库原有 `tools/*.yaml` 仍然完整
保留并由控制平面加载；`inventory.yaml` 会把 73 个可迁移工具映射到对应族镜像，3 个控制平面
Shell/Python 能力由控制面镜像提供，12 个定义保持禁用。每个族镜像构建末尾都会运行
`toolcatalog doctor --strict`，
缺少命令会让 CI 失败，不会等到线上调用才暴露。

`inventory.yaml` 中的 `count_scope: tools/*.yaml definitions`、`total`、`enabled` 和 `disabled`
只统计本检出版本的 YAML 定义；`official_tool_surface: 100+` 对应上游 README 的产品口径，
不把内置工具、外部 MCP 联邦或后续自定义扩展误算进 YAML 文件数。

catalog 的 `profile` 是逻辑工具族；`compose_profile` 把长驻 MCP 和一次性任务分开，避免执行
`docker compose --profile web-mcp up` 时误启动批量扫描任务。

## 启动前校验

```bash
cd /path/to/CyberStrikeAI/deploy/tools
cp .env.example .env
# 用 openssl rand -hex 32 生成 TOOL_MCP_TOKEN，并写入 .env
./scripts/validate.sh
```

启用 MCP 时，`deploy/control/.env` 和这里的 `.env` 必须使用同一个 `TOOL_MCP_TOKEN`。
如果修改了 `CYBERSTRIKE_CONTROL_NETWORK`，两个 `.env` 也必须保持相同的网络名。
同时把 `TOOL_ALLOWED_HOSTS` 改成当前授权范围中的精确主机或 `*.example.com` 形式的子域名范围；
服务拒绝空列表和全局 `*`。

生产环境还应把 `.env` 中的镜像改为带 digest 的不可变引用，然后执行：

```bash
REQUIRE_IMAGE_DIGESTS=1 REQUIRE_SECURE_TOKEN=1 ./scripts/validate.sh
```

## 目标输入

把已授权的 URL 写入 `data/targets/targets.txt`，一行一个：

```text
https://authorized.example.com
https://api.authorized.example.com
```

这个文件只是工具平面的输入格式，不构成授权证明。项目白名单、测试窗口和
审批必须在 CyberStrikeAI 中完成，运行前由操作人员核对二者一致。一次性任务还会使用
`.env` 中的 `TOOL_ALLOWED_HOSTS` 在容器内再次校验主机；空列表、全局通配符和不在范围内的 URL 会被拒绝。

## 执行基线任务

```bash
# ZAP 基线只接受一个明确 URL
TARGET_URL=https://authorized.example.com \
  docker compose --profile baseline-job run --rm zap-baseline

# Nuclei 使用 targets.txt，默认限制目标数、速率和并发
docker compose --profile web-job run --rm nuclei-scan
```

任务容器不会作为常驻服务运行，扫描输出保存在 `data/reports/`。报告进入
CyberStrikeAI 前应经过人工复核。

## 连接 CyberStrikeAI 外部 MCP

控制平面 Compose 会创建 `cyberstrike-control` 网络。工具 Compose 启动前先启动控制平面，
然后按需启动长驻 MCP 工具：

```bash
docker compose --profile web-mcp up -d --build nuclei-mcp
docker compose --profile baseline-mcp up -d --build zap-mcp
```

在 CyberStrikeAI 的 `external_mcp.servers` 中登记对应服务。示例：

```yaml
external_mcp:
  servers:
    tool-plane-nuclei:
      type: sse
      url: http://nuclei-mcp:8080/sse
      headers:
        X-Tool-Token: "${TOOL_MCP_TOKEN}"
      external_mcp_enable: true
```

`tool-plane-nuclei` 只会暴露 `tool_catalog` 和 `nuclei_scan`，不会接受任意 shell 命令、镜像名
或宿主机路径。ZAP 使用 `http://zap-mcp:8080/sse`，工具名为 `zap_baseline`。

### 按工具族接入现有 YAML 定义

运行时镜像已经由仓库提供构建入口。只启动需要的工具族，服务会直接读取仓库的 `tools/*.yaml`，
复用同一套参数 schema 和命令参数映射，不需要为每个工具重新写一份 Agent 接口：

```bash
# 先启动控制平面以创建内部网络
cd ../control && docker compose up -d --build
cd ../tools
cp .env.example .env
TOOL_ALLOWED_HOSTS=authorized.example.com,*.authorized.example.com

docker compose -f docker-compose.runtime.yml --env-file .env \
  --profile runtime-recon up -d --build runtime-recon-mcp
```

也可以同时启动多个族 profile；高风险族建议放到单独 Runner。`exec`、`execute-python-script`、
`install-python-package` 等控制平面 Shell 能力不会被迁移到族镜像，而是由控制面镜像中的
Bash/Python/venv 提供。镜像缺少命令时，构建阶段的
严格 doctor 和服务启动时的 `TOOL_PRECHECK=1` 都会失败。

也可以把仓库挂入该运行时后执行：

```bash
go run ./cmd/toolcatalog doctor --tools-dir /etc/cyberstrike/tools --strict
```

MCP 返回的 `report_files` 指向控制平面内的只读挂载 `/app/tool_reports`，并返回
`report_sha256` 以及实际镜像引用 `image` 作为证据校验值；Agent 或人工复核可以据此读取原始 JSON/HTML 报告。
控制平面 Compose 默认把它映射到 `../tools/data/reports`。

CyberStrikeAI 侧仍然必须启用会话审批，并为项目设置目标白名单。MCP 服务的 token 是服务级凭证，
只允许放在控制网络内，不能发布到宿主机公网端口。

## 网络和权限

当前发布镜像按 `linux/amd64` 验收。Compose 文件会显式设置平台；ARM 主机只有在 Docker 已配置
amd64 仿真时才可运行，工具安装脚本中的上游二进制仍需单独做 arm64 验证。

- 工具容器不向宿主机发布端口；一次性任务只加入 egress 网络，MCP 服务额外加入控制平面内部网络。
- 目标输入只读挂载，报告目录单独可写。
- 容器使用只读根文件系统、丢弃 Linux capabilities、禁止提权和资源上限。
- 工具镜像不使用 `latest`。生产部署应锁定 digest，并在 CI 中生成 SBOM、执行镜像漏洞扫描。
- CyberStrikeAI 控制平面不挂载 `/var/run/docker.sock`。
- 需要特权能力的工具应新增独立 profile，并放到专用 Runner 主机或隔离节点。

## 与 CyberStrikeAI 的接入顺序

第一阶段可以人工执行 Compose 任务并导入报告。当前仓库同时提供了不需要 Docker Socket 的轻量 MCP
适配器：每个适配器构建在对应工具镜像上，只暴露 catalog 中固定的扫描操作。推荐顺序是：

```text
项目目标白名单
  → 角色工具白名单
  → HITL 审批
  → MCP 适配器校验 catalog.yaml
  → 工具容器内的固定命令
  → 报告解析器
  → 资产/漏洞/项目事实
  → 审计和清理
```

MCP 适配器只接受固定工具的受限参数，不接受任意镜像名、命令或宿主机路径。需要批量调度、跨主机
队列或高风险特权工具时，再增加独立 Runner 主机；CyberStrikeAI 控制平面仍不挂载 Docker Socket。
