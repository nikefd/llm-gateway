# LLM Gateway — 多模型推理平台服务

一个支持动态模型注册、流式推理、热更新的多模型托管服务平台。

## 架构设计

```
┌─────────────┐     ┌──────────────┐     ┌─────────────────┐
│  HTTP Client │────▶│  Router/Mux  │────▶│  Handler Layer  │
└─────────────┘     └──────────────┘     └────────┬────────┘
                                                   │
                    ┌──────────────┐     ┌─────────▼────────┐
                    │   Registry   │◀────│  Model Selection │
                    │ (thread-safe)│     │ (weighted/shadow) │
                    └──────────────┘     └─────────┬────────┘
                                                   │
                    ┌──────────────────────────────▼────────┐
                    │         Backend Abstraction            │
                    │  ┌──────┐ ┌────────┐ ┌──────┐ ┌────┐ │
                    │  │ Mock │ │ OpenAI │ │Ollama│ │Qwen│ │
                    │  └──────┘ └────────┘ └──────┘ └────┘ │
                    └───────────────────────────────────────┘
```

### 关键设计决策

1. **SSE (Server-Sent Events) 作为流式协议** — 这是 LLM 行业标准（OpenAI/Anthropic 都用 SSE），比 WebSocket 更轻量，比 gRPC 更易调试，curl 直接可用。

2. **Registry 使用读写锁 + atomic** — `sync.RWMutex` 保护模型注册/查询，`atomic.Int32` 做并发计数，避免锁竞争。

3. **热更新不中断现有连接** — 更新时只修改 config 引用，已经在 `Stream()` 中的请求持有旧 config 的引用，不受影响。Go 的 GC 保证旧引用在使用完毕前不被回收。

4. **策略模式 (Strategy Pattern)** — 每个 backend_type 实现统一的 `Backend` 接口，通过工厂注册，运行时动态选择。

5. **Shadow 灰度** — shadow 版本与主版本并行运行，shadow 的结果只记录日志不返回给用户，用于质量对比评估。

## 快速开始

### 依赖

- Go 1.22+ (使用了 `net/http` 的路由模式匹配)

### 启动

```bash
go build -o llm-gateway .
```

```bash
# 默认端口 8080
./llm-gateway

# 或自定义端口
PORT=9090 ./llm-gateway
```

### 一键 Demo

```bash
chmod +x demo.sh
./demo.sh
```

自动启动服务，依次演示所有功能（注册、流式推理、权重路由、并发限制、热更新、删除、错误模拟、Prometheus 指标），最后自动清理退出。可重复运行。

## API 调用示例

### 1. 注册模型

```bash
# 注册 mock 模型
curl -X POST http://localhost:8080/models \
  -H "Content-Type: application/json" \
  -d '{
    "model_name": "chat-bot",
    "version": "v1",
    "backend_type": "mock",
    "max_concurrent": 10
  }'

# 注册 OpenAI 模型
curl -X POST http://localhost:8080/models \
  -H "Content-Type: application/json" \
  -d '{
    "model_name": "chat-bot",
    "version": "v2",
    "backend_type": "openai",
    "config": {
      "api_key": "sk-xxx",
      "model": "gpt-4"
    },
    "weight": 50,
    "max_concurrent": 5
  }'

# 注册 shadow 版本（灰度发布）
curl -X POST http://localhost:8080/models \
  -H "Content-Type: application/json" \
  -d '{
    "model_name": "chat-bot",
    "version": "v3-shadow",
    "backend_type": "mock",
    "shadow": true
  }'
```

### 2. 查看模型列表

```bash
curl http://localhost:8080/models | jq .
```

### 3. 流式推理

```bash
# 指定版本
curl -N http://localhost:8080/infer \
  -H "Content-Type: application/json" \
  -d '{
    "model": "chat-bot",
    "version": "v1",
    "input": "Tell me a joke"
  }'

# 不指定版本 → 按权重自动路由
curl -N http://localhost:8080/infer \
  -H "Content-Type: application/json" \
  -d '{
    "model": "chat-bot",
    "input": "Tell me a joke"
  }'
```

### 4. 热更新模型

```bash
# 更新 v1 的后端为 openai（不影响正在进行的请求）
curl -X PUT http://localhost:8080/models/chat-bot/version/v1 \
  -H "Content-Type: application/json" \
  -d '{
    "backend_type": "openai",
    "config": {
      "api_key": "sk-new-key",
      "model": "gpt-4o"
    }
  }'
```

### 5. 删除模型版本

```bash
curl -X DELETE http://localhost:8080/models/chat-bot/version/v1
```

### 6. 查看 Prometheus 指标

