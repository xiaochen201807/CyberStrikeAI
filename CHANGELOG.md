# Changelog

## Unreleased

### Deployment

- 单 Docker VM 启动脚本现在会校验 token、目标白名单和生产镜像 digest，拉取固定镜像并等待控制面与所选工具族健康检查通过。
- 新增 `deploy/check-stack.sh`、`deploy/backup-stack.sh` 和 `deploy/restore-stack.sh`，覆盖运行验收及 SQLite/命名卷/工具数据的停机备份恢复。
- Compose 与 GHCR 发布流程明确按 `linux/amd64` 验收；发布后自动拉取 GHCR 镜像执行控制面、recon/web 工具族冒烟测试。

### Fixed

- 自更新恢复在改写文件前严格验证恢复记录、State、提交和二进制配对；拒绝缺失、重复或矛盾证据，保留可恢复现场。
- 编辑已停止的外部 MCP 不再意外启动连接；启动/停止、配置编辑和应用重启保持一致的启停状态。

- 登录密码显隐按钮改为输入框内的眼睛图标，统一输入框宽高，支持键盘焦点、44px 触控区域、中英俄文无障碍标签及浅色/深色主题。
