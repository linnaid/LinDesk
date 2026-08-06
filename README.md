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

这只是第一个可验证闭环，后续会在同一平台上继续补齐售前咨询、订单操作和售后工单能力。

```bash
go run ./cmd/lindesk
curl http://localhost:8080/healthz
```

## 首个业务模块

本地启动后可使用内置演示订单验证退款申请流程：

```bash
curl http://localhost:8080/orders/LD202608040001

curl -X POST http://localhost:8080/refund-requests \
  -H 'Content-Type: application/json' \
  -d '{
    "external_order_no": "LD202608040001",
    "requested_amount": 12900,
    "reason_code": "CUSTOMER_CANCELLED",
    "reason_note": "客户取消未发货订单",
    "submitted_by": "user_cs_001"
  }'

curl http://localhost:8080/refund-requests/RR202608040001

curl -X POST http://localhost:8080/refund-requests/RR202608040001/approve \
  -H 'Content-Type: application/json' \
  -H 'X-Actor-ID: user_supervisor_001' \
  -d '{
    "comment": "订单未发货，符合退款规则"
  }'

curl -X POST http://localhost:8080/refund-requests/RR202608040001/refund-transactions \
  -H 'Content-Type: application/json' \
  -H 'X-Actor-ID: user_finance_001' \
  -d '{
    "provider": "alipay",
    "provider_refund_no": "ALI202608040001",
    "amount": 12900,
    "status": "SUCCEEDED"
  }'
```

内置演示订单：

- `LD202608040001`：已支付、未发货，可创建退款申请。
- `LD202608040002`：已支付、已发货，会被业务规则拒绝。
- `LD202608040003`：未支付、未发货，会被业务规则拒绝。

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
- `LINDESK_DATABASE_DSN`
- `LINDESK_HIGH_AMOUNT_APPROVAL_THRESHOLD`

## 验证

```bash
go test ./...
go build ./cmd/lindesk
```
