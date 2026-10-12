# SiYuan Wiki Agent — AI 办公助手（接管思源）

基于 [思源笔记](https://github.com/siyuan-note/siyuan) fork，**接管思源、改写源码**集成 Wiki Agent 工作流：用户给主题 → Agent 询问 → 动态问卷 → 生成文档落盘思源。复用思源已配的 LLM（火山/dashscope 网关），一体集成。

## 已实现

### Wiki Agent 后端（`kernel/api/wikiagent.go`）
- **`POST /api/ai/wikiagent`** — SSE 状态机接口，三阶段工作流：
  - `ask`：用户给主题 → LLM 生成 2-3 个澄清选择题（受众/深度等）→ `question` 事件
  - `questionnaire`：用户答完 → LLM 动态生成问卷 schema（篇幅/风格/章节等）→ `questionnaire` 事件
  - `generate`：用户填完 → LLM 流式生成 Markdown 文档 → `content` 事件 → `model.CreateDocByMd` 落盘思源笔记本 → `done` 事件（含文档 ID）
- **复用思源 LLM 配置**：`model.Conf.AI.GetAgentModel()` 取用户在 设置→AI 配的 provider/model（火山网关/dashscope），无需另存 key
- **raw HTTP 流式解析**：绕过思源 `reasoningResponseTransport`（只处理 `/chat/completions`，火山用 `/compatible/chat/completions`），自己解析 OpenAI 流式 `content` + `reasoning_content`，兼容推理模型
- **多 provider fallback**：dashscope 某些模型无权限时自动换火山网关，容错
- 路由注册：`kernel/api/router.go` 加 `/api/ai/wikiagent`
- 前端构建修复：`app/webpack.desktop.js` target `es6`→`es2020`（修复 Big integer literals 报错）

### 部署
- 源码编译（Go 1.26 kernel + pnpm/webpack 前端）
- 自签 HTTPS / Tailscale 内网访问
- AI provider 配置（火山网关/dashscope）

## 待实现

### Wiki Agent 前端（思源 dock 面板）
- `app/src/layout/dock/WikiAgent.ts` — 原生 TS dock 面板，渲染动态问卷，SSE 驱动多轮问答
- `app/src/types/index.d.ts` — TDock 加 `"wikiAgent"`
- `app/src/layout/dock/index.ts` — TYPES 加 `wikiAgent`，toggleModel 加 case
- `app/src/constants.ts` — 默认布局加 Wiki Agent dock 项
- 交互：输入主题→答选择题→填问卷→流式预览→文档落盘→跳转文档

### Agent 工具调用（function calling）
- `search_kb` 查思源知识库
- `query_external_rag` 查外部 RAG 知识库（DB-GPT）
- `execute_sql` 执行 SQL 查数据库（经 DB-GPT）
- MCP server 支持

### 其他
- 选中段落 AI 改写/润色/续写（思源已内置，待接入工作流）
- 段落级 streaming insert
- 多用户/团队/权限（思源已有，待适配 Agent）

## 部署方式

```bash
# WSL/Linux
git clone git@github.com:WinterStarHu/siyuan.git
cd siyuan

# 编译前端
cd app
pnpm install
sed -i 's/target: "es6"/target: "es2020"/g' webpack.desktop.js  # 修复构建
npx webpack --mode production --config webpack.desktop.js
cd ..

# 编译 kernel（需 Go 1.26 + gcc）
cd kernel
export CGO_ENABLED=1 GOOS=linux GOARCH=amd64 CC=gcc GOPROXY=https://goproxy.cn,direct
go build -buildmode=pie -tags "fts5 sqlcipher" -o "../app/kernel-linux/SiYuan-Kernel" -ldflags "-s -w" .
cd ..

# 启动
cd app/kernel-linux
./SiYuan-Kernel serve --port=6806 --wd=/path/to/app --workspace=/path/to/data --lang=zh-CN --accessAuthCode=yourcode

# 配置 AI provider（API 调用）
curl -X POST http://localhost:6806/api/system/loginAuth -H 'Content-Type: application/json' -d '{"authCode":"yourcode"}'
curl -X POST http://localhost:6806/api/setting/setAI -H 'Content-Type: application/json' -d '{
  "providers":[{
    "id":"volcano","displayName":"火山网关","enabled":true,
    "apiKey":"your-token","baseURL":"https://apigateway-cn-beijing.volceapi.com/compatible",
    "protocol":"openai","requestTimeout":600,
    "models":[{"id":"claude","name":"claude-sonnet-4-5-20250929","enabled":true}]
  }],
  "agent":{"modelId":"claude"}
}'

# 测试 Wiki Agent
curl -X POST http://localhost:6806/api/ai/wikiagent -H 'Content-Type: application/json' -d '{"topic":"BloomFilter技术调研","state":"ask"}'
```

## 架构

```
思源（接管后的源码）
├─ kernel/api/wikiagent.go        # Wiki Agent SSE 状态机（新增）
│   ├─ ask → question 事件
│   ├─ questionnaire → questionnaire 事件
│   └─ generate → content → done 事件（落盘思源）
├─ kernel/api/router.go            # 路由注册（改）
└─ app/（前端，待加 Wiki Agent dock）

复用思源：
├─ model.Conf.AI.GetAgentModel()  # 取配的 LLM provider/model
├─ model.CreateDocByMd()          # 文档落盘
└─ 思源内置 AI（改写/续写/Agent 对话）
```

## 许可证
AGPL-3.0（继承思源）

## 相关项目
- [webgif](https://github.com/WinterStarHu/webgif) — 屏幕录制 GIF 工具（同套基础设施）
- [DB-GPT](https://github.com/WinterStarHu/DB-GPT) — 外部 RAG 知识库（待接入）