```bash
curl http://localhost:8080/metrics
```

### 7. 健康检查

```bash
curl http://localhost:8080/health
```

### 8. 管理面板

浏览器打开 `http://localhost:8080/admin`，自动每 3 秒刷新，展示所有模型/版本/状态/活跃连接数。

### 9. 错误模拟

```bash
# 注册一个会超时的模型
curl -X POST http://localhost:8080/models \
  -H "Content-Type: application/json" \
  -d '{"model_name":"error-bot","version":"v1","backend_type":"mock","config":{"error":"timeout"}}'

# 注册一个会中途失败的模型
curl -X POST http://localhost:8080/models \
  -H "Content-Type: application/json" \
  -d '{"model_name":"error-bot","version":"v2","backend_type":"mock","config":{"error":"partial"}}'

# 注册一个返回空响应的模型
curl -X POST http://localhost:8080/models \
  -H "Content-Type: application/json" \
  -d '{"model_name":"error-bot","version":"v3","backend_type":"mock","config":{"error":"empty"}}'
```

## 功能完成情况

### 核心要求 ✅
- [x] 模型注册与管理 (POST/GET/PUT/DELETE /models)
- [x] 流式推理 API (POST /infer, SSE)
- [x] 热更新不影响已有连接 (PUT /models/{name}/version/{v})

### 加分项 ✅
- [x] 容量管理 — `max_concurrent` 参数，超出返回 429
- [x] 多版本分流策略 — `weight` 参数，按权重路由
- [x] 异常处理机制 — `trace_id`、错误码、结构化日志、**错误模拟**（timeout/partial/empty）
- [x] Prometheus metrics — `/metrics` 端点 (QPS、平均延迟、失败率)
- [x] 简易管理面板 — `/admin` 网页，自动刷新，展示模型/版本/状态/活跃连接数

### 进阶挑战 ✅
- [x] A. 多后端动态切换 — mock / openai / ollama / qwen，统一 Backend 接口
- [x] B. 模型热重启与资源回收 — 30min 未使用自动卸载，再次调用延迟加载
- [x] C. 灰度发布 — shadow 模式，主版本返回用户，shadow 版本后台执行并记录日志对比

## 项目结构

```
llm-gateway/
├── main.go              # 入口 + idle unloader goroutine
├── go.mod
├── router/
│   └── router.go        # 路由注册
├── handler/
│   ├── model.go         # 模型 CRUD handler
│   ├── infer.go         # 流式推理 handler + shadow 执行 + metrics
│   └── admin.go         # Web 管理面板 (embedded HTML)
├── registry/
│   └── registry.go      # 线程安全的模型注册表 + 版本选择 + idle 管理
├── backend/
│   ├── backend.go       # Backend 接口 + 工厂
│   ├── mock.go          # Mock 后端 (200ms/token)
│   ├── openai.go        # OpenAI API 后端
│   ├── ollama.go        # Ollama 本地推理后端
│   └── qwen.go          # 通义千问 DashScope 后端
├── middleware/
│   └── metrics.go       # Prometheus metrics 输出
└── README.md
```

## Self Report

- 总耗时：约 1 小时
- 实际做题时间段：10:10 ~ 11:10
- 完成情况：
  - [x] 模型注册 / 更新 / 查看
  - [x] 流式推理接口
  - [x] 热更新不影响已有连接
  - [x] 多版本分流
  - [x] Prometheus metrics
  - [x] 灰度发布
  - [x] 多后端动态切换 (mock/openai/ollama/qwen)
  - [x] 模型热重启与资源回收
  - [x] 容量管理 (并发限制)
  - [x] 异常处理 (trace_id/错误码/日志/错误模拟)
  - [x] 管理面板 (/admin 网页)
- 备注说明：
  - 热更新设计：Infer 使用 SnapshotConfig() 在推理开始前拷贝 config，之后不再读 ModelVersion 的 config 字段，因此 UpdateVersion 可以安全并发修改
  - Shadow 模式：shadow 版本与主版本并行执行，结果仅记录日志（token 数、延迟、响应长度），便于模型质量评估
  - 并发控制：使用 atomic.Int32 的 CAS 操作实现无锁并发限制，比 semaphore 更高效
  - 自动卸载：后台 goroutine 每 5min 扫描，30min 未调用的模型标记为 unloaded，再次调用时延迟加载
  - 错误模拟：Mock 后端支持 config["error"] 配置 timeout/partial/empty 三种错误场景
  - SSE 选型：LLM 行业标准，比 WebSocket 更轻量（单向流），比 gRPC 更 curl-friendly
  - 管理面板：嵌入式 HTML，无外部前端依赖，auto-refresh 展示实时状态
