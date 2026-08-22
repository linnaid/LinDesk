package refund

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"lindesk/internal/domain"
)

var (
	ErrOrderNotFound                 = errors.New("order not found")                                           // 订单不存在
	ErrRefundRequestNotFound         = errors.New("refund request not found")                                  // 退款申请不存在
	ErrTenantRequired                = errors.New("tenant is required")                                        // 未指定租户
	ErrExternalOrderNoRequired       = errors.New("external order number is required")                         // 未提供外部订单号
	ErrRequestedAmountPositive       = errors.New("requested amount must be positive")                         // 申请金额必须大于零
	ErrReasonCodeRequired            = errors.New("reason code is required")                                   // 未提供退款原因码
	ErrSubmittedByRequired           = errors.New("submitted by is required")                                  // 未提供申请提交人
	ErrOrderNotRefundable            = errors.New("order is not eligible for unshipped refund")                // 订单不满足未发货退款条件
	ErrAmountExceedsRefundable       = errors.New("requested amount exceeds refundable amount")                // 申请金额超过可退余额
	ErrActiveRefundRequestExists     = errors.New("active refund request exists for order")                    // 订单已有进行中的退款申请
	ErrReviewRequestNoRequired       = errors.New("refund request number is required")                         // 审核时未提供退款申请号
	ErrApprovalDecisionByRequired    = errors.New("decision by is required")                                   // 未提供审核人
	ErrApprovalCommentRequired       = errors.New("approval comment is required")                              // 未提供审核意见
	ErrRefundRequestNotReviewable    = errors.New("refund request is not pending review")                      // 退款申请不在待审核状态
	ErrApprovalActorSameAsSubmitter  = errors.New("approver cannot be the submitter")                          // 提交人不能审核本人申请
	ErrApprovalActorAlreadyReviewed  = errors.New("approver cannot approve the same request twice")            // 同一审批人不能重复审批同一申请
	ErrApprovalLevelInvalid          = errors.New("approval level must be between 1 and 2")                    // 审批级别必须是 1 或 2
	ErrApprovalLevelMismatch         = errors.New("approval level does not match the next required level")     // 当前审批级别不匹配
	ErrApprovalLevelAlreadyProcessed = errors.New("approval level has already been processed")                 // 当前审批级别已处理
	ErrTransactionRequestNoRequired  = errors.New("refund request number is required")                         // 财务回填未提供退款申请号
	ErrTransactionProviderRequired   = errors.New("refund provider is required")                               // 未提供退款渠道
	ErrProviderRefundNoRequired      = errors.New("provider refund number is required")                        // 成功回填未提供渠道退款号
	ErrTransactionAmountPositive     = errors.New("refund transaction amount must be positive")                // 财务回填金额必须大于零
	ErrTransactionAmountMismatch     = errors.New("refund transaction amount must equal approved amount")      // 财务回填金额与批准金额不一致
	ErrTransactionProcessedByNeeded  = errors.New("processed by is required")                                  // 未提供财务操作人
	ErrTransactionStatusInvalid      = errors.New("refund transaction status is invalid")                      // 财务回填状态无效
	ErrFailureReasonRequired         = errors.New("failure reason is required")                                // 失败回填未提供失败原因
	ErrRefundRequestNotApproved      = errors.New("refund request is not approved")                            // 退款申请尚未审核通过
	ErrProviderRefundNoExists        = errors.New("provider refund number already exists")                     // 渠道退款号已经登记
	ErrIdempotencyKeyRequired        = errors.New("idempotency key is required")                               // 未提供幂等键
	ErrIdempotencyKeyTooLong         = errors.New("idempotency key must not exceed 255 characters")            // 幂等键超过长度限制
	ErrIdempotencyKeyConflict        = errors.New("idempotency key was already used with a different request") // 同一幂等键对应不同请求
)

const (
	createRefundRequestOperation     = "refund_request.create"
	createRefundTransactionOperation = "refund_transaction.create"
)

