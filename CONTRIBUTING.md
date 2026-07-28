# 参与贡献

感谢你愿意改进 magnet-to-strm。

## 开始之前

- 对较大的功能或行为变更，请先创建 Issue 说明使用场景和设计思路。
- 安全漏洞不要提交公开 Issue，请按 [SECURITY.md](SECURITY.md) 私下报告。
- 提交代码即表示你同意按项目的 Apache-2.0 许可证授权该贡献。

## 本地开发

需要 Go 1.26 或更高版本。构建 WebUI 还需要 Node.js 22。

```bash
go mod download
go test ./...
go vet ./...
go build ./cmd/magnet-to-strm
```

WebUI 位于 `webui`。本地联调时，Vite 会把 `/api` 代理到 `127.0.0.1:8080`：

```bash
cd webui
npm ci
npm run dev
```

测试不应依赖真实 115 凭据或外部服务。涉及外部接口时，请使用测试替身。

## 提交 Pull Request

1. 从 `main` 创建分支。
2. 保持改动聚焦，并为新增行为补充测试。
3. 更新受到影响的 README、示例配置或迁移说明。
4. 确保测试、静态检查和构建都通过。
5. 在 PR 中说明动机、主要改动、验证方式和可能的兼容性影响。

提交信息建议使用简洁的祈使句，中文或英文均可。行为不兼容的改动必须在 PR 中明确标注。
