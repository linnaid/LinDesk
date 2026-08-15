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
migrations/         后续 PostgreSQL 迁移文件
```

## 本地启动

LinDesk 当前提供服务骨架、健康检查，以及基于内存数据的“未发货退款申请”首个业务纵切；暂不连接数据库或支付渠道。

退款链路已经接入 demo 登录、RBAC 和租户数据隔离。业务接口会根据登录 token 中的租户身份查询订单和退款申请，不信任客户端伪造的业务 `tenant_id`。

这只是第一个可验证闭环，后续会在同一平台上继续补齐售前咨询、订单操作和售后工单能力。

```bash
go run ./cmd/lindesk
curl http://localhost:8080/healthz
```

## 首个业务模块

本地启动后先登录获取 demo token，再使用内置演示订单验证退款申请流程：

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

curl -X POST http://localhost:8080/refund-requests \
  -H 'Content-Type: application/json' \
  -H "Authorization: Bearer $CS_TOKEN" \
  -d '{
    "external_order_no": "LD202608040001",
    "requested_amount": 12900,
    "reason_code": "CUSTOMER_CANCELLED",
    "reason_note": "客户取消未发货订单"
  }'

curl http://localhost:8080/refund-requests/RR202608040001 \
  -H "Authorization: Bearer $CS_TOKEN"

curl -X POST http://localhost:8080/refund-requests/RR202608040001/approve \
  -H 'Content-Type: application/json' \
  -H "Authorization: Bearer $SUPERVISOR_TOKEN" \
  -d '{
    "comment": "订单未发货，符合退款规则"
  }'

curl -X POST http://localhost:8080/refund-requests/RR202608040001/refund-transactions \
  -H 'Content-Type: application/json' \
  -H "Authorization: Bearer $FINANCE_TOKEN" \
  -d '{
    "provider": "alipay",
    "provider_refund_no": "ALI202608040001",
    "amount": 12900,
    "status": "SUCCEEDED"
  }'
```

内置演示租户与订单：

- `tenant_demo`：`LD202608040001` 已支付、未发货，可退 `12900`；`LD202608040002` 已发货；`LD202608040003` 未支付。
- `tenant_acme`：也有 `LD202608040001`，但可退金额是 `25900`，用于验证相同外部订单号在不同租户下不会串数据。

## 当前进度

- 已完成：订单查询、退款申请创建、退款审核通过/驳回、财务人工退款回填、成功/失败结案。
- 暂用方案：通过 `X-Actor-ID` 模拟当前操作人，后续替换为真实登录态。
- 下一步：统一做多租户底座、登录注册和 RBAC，把临时操作人机制替换掉。

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

当前阶段服务启动时会尝试探测 PostgreSQL 连接；如果未配置 DSN 或连接不可用，会记录日志并继续使用内存仓储。连接成功时，退款链路会使用 PostgreSQL 仓储；请先执行 `migrations/` 下的 schema，并准备租户、用户、成员、订单等基础数据。

## 验证

```bash
go test ./...
go build ./cmd/lindesk
```