// Repository 定义退款闭环需要的持久化能力。
// 内存实现用于本地演示和单元测试，PostgreSQL 实现负责生产式事务与并发约束。
type Repository interface {
	FindOrderByExternalOrderNo(ctx context.Context, tenantID string, externalOrderNo string) (domain.Order, error)
	FindRefundRequestByIdempotency(ctx context.Context, idempotency IdempotencyRecord) (domain.RefundRequest, bool, error)
	CreateRefundRequest(ctx context.Context, request domain.RefundRequest, auditLog domain.AuditLog, idempotency IdempotencyRecord) (CreateRequestPersistenceResult, error)
	FindRefundRequestByRequestNo(ctx context.Context, tenantID string, requestNo string) (domain.RefundRequest, error)
	// 根据退款申请编号查询所有审批记录
	ListApprovalsByRequestNo(ctx context.Context, tenantID string, requestNo string) ([]domain.Approval, error)
	ListRefundTransactionsByRequestNo(ctx context.Context, tenantID string, requestNo string) ([]domain.RefundTransaction, error)
	ReviewRefundRequest(ctx context.Context, tenantID string, requestNo string, approval domain.Approval, requestStatus domain.RefundRequestStatus, auditLog domain.AuditLog) (domain.RefundRequest, error)
	// 根据幂等 Key 查找之前的退款交易；
	// 找到则返回第一次的结果，如果 Key 相同但请求参数不同，则报幂等冲突
	FindRefundTransactionByIdempotency(ctx context.Context, idempotency IdempotencyRecord) (TransactionPersistenceResult, bool, error)
	RecordRefundTransaction(ctx context.Context, tenantID string, requestNo string, transaction domain.RefundTransaction, requestStatus domain.RefundRequestStatus, auditLog domain.AuditLog, idempotency IdempotencyRecord) (TransactionPersistenceResult, error)
}

type Clock interface {
	Now() time.Time
}

type SystemClock struct{}

func (SystemClock) Now() time.Time {
	return time.Now().UTC()
}

type RequestNumberGenerator interface {
	Next(tenantID string, now time.Time) string
}

type SequentialRequestNumberGenerator struct {
	mutex            sync.Mutex
	sequenceByTenant map[string]int64
}

func NewSequentialRequestNumberGenerator() *SequentialRequestNumberGenerator {
	return &SequentialRequestNumberGenerator{sequenceByTenant: make(map[string]int64)}
}

func (generator *SequentialRequestNumberGenerator) Next(tenantID string, now time.Time) string {
	generator.mutex.Lock()
	defer generator.mutex.Unlock()

	tenantID = strings.TrimSpace(tenantID)
	generator.sequenceByTenant[tenantID]++
	return fmt.Sprintf("RR%s%04d", now.UTC().Format("20060102"), generator.sequenceByTenant[tenantID])
}

type Service struct {
	repository                  Repository
	highAmountApprovalThreshold int64
	clock                       Clock
	requestNumbers              RequestNumberGenerator
}

func NewService(repository Repository, highAmountApprovalThreshold int64, clock Clock, requestNumbers RequestNumberGenerator) *Service {
	if repository == nil {
		panic("refund repository is required")
	}
	if clock == nil {
		clock = SystemClock{}
	}
	if requestNumbers == nil {
		requestNumbers = NewSequentialRequestNumberGenerator()
	}

	return &Service{
		repository:                  repository,
		highAmountApprovalThreshold: highAmountApprovalThreshold,
		clock:                       clock,
		requestNumbers:              requestNumbers,
	}
}

type CreateRequestCommand struct {
	TenantID        string
	IdempotencyKey  string
	ExternalOrderNo string
	RequestedAmount int64
	ReasonCode      string
	ReasonNote      string
	SubmittedBy     string
}

