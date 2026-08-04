# LinDesk

LinDesk 是一个面向电商售后团队的内部工作台。第一版聚焦“订单未发货申请退款”的人工审核与人工退款闭环。

产品范围、核心数据表和完整流程见 [docs/mvp.md](docs/mvp.md)。

## 目录

```text
cmd/lindesk/        服务启动入口
configs/            可提交的配置示例
docs/               产品与技术文档
internal/config/    配置加载与校验
internal/domain/    订单、退款、审核领域模型
internal/httpapi/   HTTP 路由与基础探针
migrations/         后续 PostgreSQL 迁移文件
```

## 本地启动

LinDesk 当前提供服务骨架、健康检查，以及基于内存数据的“未发货退款申请”首个业务纵切；暂不连接数据库或支付渠道。

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
```

内置演示订单：

- `LD202608040001`：已支付、未发货，可创建退款申请。
- `LD202608040002`：已支付、已发货，会被业务规则拒绝。
- `LD202608040003`：未支付、未发货，会被业务规则拒绝。

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
