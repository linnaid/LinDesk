package refund

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"lindesk/internal/domain"

	"github.com/jackc/pgx/v5/pgconn"
)

const postgresUniqueViolationCode = "23505"

var _ Repository = (*PostgresRepository)(nil)

type PostgresRepository struct {
	db *sql.DB
}

func NewPostgresRepository(db *sql.DB) *PostgresRepository {
	if db == nil {
		panic("postgres database handle is required")
	}

	return &PostgresRepository{db: db}
}

func (repository *PostgresRepository) FindOrderByExternalOrderNo(ctx context.Context, tenantID string, externalOrderNo string) (domain.Order, error) {
	order, err := scanOrder(repository.db.QueryRowContext(ctx, `
SELECT id, tenant_id, external_order_no, customer_id, payment_status, fulfillment_status,
       paid_amount, refunded_amount, currency, paid_at
FROM orders
WHERE tenant_id = $1 AND external_order_no = $2
`, tenantID, externalOrderNo))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Order{}, ErrOrderNotFound
	}
	if err != nil {
		return domain.Order{}, err
	}

	return order, nil
}

func (repository *PostgresRepository) FindRefundRequestByIdempotency(ctx context.Context, idempotency IdempotencyRecord) (domain.RefundRequest, bool, error) {
	var existingHash string
	var responseData []byte
	err := repository.db.QueryRowContext(ctx, `
SELECT request_hash, response_data
FROM idempotency_records
WHERE tenant_id = $1 AND actor_id = $2 AND operation = $3 AND idempotency_key = $4
`, idempotency.TenantID, idempotency.ActorID, idempotency.Operation, idempotency.Key).Scan(&existingHash, &responseData)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.RefundRequest{}, false, nil
	}
	if err != nil {
		return domain.RefundRequest{}, false, err
	}
	if existingHash != idempotency.RequestHash {
		return domain.RefundRequest{}, false, ErrIdempotencyKeyConflict
	}

	var existingRequest domain.RefundRequest
	if err := json.Unmarshal(responseData, &existingRequest); err != nil {
		return domain.RefundRequest{}, false, fmt.Errorf("unmarshal idempotency response: %w", err)
	}
	return existingRequest, true, nil
}

func (repository *PostgresRepository) FindRefundTransactionByIdempotency(ctx context.Context, idempotency IdempotencyRecord) (TransactionPersistenceResult, bool, error) {
	var existingHash string
	var responseData []byte
	err := repository.db.QueryRowContext(ctx, `
SELECT request_hash, response_data
FROM idempotency_records
WHERE tenant_id = $1 AND actor_id = $2 AND operation = $3 AND idempotency_key = $4
`, idempotency.TenantID, idempotency.ActorID, idempotency.Operation, idempotency.Key).Scan(&existingHash, &responseData)
	if errors.Is(err, sql.ErrNoRows) {
		return TransactionPersistenceResult{}, false, nil
	}
	if err != nil {
		return TransactionPersistenceResult{}, false, err
	}
	if existingHash != idempotency.RequestHash {
		return TransactionPersistenceResult{}, false, ErrIdempotencyKeyConflict
	}

	var response transactionIdempotencyResponse
	if err := json.Unmarshal(responseData, &response); err != nil {
		return TransactionPersistenceResult{}, false, fmt.Errorf("unmarshal transaction idempotency response: %w", err)
	}
	return TransactionPersistenceResult{
		Request:     response.Request,
		Transaction: response.Transaction,
		Replayed:    true,
	}, true, nil
}