// RequestDetail 是退款详情聚合，同时携带申请本身和审批时间线。
type RequestDetail struct {
	Request                    domain.RefundRequest
	RequiresHighAmountApproval bool
	Approvals                  []domain.Approval
	RefundTransactions         []domain.RefundTransaction
	// IdempotencyReplayed 只在创建接口复用首次结果时为 true。
	IdempotencyReplayed bool
}

// IdempotencyRecord 描述一次写操作的稳定请求指纹和首次成功结果。
type IdempotencyRecord struct {
	ID             string
	TenantID       string
	ActorID        string
	Operation      string
	Key            string
	RequestHash    string
	ResponseStatus int
	CreatedAt      time.Time
}

// CreateRequestPersistenceResult 区分首次创建和幂等结果复用。
type CreateRequestPersistenceResult struct {
	Request  domain.RefundRequest
	Replayed bool
}

// ReviewRequestCommand 只表达业务输入，不承载登录态；当前操作者由上层鉴权层注入。
type ReviewRequestCommand struct {
	TenantID      string
	RequestNo     string
	DecisionBy    string
	Comment       string
	ApprovalLevel int
}

type ReviewResult struct {
	Request  RequestDetail
	Approval domain.Approval
}

type RecordTransactionCommand struct {
	TenantID         string
	IdempotencyKey   string
	RequestNo        string
	Provider         string
	ProviderRefundNo string
	Amount           int64
	Status           domain.RefundTransactionStatus
	FailureReason    string
	ProcessedBy      string
}

type TransactionResult struct {
	Request             RequestDetail            // 最新退款申请详情
	Transaction         domain.RefundTransaction // 本次退款交易记录
	IdempotencyReplayed bool                     // 是否复用首次财务回填结果
}

// TransactionPersistenceResult 是财务回填事务的持久化结果。
type TransactionPersistenceResult struct {
	Request     domain.RefundRequest
	Transaction domain.RefundTransaction
	Replayed    bool
}

// transactionIdempotencyResponse 是保存到 response_data 的稳定响应结构。
type transactionIdempotencyResponse struct {
	Request     domain.RefundRequest     `json:"request"`
	Transaction domain.RefundTransaction `json:"transaction"`
}

func (service *Service) GetOrder(ctx context.Context, tenantID string, externalOrderNo string) (domain.Order, error) {
	tenantID = strings.TrimSpace(tenantID)
	externalOrderNo = strings.TrimSpace(externalOrderNo)
	if tenantID == "" {
		return domain.Order{}, ErrTenantRequired
	}
	if externalOrderNo == "" {
		return domain.Order{}, ErrExternalOrderNoRequired
	}

	return service.repository.FindOrderByExternalOrderNo(ctx, tenantID, externalOrderNo)
}

