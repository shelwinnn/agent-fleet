# agent-fleet Web UI

控制面 Web UI（Svelte + TypeScript + Vite + shadcn-svelte；FR-14.4 的显式 FR 例外）。

```bash
pnpm install
pnpm dev          # 开发服务器；/api、/healthz、/readyz 代理到 FLEET_API（默认 http://127.0.0.1:7788）
pnpm check        # svelte-check 类型检查
pnpm test         # 单元测试（vitest）
pnpm build        # 生产构建 → dist/
pnpm integration  # 与真实控制面的联调检查（需控制面已运行；FLEET_INTEGRATION=1 由脚本设置）
```

架构落点、路由清单、FR-14.5 四组状态呈现的说明、本片发现的后端契约缺口与未验证清单，
见 [`../docs/web-ui.md`](../docs/web-ui.md)。

- shadcn-svelte 组件在 `src/lib/components/ui/`：按页面需要引入（`components.json` 记录别名与风格）。
- `pnpm-workspace.yaml` 只声明 `allowBuilds`（pnpm 11+ 的依赖构建脚本白名单，此处为 esbuild）。
- SSE 用 `fetch` 流式读取而非 `EventSource`：SSE 端点与其余 `/api/v1` 端点同一鉴权模型（§29.14），
  而 `EventSource` 不能设置 `Authorization` 头。
