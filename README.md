# LinDesk

LinDesk 是一个面向电商/零售企业的多租户 AI Customer Agent 平台，统一承载售前咨询、订单操作和售后工单。

当前仓库先把第一条可运行业务纵切跑通："订单未发货申请退款"。现在已经覆盖订单查询、申请创建、主管审核、财务人工回填和成功/失败结案。

产品总纲见 Agent 根目录下的 [LinDesk-产品范围.md](../LinDesk-产品范围.md)，MVP 阶段、核心数据和完整流程见 [docs/mvp.md](docs/mvp.md)。

## 目录

```text
cmd/lindesk/        服务启动入口
configs/            可提交的配置示例
docs/               产品与技术文档
internal/config/    配置加载与校验
internal/domain/    订单、退款、审核、审计领域模型
internal/httpapi/   HTTP 路由与基础探针
migrations/         PostgreSQL schema 迁移
scripts/            本地初始化脚本与演示 seed
```

## 本地启动

LinDesk 当前提供健康检查、登录、多租户 RBAC，以及“未发货退款申请”首个业务纵切。未配置数据库 DSN 时会使用内存仓储和 Demo Auth；配置 PostgreSQL 且连接成功后，退款链路与 Auth Session 都会切换到 PostgreSQL。

退款链路已经接入 Bearer Token、RBAC 和租户数据隔离。PostgreSQL Auth 只保存 token hash，并在每次鉴权时重新读取当前租户成员关系和角色权限；业务接口不信任客户端伪造的业务 `tenant_id`。

这只是第一个可验证闭环，后续会在同一平台上继续补齐售前咨询、订单操作和售后工单能力。

```bash
go run ./cmd/lindesk
curl http://localhost:8080/healthz
```

## 首个业务模块

本地启动后先登录获取 demo token，再使用演示订单验证退款申请流程：

```bash
CS_TOKEN=$(curl -s -X POST http://localhost:8080/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"email":"cs@lindesk.local","password":"password123"}' | jq -r .token)

SUPERVISOR_TOKEN=$(curl -s -X POST http://localhost:8080/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"email":"supervisor@lindesk.local","password":"password123"}' | jq -r .token)

FINANCE_TOKEN=$(curl -s -X POST http://localhost:8080/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"email":"finance@lindesk.local","password":"password123"}' | jq -r .token)

curl http://localhost:8080/orders/LD202608040001 \
  -H "Authorization: Bearer $CS_TOKEN"

CREATE_RESPONSE=$(curl -s -X POST http://localhost:8080/refund-requests \
  -H 'Content-Type: application/json' \
  -H 'Idempotency-Key: refund-demo-001' \
  -H "Authorization: Bearer $CS_TOKEN" \
  -d '{
    "external_order_no": "LD202608040001",
    "requested_amount": 12900,
    "reason_code": "CUSTOMER_CANCELLED",
    "reason_note": "客户取消未发货订单"
  }')

REQUEST_NO=$(echo "$CREATE_RESPONSE" | jq -r .request_no)

curl http://localhost:8080/refund-requests/$REQUEST_NO \
  -H "Authorization: Bearer $CS_TOKEN"

curl -X POST http://localhost:8080/refund-requests/$REQUEST_NO/approve \
  -H 'Content-Type: application/json' \
  -H "Authorization: Bearer $SUPERVISOR_TOKEN" \
  -d '{
    "comment": "订单未发货，符合退款规则"
  }'

curl -X POST http://localhost:8080/refund-requests/$REQUEST_NO/refund-transactions \
  -H 'Content-Type: application/json' \
  -H "Authorization: Bearer $FINANCE_TOKEN" \
  -d '{
    "provider": "alipay",
    "provider_refund_no": "ALI-DEMO-001",
    "amount": 12900,
    "status": "SUCCEEDED"
  }'
```

内置演示租户与订单：

- `tenant_demo`：`LD202608040001` 已支付、未发货，可退 `12900`；`LD202608040002` 已发货；`LD202608040003` 未支付。
- `tenant_acme`：也有 `LD202608040001`，但可退金额是 `25900`，用于验证相同外部订单号在不同租户下不会串数据。

## 当前进度