// CreateRequest 先校验订单资格，再固化订单快照并进入待审状态。
func (service *Service) CreateRequest(ctx context.Context, command CreateRequestCommand) (RequestDetail, error) {
	command.TenantID = strings.TrimSpace(command.TenantID)
	command.IdempotencyKey = strings.TrimSpace(command.IdempotencyKey)
	command.ExternalOrderNo = strings.TrimSpace(command.ExternalOrderNo)
	command.ReasonCode = strings.TrimSpace(command.ReasonCode)
	command.ReasonNote = strings.TrimSpace(command.ReasonNote)
	command.SubmittedBy = strings.TrimSpace(command.SubmittedBy)

	if command.TenantID == "" {
		return RequestDetail{}, ErrTenantRequired
	}
	if command.IdempotencyKey == "" {
		return RequestDetail{}, ErrIdempotencyKeyRequired
	}
	if utf8.RuneCountInString(command.IdempotencyKey) > 255 {
		return RequestDetail{}, ErrIdempotencyKeyTooLong
	}
	if command.ExternalOrderNo == "" {
		return RequestDetail{}, ErrExternalOrderNoRequired
	}
	if command.RequestedAmount <= 0 {
		return RequestDetail{}, ErrRequestedAmountPositive
	}
	if command.ReasonCode == "" {
		return RequestDetail{}, ErrReasonCodeRequired
	}
	if command.SubmittedBy == "" {
		return RequestDetail{}, ErrSubmittedByRequired
	}

	requestHash, err := createRequestHash(command)
	if err != nil {
		return RequestDetail{}, err
	}
	idempotency := IdempotencyRecord{
		ID:          idempotencyRecordID(command.TenantID, command.SubmittedBy, createRefundRequestOperation, command.IdempotencyKey),
		TenantID:    command.TenantID,
		ActorID:     command.SubmittedBy,
		Operation:   createRefundRequestOperation,
		Key:         command.IdempotencyKey,
		RequestHash: requestHash,
	}

	// 普通重试先读取首次结果，避免重复消耗新的业务单号；并发竞争仍由写事务兜底。
	existingRequest, found, err := service.repository.FindRefundRequestByIdempotency(ctx, idempotency)
	if err != nil {
		return RequestDetail{}, err
	}
	if found {
		return service.createdRequestDetail(existingRequest, true), nil
	}

	// 只有首次处理才读取实时订单状态；重试必须优先返回首次结果。
	order, err := service.repository.FindOrderByExternalOrderNo(ctx, command.TenantID, command.ExternalOrderNo)
	if err != nil {
		return RequestDetail{}, err
	}
	if !order.CanRequestUnshippedRefund() {
		return RequestDetail{}, ErrOrderNotRefundable
	}
	if command.RequestedAmount > order.RefundableAmount() {
		return RequestDetail{}, ErrAmountExceedsRefundable
	}

	now := service.clock.Now().UTC()
	requestNo := service.requestNumbers.Next(command.TenantID, now)
	request := domain.RefundRequest{
		ID:              "refund_" + command.TenantID + "_" + requestNo,
		TenantID:        command.TenantID,
		RequestNo:       requestNo,
		OrderID:         order.ID,
		OrderSnapshot:   order,
		RequestedAmount: command.RequestedAmount,
		ReasonCode:      command.ReasonCode,
		ReasonNote:      command.ReasonNote,
		Status:          domain.RefundRequestStatusPendingReview,
		SubmittedBy:     command.SubmittedBy,
		SubmittedAt:     now,
	}
	auditLog := domain.AuditLog{
		ID:         "audit_" + command.TenantID + "_" + requestNo,
		TenantID:   command.TenantID,
		EntityType: "refund_request",
		EntityID:   request.ID,
		Action:     "refund_request.created",
		OperatorID: command.SubmittedBy,
		BeforeData: map[string]any{},
		AfterData: map[string]any{
			"request_no":        request.RequestNo,
			"external_order_no": order.ExternalOrderNo,
			"requested_amount":  request.RequestedAmount,
			"status":            request.Status,
		},
		CreatedAt: now,
	}

	// 幂等键、退款申请和审计日志由 Repository 在同一临界区或数据库事务内写入。
	idempotency.ResponseStatus = 201
	idempotency.CreatedAt = now
	persistenceResult, err := service.repository.CreateRefundRequest(ctx, request, auditLog, idempotency)
	if err != nil {
		return RequestDetail{}, err
	}
	if persistenceResult.Replayed {
		return service.createdRequestDetail(persistenceResult.Request, true), nil
	}

	// 创建后的响应只包含申请本体和空时间线，直接构造可确保首次和重放响应一致。
	return service.createdRequestDetail(persistenceResult.Request, false), nil
}

func (service *Service) createdRequestDetail(request domain.RefundRequest, replayed bool) RequestDetail {
	return RequestDetail{
		Request:                    request,
		RequiresHighAmountApproval: request.RequiresHighAmountApproval(service.highAmountApprovalThreshold),
		Approvals:                  []domain.Approval{},
		RefundTransactions:         []domain.RefundTransaction{},
		IdempotencyReplayed:        replayed,
	}
}

