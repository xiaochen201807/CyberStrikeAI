# CyberStrikeAI 控制平面容器

这里的镜像只包含 CyberStrikeAI Web/API、内置资源和运行时依赖。
安全工具不安装在这个镜像中，工具由 `deploy/tools/docker-compose.yml` 独立管理。

## 启动

```bash
cd deploy/control
cp .env.example .env
docker compose up -d --build
docker compose logs -f cyberstrike-ai
```

首次启动会把 `config.example.yaml` 复制到持久化卷中的
`/app/config/config.yaml`。进入容器或使用配置管理页面，先填写 AI 通道、修改管理员密码，
再开始配置工具平面。

Compose 默认只把 Web 端口绑定到 `127.0.0.1`。如果由反向代理或受控内网接入，才在 `.env` 中设置
`CYBERSTRIKE_BIND`，并同步配置访问控制和 TLS。

启用外部工具 MCP 时，把 `TOOL_MCP_TOKEN` 设置成与 `deploy/tools/.env` 完全相同的随机值，
并在 `config.yaml` 的 `external_mcp.servers` 中启用对应服务。

健康检查：

```text
https://127.0.0.1:8080/healthz
https://127.0.0.1:8080/readyz
```

`control` Compose 项目不挂载 Docker Socket，也不直接启动 nmap、Nuclei、ZAP 等扫描器。
控制平面镜像和只读注册表挂载会保留仓库里的完整 `tools/*.yaml` 定义，保证当前项目的工具名称、参数
和角色兼容性不丢失；命令二进制由受管运行时或工具平面的外部 MCP 提供。独立工具平面现在覆盖
Nuclei/ZAP 适配器和 8 个工具族运行时，其余控制平面能力及禁用项状态见 `deploy/tools/inventory.yaml`。

## 持久化卷

配置、数据库、日志、上传件和可编辑资源分别使用命名卷保存。升级镜像前应备份：

```bash
docker compose run --rm cyberstrike-ai sh -c 'tar czf - /app/config /app/data /app/roles /app/skills /app/agents /app/knowledge_base'
```