- 已完成：订单查询、退款申请创建、退款审核通过/驳回、财务人工退款回填、成功/失败结案。
- 已完成：退款申请创建接口的 `Idempotency-Key`、请求摘要校验、首次结果复用和并发重复提交防护。
- 已完成：PostgreSQL Auth Session、Bearer Token、密码哈希、RBAC 权限校验和租户数据隔离。
- 已完成：PostgreSQL 退款仓储、核心 schema、本地演示 seed 和初始化脚本。
- 下一步：为财务退款回填增加 `Idempotency-Key`，并补齐高金额两级审批等剩余业务规则。

### 创建退款申请的幂等规则

`POST /refund-requests` 必须携带长度不超过 255 的 `Idempotency-Key`：

- 幂等作用域为当前租户、当前操作人和“创建退款申请”操作。
- 相同幂等键与相同业务参数重复提交时，不会再次创建退款申请，而是返回首次 `201` 响应。
- 重放响应会携带 `Idempotency-Replayed: true`。
- 相同幂等键携带不同业务参数时返回 `409 idempotency_key_conflict`。
- PostgreSQL 会在同一事务内写入幂等记录、退款申请和审计日志，避免并发重复创建。

如需使用本地配置文件：

```bash
cp configs/local.example.json configs/local.json
go run ./cmd/lindesk -config configs/local.json
```

支持通过环境变量覆盖本地配置中的非敏感项：

- `LINDESK_HTTP_ADDR`
- `LINDESK_DATABASE_DRIVER`
- `LINDESK_DATABASE_DSN`
- `LINDESK_HIGH_AMOUNT_APPROVAL_THRESHOLD`

当前阶段服务启动时会尝试探测 PostgreSQL 连接；如果未配置 DSN 或连接不可用，会记录日志并继续使用内存仓储和 Demo Auth。连接成功时，退款链路和 Auth Session 都会使用 PostgreSQL。

## PostgreSQL 端到端验证

本地可以直接使用仓库内的 PostgreSQL compose 配置：

```bash
docker compose up -d postgres
./scripts/init_postgres.sh --reset
cp configs/local.example.json configs/local.json
go run ./cmd/lindesk -config configs/local.json
```

说明：

- `docker-compose.yml` 启动本地 PostgreSQL，并创建 `lindesk` 数据库和用户。
- `scripts/init_postgres.sh --reset` 会重建本地 schema，并执行 `scripts/seeds/` 下的 demo seed。
- `configs/local.example.json` 的 DSN 与 compose 默认账号一致。
- PostgreSQL Auth 使用 seed 中的用户、租户成员和角色权限完成登录，并将 Session 写入 `auth_sessions`。
- 密码校验使用 PBKDF2-SHA256；新密码哈希使用随机盐，Demo seed 使用可重复初始化的预计算哈希。服务仍兼容读取旧 Demo SHA-256 哈希，便于滚动迁移。
- 数据库只保存 access token 的 SHA-256 hash，不保存登录响应中的原始 token。

跑完退款流程后，可以检查真实落库结果：

```bash
docker compose exec -T postgres psql 'postgres://lindesk:lindesk@localhost:5432/lindesk?sslmode=disable' \
  -c 'SELECT tenant_id, request_no, status, requested_amount FROM refund_requests ORDER BY created_at DESC;'

docker compose exec -T postgres psql 'postgres://lindesk:lindesk@localhost:5432/lindesk?sslmode=disable' \
  -c 'SELECT tenant_id, entity_type, action, operator_id FROM audit_logs ORDER BY created_at DESC;'

docker compose exec -T postgres psql 'postgres://lindesk:lindesk@localhost:5432/lindesk?sslmode=disable' \
  -c 'SELECT tenant_id, user_id, expires_at, revoked_at FROM auth_sessions ORDER BY created_at DESC;'

docker compose exec -T postgres psql 'postgres://lindesk:lindesk@localhost:5432/lindesk?sslmode=disable' \
  -c 'SELECT tenant_id, actor_id, operation, idempotency_key, resource_id FROM idempotency_records ORDER BY created_at DESC;'
```

## 验证

```bash
go test ./...
go build ./cmd/lindesk
```
