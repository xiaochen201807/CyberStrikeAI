# 独立 Docker 工具平面

CyberStrikeAI 的 Web/API 控制平面和安全工具平面应分开部署。控制平面镜像包含完整的
`tools/*.yaml` 工具注册表（名称、参数、描述和角色兼容性），但不把 nmap、Nuclei、ZAP、
sqlmap 等扫描器二进制塞进控制镜像；二进制由 `deploy/tools/docker-compose.yml`、
`deploy/tools/docker-compose.runtime.yml` 或受管主机运行时提供。

当前仓库工具清单以 `tools/*.yaml` 为准，共 90 个定义，其中 78 个启用、12 个禁用。完整
部署状态见 [`deploy/tools/inventory.yaml`](../../deploy/tools/inventory.yaml)。73 个可迁移工具已经
映射到 8 个独立工具族镜像；3 个控制平面 Shell/Python 能力由控制面镜像提供；Nuclei/ZAP 仍使用
固定的专用适配器；1 个内部工具不会迁移到外部运行时。镜像构建阶段会对每个族执行严格
`toolcatalog doctor`，控制面 Dockerfile 也会检查 Bash/Python/venv，因此 `runtime-declared` 和
`control-image` 都表示已有明确构建配方，CI 通过后再视为已验证执行路径。

工具定义发生变化后重新生成清单：

```bash
go run ./cmd/toolcatalog inventory \
  --tools-dir tools \
  --catalog deploy/tools/catalog.yaml \
  --runtime deploy/tools/runtime/families.yaml \
  --output deploy/tools/inventory.yaml
```

CI 会比较生成结果；新增、删除或禁用工具而不更新清单会直接失败。

在某个运行时镜像或受管主机内验收命令是否真的存在：

```bash
go run ./cmd/toolcatalog doctor --tools-dir tools
# 交付前要求所有启用命令都存在：
go run ./cmd/toolcatalog doctor --tools-dir tools --strict
```

这两个概念必须分开：工具注册表保证当前项目能力不丢失，工具平面负责逐族提供隔离运行时。迁移一个
工具时，必须同时提供可验证的镜像、参数适配器、目标边界、资源限制和结果回写契约；只复制 YAML
不能算完成迁移。

## 为什么需要工具目录

`tools/*.yaml` 是 Agent 调用命令的描述，不能完整表达容器镜像、风险、目标输入、资源上限、
审批要求和报告格式。`deploy/tools/catalog.yaml` 增加了这些部署契约，并由：

```bash
go run ./cmd/toolcatalog validate --compose deploy/tools/docker-compose.yml deploy/tools/catalog.yaml
```

进行严格校验。CI/生产环境可以增加 `--require-digests`，禁止只使用版本标签。
其中 `profile` 表示逻辑工具族，`compose_profile` 将长驻 MCP 和一次性任务分开。

## 三个 Compose 文件

```bash
cd deploy/control
docker compose up -d --build

cd ../tools
cp .env.example .env
./scripts/validate.sh
```

控制平面只暴露 Web/API 端口，并使用 `/healthz`、`/readyz` 供编排检查。控制面镜像复制完整的
工具 YAML 注册表；工具项目同时提供一次性扫描服务和长驻 MCP 适配器。目标和报告分别挂载到
`data/targets` 和 `data/reports`。

长驻 MCP 适配器构建在对应的工具镜像上，只开放 catalog 中固定的工具和参数：

```bash
cd deploy/tools
cp .env.example .env
# 用 openssl rand -hex 32 生成 TOOL_MCP_TOKEN 并写入 .env
docker compose --profile web-mcp up -d --build nuclei-mcp
```

CyberStrikeAI 中可登记：

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

ZAP 对应 `http://zap-mcp:8080/sse` 和 `zap_baseline` 工具。工具服务不发布宿主机端口，控制平面
通过 `cyberstrike-control` 内部网络访问。

工具族运行时使用单独的 Compose 文件，按需启用，不会把 73 个工具塞进控制平面或一个通用大镜像：

```bash
cd deploy/tools
docker compose -f docker-compose.runtime.yml --env-file .env \
  --profile runtime-recon --profile runtime-web up -d --build
```

每个族服务只选择对应 `TOOL_FAMILY` 的工具定义。运行时安装脚本位于
[`deploy/tools/runtime/install-family.sh`](../../deploy/tools/runtime/install-family.sh)，族映射位于
[`deploy/tools/runtime/families.yaml`](../../deploy/tools/runtime/families.yaml)。

每个 MCP 工具服务还必须配置 `TOOL_ALLOWED_HOSTS`。它只允许精确主机或限定子域名模式，拒绝空列表
和全局通配符；建议每个 Compose 项目对应一个授权测试范围。这个检查不能替代 CyberStrikeAI 的项目
白名单和 HITL 审批，三者都要保留。一次性 Nuclei/ZAP 任务也会在容器内执行同一主机范围检查。

## 生产约束

1. 工具镜像固定 digest，并在 CI 中生成 SBOM、扫描漏洞。
2. 控制平面不挂载 Docker Socket；MCP 适配器直接运行在对应工具镜像内。
3. 适配器只允许 catalog 中的固定适配器，或 inventory 中通过 `TOOL_IDS`/`TOOL_FAMILY` 明确选择的工具；不接收任意命令、镜像或宿主机路径。批量调度或高风险特权工具使用独立 Runner 主机。
4. 每个任务必须关联项目、目标白名单、测试窗口、审批记录和报告校验值。
5. 工具 egress 网络必须由主机防火墙、云安全组或 egress 代理限制；Compose 网络本身不是目标白名单。
6. 高风险工具使用独立 profile、独立网络和专用执行节点，默认不启动。

## 结果回写

扫描报告先落盘，再由解析器转换成统一结果。推荐保留：

- 执行 ID、项目 ID、目标文件哈希；
- 工具 ID、镜像 digest、参数摘要、开始/结束时间；
- 原始报告路径和校验值；
- 资产、项目事实、漏洞记录之间的关联。

这使任务可以暂停、取消、重试和复核，不再依赖 Agent 对长篇命令输出的记忆。

控制平面默认以只读方式挂载工具报告目录到 `/app/tool_reports`。MCP 结果中的 `report_files` 使用这个
容器内路径，`image` 记录 catalog 中的镜像引用，`report_sha256` 记录每个已生成报告的 SHA-256 校验值，便于 Agent 继续读取和复核原始证据；
生产环境应对该目录做访问审计和保留期清理。