func (repository *PostgresRepository) CreateRefundRequest(ctx context.Context, request domain.RefundRequest, auditLog domain.AuditLog, idempotency IdempotencyRecord) (CreateRequestPersistenceResult, error) {
	var result CreateRequestPersistenceResult
	err := repository.withTx(ctx, func(tx *sql.Tx) error {
		responseData, err := json.Marshal(request)
		if err != nil {
			return fmt.Errorf("marshal idempotency response: %w", err)
		}

		// 唯一约束会让相同作用域的并发请求在这里串行化。
		insertResult, err := tx.ExecContext(ctx, `
INSERT INTO idempotency_records (
    id, tenant_id, actor_id, operation, idempotency_key, request_hash,
    status, response_status, response_data, resource_type, resource_id,
    created_at, completed_at
) VALUES ($1, $2, $3, $4, $5, $6, 'COMPLETED', $7, $8, 'refund_request', $9, $10, $10)
ON CONFLICT (tenant_id, actor_id, operation, idempotency_key) DO NOTHING
`, idempotency.ID, idempotency.TenantID, idempotency.ActorID, idempotency.Operation, idempotency.Key, idempotency.RequestHash, idempotency.ResponseStatus, responseData, request.ID, idempotency.CreatedAt)
		if err != nil {
			return err
		}
		rowsAffected, err := insertResult.RowsAffected()
		if err != nil {
			return err
		}
		if rowsAffected == 0 {
			var existingHash string
			var existingResponse []byte
			if err := tx.QueryRowContext(ctx, `
SELECT request_hash, response_data
FROM idempotency_records
WHERE tenant_id = $1 AND actor_id = $2 AND operation = $3 AND idempotency_key = $4
`, idempotency.TenantID, idempotency.ActorID, idempotency.Operation, idempotency.Key).Scan(&existingHash, &existingResponse); err != nil {
				return err
			}
			if existingHash != idempotency.RequestHash {
				return ErrIdempotencyKeyConflict
			}

			var existingRequest domain.RefundRequest
			if err := json.Unmarshal(existingResponse, &existingRequest); err != nil {
				return fmt.Errorf("unmarshal idempotency response: %w", err)
			}
			result = CreateRequestPersistenceResult{Request: existingRequest, Replayed: true}
			return nil
		}

		var activeRequestID string
		err = tx.QueryRowContext(ctx, `
SELECT id
FROM refund_requests
WHERE tenant_id = $1
  AND order_id = $2
  AND status IN ('PENDING_REVIEW', 'APPROVED', 'PROCESSING')
LIMIT 1
`, request.TenantID, request.OrderID).Scan(&activeRequestID)
		if err == nil {
			return ErrActiveRefundRequestExists
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}

		orderSnapshot, err := json.Marshal(request.OrderSnapshot)
		if err != nil {
			return fmt.Errorf("marshal order snapshot: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO refund_requests (
    id, tenant_id, request_no, order_id, order_snapshot, requested_amount,
    reason_code, reason_note, status, submitted_by, submitted_at, created_at, updated_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $11, $11)
`, request.ID, request.TenantID, request.RequestNo, request.OrderID, orderSnapshot, request.RequestedAmount, request.ReasonCode, request.ReasonNote, request.Status, request.SubmittedBy, request.SubmittedAt); err != nil {
			if isUniqueViolation(err, "refund_requests_active_order_unique_idx") {
				return ErrActiveRefundRequestExists
			}
			return err
		}

		if err := insertAuditLog(ctx, tx, auditLog); err != nil {
			return err
		}

		result = CreateRequestPersistenceResult{Request: request}
		return nil
	})
	if err != nil {
		return CreateRequestPersistenceResult{}, err
	}

	return result, nil
}

func (repository *PostgresRepository) FindRefundRequestByRequestNo(ctx context.Context, tenantID string, requestNo string) (domain.RefundRequest, error) {
	request, err := scanRefundRequest(repository.db.QueryRowContext(ctx, `
SELECT id, tenant_id, request_no, order_id, order_snapshot, requested_amount,
       reason_code, reason_note, status, submitted_by, submitted_at
FROM refund_requests
WHERE tenant_id = $1 AND request_no = $2
`, tenantID, requestNo))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.RefundRequest{}, ErrRefundRequestNotFound
	}
	if err != nil {
		return domain.RefundRequest{}, err
	}

	return request, nil
}

func (repository *PostgresRepository) ListApprovalsByRequestNo(ctx context.Context, tenantID string, requestNo string) ([]domain.Approval, error) {
	refundRequest, err := repository.FindRefundRequestByRequestNo(ctx, tenantID, requestNo)
	if err != nil {
		return nil, err
	}

	rows, err := repository.db.QueryContext(ctx, `
SELECT id, tenant_id, refund_request_id, level, status, assignee_id, decision_by, decision_at, comment
FROM approvals
WHERE tenant_id = $1 AND refund_request_id = $2
ORDER BY level, created_at
`, tenantID, refundRequest.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	approvals := make([]domain.Approval, 0)
	for rows.Next() {
		approval, err := scanApproval(rows)
		if err != nil {
			return nil, err
		}
		approvals = append(approvals, approval)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return approvals, nil
}

func (repository *PostgresRepository) ListRefundTransactionsByRequestNo(ctx context.Context, tenantID string, requestNo string) ([]domain.RefundTransaction, error) {
	refundRequest, err := repository.FindRefundRequestByRequestNo(ctx, tenantID, requestNo)
	if err != nil {
		return nil, err
	}

	rows, err := repository.db.QueryContext(ctx, `
SELECT id, tenant_id, refund_request_id, provider, provider_refund_no, amount, status,
       failure_reason, processed_by, processed_at
FROM refund_transactions
WHERE tenant_id = $1 AND refund_request_id = $2
ORDER BY processed_at, created_at
`, tenantID, refundRequest.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	transactions := make([]domain.RefundTransaction, 0)
	for rows.Next() {
		transaction, err := scanRefundTransaction(rows)
		if err != nil {
			return nil, err
		}
		transactions = append(transactions, transaction)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return transactions, nil
}

func (repository *PostgresRepository) ReviewRefundRequest(ctx context.Context, tenantID string, requestNo string, approval domain.Approval, requestStatus domain.RefundRequestStatus, auditLog domain.AuditLog) (domain.RefundRequest, error) {
	var updatedRequest domain.RefundRequest
	err := repository.withTx(ctx, func(tx *sql.Tx) error {
		request, err := scanRefundRequest(tx.QueryRowContext(ctx, `
SELECT id, tenant_id, request_no, order_id, order_snapshot, requested_amount,
       reason_code, reason_note, status, submitted_by, submitted_at
FROM refund_requests
WHERE tenant_id = $1 AND request_no = $2
FOR UPDATE
`, tenantID, requestNo))
		if errors.Is(err, sql.ErrNoRows) {
			return ErrRefundRequestNotFound
		}
		if err != nil {
			return err
		}
		if request.Status != domain.RefundRequestStatusPendingReview {
			return ErrRefundRequestNotReviewable
		}
		var approvalExists bool
		if err := tx.QueryRowContext(ctx, `
SELECT EXISTS (
    SELECT 1
    FROM approvals
    WHERE tenant_id = $1 AND refund_request_id = $2 AND level = $3
)
`, tenantID, request.ID, approval.Level).Scan(&approvalExists); err != nil {
			return err
		}
		if approvalExists {
			return ErrApprovalLevelAlreadyProcessed
		}

		request.Status = requestStatus
		if _, err := tx.ExecContext(ctx, `
UPDATE refund_requests
SET status = $1, updated_at = $2
WHERE tenant_id = $3 AND request_no = $4
`, requestStatus, timeOrNow(approval.DecisionAt), tenantID, requestNo); err != nil {
			return err
		}
		if err := insertApproval(ctx, tx, approval); err != nil {
			return err
		}
		if err := insertAuditLog(ctx, tx, auditLog); err != nil {
			return err
		}

		updatedRequest = request
		return nil
	})
	if err != nil {
		return domain.RefundRequest{}, err
	}

	return updatedRequest, nil
}

func (repository *PostgresRepository) RecordRefundTransaction(ctx context.Context, tenantID string, requestNo string, transaction domain.RefundTransaction, requestStatus domain.RefundRequestStatus, auditLog domain.AuditLog, idempotency IdempotencyRecord) (TransactionPersistenceResult, error) {
	var result TransactionPersistenceResult
	err := repository.withTx(ctx, func(tx *sql.Tx) error {
		// FOR UPDATE 表示吧这条退款申请的数据库行锁住
		request, err := scanRefundRequest(tx.QueryRowContext(ctx, `
SELECT id, tenant_id, request_no, order_id, order_snapshot, requested_amount,
       reason_code, reason_note, status, submitted_by, submitted_at
FROM refund_requests
WHERE tenant_id = $1 AND request_no = $2
FOR UPDATE
`, tenantID, requestNo))
		if errors.Is(err, sql.ErrNoRows) {
			return ErrRefundRequestNotFound
		}
		if err != nil {
			return err
		}

		responseRequest := request
		responseRequest.Status = requestStatus
		responseData, err := json.Marshal(transactionIdempotencyResponse{
			Request:     responseRequest,
			Transaction: transaction,
		})
		if err != nil {
			return fmt.Errorf("marshal transaction idempotency response: %w", err)
		}

		// 先锁定退款申请，再占用幂等键，确保并发回填只会有一个事务继续写入。
		// 通过 ON CONFLICT ... DO NOTHING 来尝试‘抢占‘这个 IdempotencyKey
		insertResult, err := tx.ExecContext(ctx, `
INSERT INTO idempotency_records (
    id, tenant_id, actor_id, operation, idempotency_key, request_hash,
    status, response_status, response_data, resource_type, resource_id,
    created_at, completed_at
) VALUES ($1, $2, $3, $4, $5, $6, 'COMPLETED', $7, $8, 'refund_transaction', $9, $10, $10)
ON CONFLICT (tenant_id, actor_id, operation, idempotency_key) DO NOTHING
`, idempotency.ID, idempotency.TenantID, idempotency.ActorID, idempotency.Operation, idempotency.Key, idempotency.RequestHash, idempotency.ResponseStatus, responseData, transaction.ID, idempotency.CreatedAt)
		if err != nil {
			return err
		}
		// 通过 RowAffected的结果判断是否抢占成功；0失败，1成功
		rowsAffected, err := insertResult.RowsAffected()
		if err != nil {
			return err
		}
		if rowsAffected == 0 {
			var existingHash string
			var existingResponse []byte
			if err := tx.QueryRowContext(ctx, `
SELECT request_hash, response_data
FROM idempotency_records
WHERE tenant_id = $1 AND actor_id = $2 AND operation = $3 AND idempotency_key = $4
`, idempotency.TenantID, idempotency.ActorID, idempotency.Operation, idempotency.Key).Scan(&existingHash, &existingResponse); err != nil {
				return err
			}
			if existingHash != idempotency.RequestHash {
				return ErrIdempotencyKeyConflict
			}

			var response transactionIdempotencyResponse
			if err := json.Unmarshal(existingResponse, &response); err != nil {
				return fmt.Errorf("unmarshal transaction idempotency response: %w", err)
			}
			result = TransactionPersistenceResult{
				Request:     response.Request,
				Transaction: response.Transaction,
				Replayed:    true,
			}
			return nil
		}

		if request.Status != domain.RefundRequestStatusApproved {
			return ErrRefundRequestNotApproved
		}

		request.Status = requestStatus
		if _, err := tx.ExecContext(ctx, `
UPDATE refund_requests
SET status = $1, updated_at = $2
WHERE tenant_id = $3 AND request_no = $4
`, requestStatus, transaction.ProcessedAt, tenantID, requestNo); err != nil {
			return err
		}
		if err := insertRefundTransaction(ctx, tx, transaction); err != nil {
			if isUniqueViolation(err, "refund_transactions_provider_refund_no_unique_idx") {
				return ErrProviderRefundNoExists
			}
			return err
		}
		if err := insertAuditLog(ctx, tx, auditLog); err != nil {
			return err
		}

		result = TransactionPersistenceResult{Request: request, Transaction: transaction}
		return nil
	})
	if err != nil {
		return TransactionPersistenceResult{}, err
	}

	return result, nil
}

// 开启事务、执行回调、成功提交、失败回滚
func (repository *PostgresRepository) withTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := repository.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if err := fn(tx); err != nil {
		return err
	}

	return tx.Commit()
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanOrder(scanner rowScanner) (domain.Order, error) {
	var order domain.Order
	var paymentStatus string
	var fulfillmentStatus string
	if err := scanner.Scan(
		&order.ID,
		&order.TenantID,
		&order.ExternalOrderNo,
		&order.CustomerID,
		&paymentStatus,
		&fulfillmentStatus,
		&order.PaidAmount,
		&order.RefundedAmount,
		&order.Currency,
		&order.PaidAt,
	); err != nil {
		return domain.Order{}, err
	}
	order.PaymentStatus = domain.PaymentStatus(paymentStatus)
	order.FulfillmentStatus = domain.FulfillmentStatus(fulfillmentStatus)
	return order, nil
}

func scanRefundRequest(scanner rowScanner) (domain.RefundRequest, error) {
	var request domain.RefundRequest
	var snapshot []byte
	var status string
	if err := scanner.Scan(
		&request.ID,
		&request.TenantID,
		&request.RequestNo,
		&request.OrderID,
		&snapshot,
		&request.RequestedAmount,
		&request.ReasonCode,
		&request.ReasonNote,
		&status,
		&request.SubmittedBy,
		&request.SubmittedAt,
	); err != nil {
		return domain.RefundRequest{}, err
	}
	if err := json.Unmarshal(snapshot, &request.OrderSnapshot); err != nil {
		return domain.RefundRequest{}, fmt.Errorf("unmarshal order snapshot: %w", err)
	}
	request.Status = domain.RefundRequestStatus(status)
	return request, nil
}

func scanApproval(scanner rowScanner) (domain.Approval, error) {
	var approval domain.Approval
	var status string
	var decisionBy sql.NullString
	var decisionAt sql.NullTime
	if err := scanner.Scan(
		&approval.ID,
		&approval.TenantID,
		&approval.RefundRequestID,
		&approval.Level,
		&status,
		&approval.AssigneeID,
		&decisionBy,
		&decisionAt,
		&approval.Comment,
	); err != nil {
		return domain.Approval{}, err
	}
	approval.Status = domain.ApprovalStatus(status)
	if decisionBy.Valid {
		approval.DecisionBy = decisionBy.String
	}
	if decisionAt.Valid {
		approval.DecisionAt = &decisionAt.Time
	}
	return approval, nil
}

func scanRefundTransaction(scanner rowScanner) (domain.RefundTransaction, error) {
	var transaction domain.RefundTransaction
	var providerRefundNo sql.NullString
	var status string
	if err := scanner.Scan(
		&transaction.ID,
		&transaction.TenantID,
		&transaction.RefundRequestID,
		&transaction.Provider,
		&providerRefundNo,
		&transaction.Amount,
		&status,
		&transaction.FailureReason,
		&transaction.ProcessedBy,
		&transaction.ProcessedAt,
	); err != nil {
		return domain.RefundTransaction{}, err
	}
	if providerRefundNo.Valid {
		transaction.ProviderRefundNo = providerRefundNo.String
	}
	transaction.Status = domain.RefundTransactionStatus(status)
	return transaction, nil
}

func insertApproval(ctx context.Context, tx *sql.Tx, approval domain.Approval) error {
	_, err := tx.ExecContext(ctx, `
INSERT INTO approvals (
    id, tenant_id, refund_request_id, level, status, assignee_id,
    decision_by, decision_at, comment, created_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
`, approval.ID, approval.TenantID, approval.RefundRequestID, approval.Level, approval.Status, approval.AssigneeID, nullString(approval.DecisionBy), approval.DecisionAt, approval.Comment, timeOrNow(approval.DecisionAt))
	return err
}

func insertRefundTransaction(ctx context.Context, tx *sql.Tx, transaction domain.RefundTransaction) error {
	_, err := tx.ExecContext(ctx, `
INSERT INTO refund_transactions (
    id, tenant_id, refund_request_id, provider, provider_refund_no, amount,
    status, failure_reason, processed_by, processed_at, created_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $10)
`, transaction.ID, transaction.TenantID, transaction.RefundRequestID, transaction.Provider, nullString(transaction.ProviderRefundNo), transaction.Amount, transaction.Status, transaction.FailureReason, transaction.ProcessedBy, transaction.ProcessedAt)
	return err
}

// 把一个 domain.AuditLog 审计日志对象转换成数据库需要的格式，然后插入 PostgreSQL 的 audit_logs 表
func insertAuditLog(ctx context.Context, tx *sql.Tx, auditLog domain.AuditLog) error {
	beforeData, err := json.Marshal(auditLog.BeforeData)
	if err != nil {
		return fmt.Errorf("marshal audit before data: %w", err)
	}
	afterData, err := json.Marshal(auditLog.AfterData)
	if err != nil {
		return fmt.Errorf("marshal audit after data: %w", err)
	}

	_, err = tx.ExecContext(ctx, `
INSERT INTO audit_logs (
    id, tenant_id, entity_type, entity_id, action, operator_id,
    before_data, after_data, created_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
`, auditLog.ID, auditLog.TenantID, auditLog.EntityType, auditLog.EntityID, auditLog.Action, auditLog.OperatorID, beforeData, afterData, auditLog.CreatedAt)
	return err
}

func nullString(value string) sql.NullString {
	if value == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: value, Valid: true}
}

func timeOrNow(value *time.Time) time.Time {
	if value == nil {
		return time.Now().UTC()
	}
	return *value
}

func isUniqueViolation(err error, constraintName string) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != postgresUniqueViolationCode {
		return false
	}
	return constraintName == "" || pgErr.ConstraintName == constraintName
}