func createRequestHash(command CreateRequestCommand) (string, error) {
	// 使用规范化后的业务字段生成摘要，避免 JSON 字段顺序影响幂等判断。
	payload := struct {
		ExternalOrderNo string `json:"external_order_no"`
		RequestedAmount int64  `json:"requested_amount"`
		ReasonCode      string `json:"reason_code"`
		ReasonNote      string `json:"reason_note"`
		SubmittedBy     string `json:"submitted_by"`
	}{
		ExternalOrderNo: command.ExternalOrderNo,
		RequestedAmount: command.RequestedAmount,
		ReasonCode:      command.ReasonCode,
		ReasonNote:      command.ReasonNote,
		SubmittedBy:     command.SubmittedBy,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal refund request idempotency payload: %w", err)
	}

	digest := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

// 根据 tenantID + actorID + operation + key 生成一个稳定、唯一性很高的幂等记录 ID
func idempotencyRecordID(tenantID, actorID, operation, key string) string {
	digest := sha256.Sum256([]byte(strings.Join([]string{tenantID, actorID, operation, key}, "\x00")))
	return "idempotency_" + hex.EncodeToString(digest[:])
}

func (service *Service) GetRequest(ctx context.Context, tenantID string, requestNo string) (RequestDetail, error) {
	tenantID = strings.TrimSpace(tenantID)
	requestNo = strings.TrimSpace(requestNo)
	if tenantID == "" {
		return RequestDetail{}, ErrTenantRequired
	}
	if requestNo == "" {
		return RequestDetail{}, ErrRefundRequestNotFound
	}

	request, err := service.repository.FindRefundRequestByRequestNo(ctx, tenantID, requestNo)
	if err != nil {
		return RequestDetail{}, err
	}

	return service.detail(ctx, request)
}

func (service *Service) ApproveRequest(ctx context.Context, command ReviewRequestCommand) (ReviewResult, error) {
	return service.reviewRequest(ctx, command, domain.ApprovalStatusApproved)
}

func (service *Service) RejectRequest(ctx context.Context, command ReviewRequestCommand) (ReviewResult, error) {
	return service.reviewRequest(ctx, command, domain.ApprovalStatusRejected)
}

// RecordTransaction 记录财务人工退款结果；只有已审批通过的申请才能结案。
func (service *Service) RecordTransaction(ctx context.Context, command RecordTransactionCommand) (TransactionResult, error) {
	command.TenantID = strings.TrimSpace(command.TenantID)
	command.IdempotencyKey = strings.TrimSpace(command.IdempotencyKey)
	command.RequestNo = strings.TrimSpace(command.RequestNo)
	command.Provider = strings.TrimSpace(command.Provider)
	command.ProviderRefundNo = strings.TrimSpace(command.ProviderRefundNo)
	command.FailureReason = strings.TrimSpace(command.FailureReason)
	command.ProcessedBy = strings.TrimSpace(command.ProcessedBy)

	if command.TenantID == "" {
		return TransactionResult{}, ErrTenantRequired
	}
	if command.IdempotencyKey == "" {
		return TransactionResult{}, ErrIdempotencyKeyRequired
	}
	if utf8.RuneCountInString(command.IdempotencyKey) > 255 {
		return TransactionResult{}, ErrIdempotencyKeyTooLong
	}
	if command.RequestNo == "" {
		return TransactionResult{}, ErrTransactionRequestNoRequired
	}
	if command.Provider == "" {
		return TransactionResult{}, ErrTransactionProviderRequired
	}
	if command.Amount <= 0 {
		return TransactionResult{}, ErrTransactionAmountPositive
	}
	if command.ProcessedBy == "" {
		return TransactionResult{}, ErrTransactionProcessedByNeeded
	}
	if command.Status != domain.RefundTransactionStatusSucceeded && command.Status != domain.RefundTransactionStatusFailed {
		return TransactionResult{}, ErrTransactionStatusInvalid
	}
	if command.Status == domain.RefundTransactionStatusSucceeded && command.ProviderRefundNo == "" {
		return TransactionResult{}, ErrProviderRefundNoRequired
	}
	if command.Status == domain.RefundTransactionStatusFailed && command.FailureReason == "" {
		return TransactionResult{}, ErrFailureReasonRequired
	}

	requestHash, err := recordTransactionHash(command)
	if err != nil {
		return TransactionResult{}, err
	}
	// 构建这次请求的幂等身份
	idempotency := IdempotencyRecord{
		ID:          idempotencyRecordID(command.TenantID, command.ProcessedBy, createRefundTransactionOperation, command.IdempotencyKey),
		TenantID:    command.TenantID,
		ActorID:     command.ProcessedBy,
		Operation:   createRefundTransactionOperation,
		Key:         command.IdempotencyKey,
		RequestHash: requestHash,
	}

	// 财务重试必须优先返回首次结果，不能被已经结案后的状态校验拦截。
	existingResult, found, err := service.repository.FindRefundTransactionByIdempotency(ctx, idempotency)
	if err != nil {
		return TransactionResult{}, err
	}
	// 如果已经被处理过，就返回第一次的结果
	if found {
		return service.transactionResult(ctx, existingResult)
	}

	// 业务状态校验 + 金额一致性校验
	request, err := service.repository.FindRefundRequestByRequestNo(ctx, command.TenantID, command.RequestNo)
	if err != nil {
		return TransactionResult{}, err
	}
	if request.Status != domain.RefundRequestStatusApproved {
		// 首次请求可能刚在前置幂等查询之后完成提交；状态冲突前再查一次，确保并发重试复用首次结果。
		existingResult, found, idempotencyErr := service.repository.FindRefundTransactionByIdempotency(ctx, idempotency)
		if idempotencyErr != nil {
			return TransactionResult{}, idempotencyErr
		}
		if found {
			return service.transactionResult(ctx, existingResult)
		}
		return TransactionResult{}, ErrRefundRequestNotApproved
	}
	if command.Amount != request.RequestedAmount {
		return TransactionResult{}, ErrTransactionAmountMismatch
	}

	now := service.clock.Now().UTC()
	transaction := domain.RefundTransaction{
		ID:               "transaction_" + command.TenantID + "_" + request.RequestNo + "_" + strings.ToLower(string(command.Status)),
		TenantID:         command.TenantID,
		RefundRequestID:  request.ID,
		Provider:         command.Provider,
		ProviderRefundNo: command.ProviderRefundNo,
		Amount:           command.Amount,
		Status:           command.Status,
		FailureReason:    command.FailureReason,
		ProcessedBy:      command.ProcessedBy,
		ProcessedAt:      now,
	}

	requestStatus := domain.RefundRequestStatusSucceeded
	action := "refund_request.refund_succeeded"
	if command.Status == domain.RefundTransactionStatusFailed {
		requestStatus = domain.RefundRequestStatusFailed
		action = "refund_request.refund_failed"
	}
	auditLog := domain.AuditLog{
		ID:         "audit_" + command.TenantID + "_" + request.RequestNo + "_" + string(command.Status),
		TenantID:   command.TenantID,
		EntityType: "refund_request",
		EntityID:   request.ID,
		Action:     action,
		OperatorID: command.ProcessedBy,
		BeforeData: map[string]any{
			"status": request.Status,
		},
		AfterData: map[string]any{
			"status":             requestStatus,
			"provider":           command.Provider,
			"provider_refund_no": command.ProviderRefundNo,
			"amount":             command.Amount,
			"transaction_status": command.Status,
			"processed_by":       command.ProcessedBy,
			"processed_at":       now,
			"failure_reason":     command.FailureReason,
		},
		CreatedAt: now,
	}

	// 幂等记录、状态更新、交易记录和审计日志必须原子提交。
	idempotency.ResponseStatus = 200
	idempotency.CreatedAt = now
	persistenceResult, err := service.repository.RecordRefundTransaction(ctx, command.TenantID, request.RequestNo, transaction, requestStatus, auditLog, idempotency)
	if err != nil {
		return TransactionResult{}, err
	}

	return service.transactionResult(ctx, persistenceResult)
}

// 把数据库层返回的 TransactionPersistenceResult 转换为业务层最终要返回的 TransactionResult
func (service *Service) transactionResult(ctx context.Context, persistenceResult TransactionPersistenceResult) (TransactionResult, error) {
	detail, err := service.detail(ctx, persistenceResult.Request)
	if err != nil {
		return TransactionResult{}, err
	}

	return TransactionResult{
		Request:             detail,
		Transaction:         persistenceResult.Transaction,
		IdempotencyReplayed: persistenceResult.Replayed,
	}, nil
}

// 根据 command 计算一个 requestHash
func recordTransactionHash(command RecordTransactionCommand) (string, error) {
	// 摘要覆盖所有会影响退款结果的业务字段，防止同一键复用不同回填内容。
	payload := struct {
		RequestNo        string                         `json:"request_no"`
		Provider         string                         `json:"provider"`
		ProviderRefundNo string                         `json:"provider_refund_no"`
		Amount           int64                          `json:"amount"`
		Status           domain.RefundTransactionStatus `json:"status"`
		FailureReason    string                         `json:"failure_reason"`
		ProcessedBy      string                         `json:"processed_by"`
	}{
		RequestNo:        command.RequestNo,
		Provider:         command.Provider,
		ProviderRefundNo: command.ProviderRefundNo,
		Amount:           command.Amount,
		Status:           command.Status,
		FailureReason:    command.FailureReason,
		ProcessedBy:      command.ProcessedBy,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal refund transaction idempotency payload: %w", err)
	}

	digest := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

// reviewRequest 让通过和驳回共享同一套校验，避免重复处理审批人、状态和备注。
func (service *Service) reviewRequest(ctx context.Context, command ReviewRequestCommand, approvalStatus domain.ApprovalStatus) (ReviewResult, error) {
	command.TenantID = strings.TrimSpace(command.TenantID)
	command.RequestNo = strings.TrimSpace(command.RequestNo)
	command.DecisionBy = strings.TrimSpace(command.DecisionBy)
	command.Comment = strings.TrimSpace(command.Comment)

	if command.TenantID == "" {
		return ReviewResult{}, ErrTenantRequired
	}
	if command.RequestNo == "" {
		return ReviewResult{}, ErrReviewRequestNoRequired
	}
	if command.DecisionBy == "" {
		return ReviewResult{}, ErrApprovalDecisionByRequired
	}
	if command.Comment == "" {
		return ReviewResult{}, ErrApprovalCommentRequired
	}
	if command.ApprovalLevel < 0 || command.ApprovalLevel > 2 {
		return ReviewResult{}, ErrApprovalLevelInvalid
	}

	request, err := service.repository.FindRefundRequestByRequestNo(ctx, command.TenantID, command.RequestNo)
	if err != nil {
		return ReviewResult{}, err
	}
	if request.Status != domain.RefundRequestStatusPendingReview {
		return ReviewResult{}, ErrRefundRequestNotReviewable
	}
	if request.SubmittedBy == command.DecisionBy {
		return ReviewResult{}, ErrApprovalActorSameAsSubmitter
	}

	approvals, err := service.repository.ListApprovalsByRequestNo(ctx, command.TenantID, request.RequestNo)
	if err != nil {
		return ReviewResult{}, err
	}
	nextLevel := nextApprovalLevel(request, approvals, service.highAmountApprovalThreshold)
	if command.ApprovalLevel != 0 && command.ApprovalLevel != nextLevel {
		return ReviewResult{}, ErrApprovalLevelMismatch
	}
	if nextLevel > 2 {
		return ReviewResult{}, ErrRefundRequestNotReviewable
	}
	for _, existingApproval := range approvals {
		if existingApproval.DecisionBy == command.DecisionBy {
			return ReviewResult{}, ErrApprovalActorAlreadyReviewed
		}
	}

	requestStatus := domain.RefundRequestStatusRejected
	if approvalStatus == domain.ApprovalStatusApproved {
		requestStatus = domain.RefundRequestStatusApproved
		if request.RequiresHighAmountApproval(service.highAmountApprovalThreshold) && nextLevel == 1 {
			requestStatus = domain.RefundRequestStatusPendingReview
		}
	}

	now := service.clock.Now().UTC()
	approval := domain.Approval{
		ID:              "approval_" + command.TenantID + "_" + request.RequestNo + "_" + fmt.Sprint(nextLevel),
		TenantID:        command.TenantID,
		RefundRequestID: request.ID,
		Level:           nextLevel,
		Status:          approvalStatus,
		AssigneeID:      command.DecisionBy,
		DecisionBy:      command.DecisionBy,
		DecisionAt:      &now,
		Comment:         command.Comment,
	}
	auditLog := domain.AuditLog{
		ID:         "audit_" + command.TenantID + "_" + request.RequestNo + "_approval_" + fmt.Sprint(nextLevel) + "_" + strings.ToLower(string(approvalStatus)),
		TenantID:   command.TenantID,
		EntityType: "refund_request",
		EntityID:   request.ID,
		Action:     "refund_request." + strings.ToLower(string(approvalStatus)),
		OperatorID: command.DecisionBy,
		BeforeData: map[string]any{
			"status": request.Status,
		},
		AfterData: map[string]any{
			"status":         requestStatus,
			"decision_by":    command.DecisionBy,
			"decision_at":    now,
			"comment":        command.Comment,
			"approval_level": approval.Level,
			"next_status":    requestStatus,
		},
		CreatedAt: now,
	}

	updatedRequest, err := service.repository.ReviewRefundRequest(ctx, command.TenantID, request.RequestNo, approval, requestStatus, auditLog)
	if err != nil {
		return ReviewResult{}, err
	}

	detail, err := service.detail(ctx, updatedRequest)
	if err != nil {
		return ReviewResult{}, err
	}

	return ReviewResult{Request: detail, Approval: approval}, nil
}

// 当前退款申请下一步需要几级审批
func nextApprovalLevel(request domain.RefundRequest, approvals []domain.Approval, threshold int64) int {
	if !request.RequiresHighAmountApproval(threshold) {
		return 1
	}

	nextLevel := 1
	for _, approval := range approvals {
		if approval.Level >= nextLevel {
			nextLevel = approval.Level + 1
		}
	}
	return nextLevel
}

// detail 查询时把审批记录一并带回，方便前端渲染完整时间线。
func (service *Service) detail(ctx context.Context, request domain.RefundRequest) (RequestDetail, error) {
	approvals, err := service.repository.ListApprovalsByRequestNo(ctx, request.TenantID, request.RequestNo)
	if err != nil {
		return RequestDetail{}, err
	}
	transactions, err := service.repository.ListRefundTransactionsByRequestNo(ctx, request.TenantID, request.RequestNo)
	if err != nil {
		return RequestDetail{}, err
	}

	return RequestDetail{
		Request:                    request,
		RequiresHighAmountApproval: request.RequiresHighAmountApproval(service.highAmountApprovalThreshold),
		Approvals:                  approvals,
		RefundTransactions:         transactions,
	}, nil
}

// 进行中状态用于阻止同一订单重复发起退款。
func IsActiveStatus(status domain.RefundRequestStatus) bool {
	switch status {
	case domain.RefundRequestStatusPendingReview,
		domain.RefundRequestStatusApproved,
		domain.RefundRequestStatusProcessing:
		return true
	default:
		return false
	}
}
