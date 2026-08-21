package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"lindesk/internal/auth"
	"lindesk/internal/domain"
	apprefund "lindesk/internal/refund"
)

type Dependencies struct {
	Refunds *apprefund.Service
	Auth    auth.Authenticator
}

type actorContextKey struct{}

// 返回当前进程的网页服务。后续实现第一版流程时，业务接口会挂载到这个路由上。
func NewServer(address, serviceName, version string, dependencies ...Dependencies) *http.Server {
	return &http.Server{
		Addr:    address,
		Handler: NewHandler(serviceName, version, dependencies...),
	}
}

func NewHandler(serviceName, version string, dependencies ...Dependencies) http.Handler {
	deps := resolveDependencies(dependencies)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", func(writer http.ResponseWriter, request *http.Request) {
		writeJSON(writer, http.StatusOK, map[string]string{
			"service": serviceName,
			"version": version,
		})
	})
	mux.HandleFunc("GET /healthz", func(writer http.ResponseWriter, request *http.Request) {
		writeJSON(writer, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /readyz", func(writer http.ResponseWriter, request *http.Request) {
		writeJSON(writer, http.StatusOK, map[string]string{"status": "ready"})
	})
	if deps.Auth != nil {
		mux.HandleFunc("POST /auth/login", handleLogin(deps.Auth))
		mux.HandleFunc("POST /auth/logout", handleLogout(deps.Auth))
	}
	if deps.Refunds != nil {
		mux.HandleFunc("GET /orders/{external_order_no}", requirePermission(deps.Auth, domain.PermissionOrderRead, handleGetOrder(deps.Refunds)))
		mux.HandleFunc("POST /refund-requests", requirePermission(deps.Auth, domain.PermissionRefundRequestCreate, handleCreateRefundRequest(deps.Refunds)))
		mux.HandleFunc("GET /refund-requests/{request_no}", requirePermission(deps.Auth, domain.PermissionRefundRequestRead, handleGetRefundRequest(deps.Refunds)))
		mux.HandleFunc("POST /refund-requests/{request_no}/approve", requirePermission(deps.Auth, domain.PermissionRefundRequestReview, handleApproveRefundRequest(deps.Refunds)))
		mux.HandleFunc("POST /refund-requests/{request_no}/reject", requirePermission(deps.Auth, domain.PermissionRefundRequestReview, handleRejectRefundRequest(deps.Refunds)))
		mux.HandleFunc("POST /refund-requests/{request_no}/refund-transactions", requirePermission(deps.Auth, domain.PermissionRefundTransactionWrite, handleRecordRefundTransaction(deps.Refunds)))
	}

	return mux
}

func resolveDependencies(dependencies []Dependencies) Dependencies {
	if len(dependencies) == 0 {
		return Dependencies{}
	}

	return dependencies[0]
}

func handleLogin(auths auth.Authenticator) http.HandlerFunc {
	type payload struct {
		Email    string `json:"email"`
		Password string `json:"password"`
		TenantID string `json:"tenant_id"`
	}

	return func(writer http.ResponseWriter, request *http.Request) {
		var body payload
		decoder := json.NewDecoder(http.MaxBytesReader(writer, request.Body, 1<<20))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&body); err != nil {
			writeError(writer, http.StatusBadRequest, "invalid_json", "请求体必须是合法 JSON")
			return
		}

		session, err := auths.Login(request.Context(), auth.LoginCommand{
			Email:    body.Email,
			Password: body.Password,
			TenantID: body.TenantID,
		})
		if err != nil {
			writeAuthError(writer, err)
			return
		}

		writeJSON(writer, http.StatusOK, loginResponse{
			Token:     session.Token,
			TokenType: "Bearer",
			ExpiresAt: session.ExpiresAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
			Actor:     newActorResponse(session.Actor),
		})
	}
}

func handleLogout(auths auth.Authenticator) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		if err := auths.Logout(request.Context(), bearerToken(request.Header.Get("Authorization"))); err != nil {
			writeAuthError(writer, err)
			return
		}

		writeJSON(writer, http.StatusOK, map[string]string{"status": "logged_out"})
	}
}

// 为 HTTP handler 添加认证和权限校验
// 只有当前用户已登录且拥有指定权限时，才会执行被包装的 handler
func requirePermission(auths auth.Authenticator, permission domain.Permission, next http.HandlerFunc) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		if auths == nil {
			next(writer, request)
			return
		}

		actor, err := auths.Authenticate(request.Context(), bearerToken(request.Header.Get("Authorization")))
		if err != nil {
			writeAuthError(writer, err)
			return
		}
		if err := auth.RequirePermission(actor, permission); err != nil {
			writeAuthError(writer, err)
			return
		}

		ctx := context.WithValue(request.Context(), actorContextKey{}, actor)
		next(writer, request.WithContext(ctx))
	}
}

func currentActor(request *http.Request) (domain.Actor, bool) {
	actor, ok := request.Context().Value(actorContextKey{}).(domain.Actor)
	return actor, ok
}

func currentActorID(request *http.Request) string {
	if actor, ok := currentActor(request); ok {
		return actor.User.ID
	}

	return request.Header.Get("X-Actor-ID")
}

func currentTenantID(request *http.Request) string {
	if actor, ok := currentActor(request); ok {
		return actor.Tenant.ID
	}

	return request.Header.Get("X-Tenant-ID")
}

func bearerToken(header string) string {
	header = strings.TrimSpace(header)
	if header == "" {
		return ""
	}

	prefix := "Bearer "
	if !strings.HasPrefix(header, prefix) {
		return ""
	}

	return strings.TrimSpace(strings.TrimPrefix(header, prefix))
}

// 返回一个 HTTP handler，用户根据外部订单号查询订单
// 具体查询逻辑由 refund service 负责，handler 只负责处理HTTP请求和响应
func handleGetOrder(refunds *apprefund.Service) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		order, err := refunds.GetOrder(request.Context(), currentTenantID(request), request.PathValue("external_order_no"))
		if err != nil {
			writeServiceError(writer, err)
			return
		}

		writeJSON(writer, http.StatusOK, newOrderResponse(order))
	}
}

func handleCreateRefundRequest(refunds *apprefund.Service) http.HandlerFunc {
	type payload struct {
		ExternalOrderNo string `json:"external_order_no"`
		RequestedAmount int64  `json:"requested_amount"`
		ReasonCode      string `json:"reason_code"`
		ReasonNote      string `json:"reason_note"`
		SubmittedBy     string `json:"submitted_by"`
	}

	return func(writer http.ResponseWriter, request *http.Request) {
		var body payload
		decoder := json.NewDecoder(http.MaxBytesReader(writer, request.Body, 1<<20))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&body); err != nil {
			writeError(writer, http.StatusBadRequest, "invalid_json", "request body must be valid JSON")
			return
		}

		detail, err := refunds.CreateRequest(request.Context(), apprefund.CreateRequestCommand{
			TenantID:        currentTenantID(request),
			IdempotencyKey:  request.Header.Get("Idempotency-Key"),
			ExternalOrderNo: body.ExternalOrderNo,
			RequestedAmount: body.RequestedAmount,
			ReasonCode:      body.ReasonCode,
			ReasonNote:      body.ReasonNote,
			SubmittedBy:     submittedBy(request, body.SubmittedBy),
		})
		if err != nil {
			writeServiceError(writer, err)
			return
		}

		if detail.IdempotencyReplayed {
			// 客户端可以据此区分首次创建和服务端复用的首次结果。
			writer.Header().Set("Idempotency-Replayed", "true")
		}
		writeJSON(writer, http.StatusCreated, newRefundRequestResponse(detail))
	}
}

func handleGetRefundRequest(refunds *apprefund.Service) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		detail, err := refunds.GetRequest(request.Context(), currentTenantID(request), request.PathValue("request_no"))
		if err != nil {
			writeServiceError(writer, err)
			return
		}

		writeJSON(writer, http.StatusOK, newRefundRequestResponse(detail))
	}
}

func handleApproveRefundRequest(refunds *apprefund.Service) http.HandlerFunc {
	return handleReviewRefundRequest(refunds, true)
}

func handleRejectRefundRequest(refunds *apprefund.Service) http.HandlerFunc {
	return handleReviewRefundRequest(refunds, false)
}

func handleReviewRefundRequest(refunds *apprefund.Service, approved bool) http.HandlerFunc {
	type payload struct {
		Comment string `json:"comment"`
	}

	return func(writer http.ResponseWriter, request *http.Request) {
		actorID := currentActorID(request)
		if actorID == "" {
			writeError(writer, http.StatusBadRequest, "validation_failed", "X-Actor-ID 不能为空")
			return
		}

		var body payload
		decoder := json.NewDecoder(http.MaxBytesReader(writer, request.Body, 1<<20))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&body); err != nil {
			writeError(writer, http.StatusBadRequest, "invalid_json", "请求体必须是合法 JSON")
			return
		}

		command := apprefund.ReviewRequestCommand{
			TenantID:   currentTenantID(request),
			RequestNo:  request.PathValue("request_no"),
			DecisionBy: actorID,
			Comment:    body.Comment,
		}

		var (
			result apprefund.ReviewResult
			err    error
		)
		if approved {
			result, err = refunds.ApproveRequest(request.Context(), command)
		} else {
			result, err = refunds.RejectRequest(request.Context(), command)
		}
		if err != nil {
			writeServiceError(writer, err)
			return
		}

		writeJSON(writer, http.StatusOK, reviewResultResponse{
			Request:  newRefundRequestResponse(result.Request),
			Approval: newApprovalResponse(result.Approval),
		})
	}
}

func handleRecordRefundTransaction(refunds *apprefund.Service) http.HandlerFunc {
	type payload struct {
		Provider         string                         `json:"provider"`
		ProviderRefundNo string                         `json:"provider_refund_no"`
		Amount           int64                          `json:"amount"`
		Status           domain.RefundTransactionStatus `json:"status"`
		FailureReason    string                         `json:"failure_reason"`
	}

	return func(writer http.ResponseWriter, request *http.Request) {
		actorID := currentActorID(request)
		if actorID == "" {
			writeError(writer, http.StatusBadRequest, "validation_failed", "X-Actor-ID 不能为空")
			return
		}

		var body payload
		decoder := json.NewDecoder(http.MaxBytesReader(writer, request.Body, 1<<20))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&body); err != nil {
			writeError(writer, http.StatusBadRequest, "invalid_json", "请求体必须是合法 JSON")
			return
		}

		result, err := refunds.RecordTransaction(request.Context(), apprefund.RecordTransactionCommand{
			TenantID:         currentTenantID(request),
			IdempotencyKey:   request.Header.Get("Idempotency-Key"),
			RequestNo:        request.PathValue("request_no"),
			Provider:         body.Provider,
			ProviderRefundNo: body.ProviderRefundNo,
			Amount:           body.Amount,
			Status:           body.Status,
			FailureReason:    body.FailureReason,
			ProcessedBy:      actorID,
		})
		if err != nil {
			writeServiceError(writer, err)
			return
		}

		if result.IdempotencyReplayed {
			// 与创建退款申请保持一致，重放时显式告知客户端。
			writer.Header().Set("Idempotency-Replayed", "true")
		}
		writeJSON(writer, http.StatusOK, transactionResultResponse{
			Request:     newRefundRequestResponse(result.Request),
			Transaction: newRefundTransactionResponse(result.Transaction),
		})
	}
}

type loginResponse struct {
	Token     string        `json:"token"`
	TokenType string        `json:"token_type"`
	ExpiresAt string        `json:"expires_at"`
	Actor     actorResponse `json:"actor"`
}

type actorResponse struct {
	TenantID   string         `json:"tenant_id"`
	TenantName string         `json:"tenant_name"`
	UserID     string         `json:"user_id"`
	UserName   string         `json:"user_name"`
	Email      string         `json:"email"`
	Roles      []roleResponse `json:"roles"`
}

type roleResponse struct {
	Code        domain.RoleCode     `json:"code"`
	Name        string              `json:"name"`
	Permissions []domain.Permission `json:"permissions"`
}

func newActorResponse(actor domain.Actor) actorResponse {
	roles := make([]roleResponse, 0, len(actor.Roles))
	for _, role := range actor.Roles {
		roles = append(roles, roleResponse{
			Code:        role.Code,
			Name:        role.Name,
			Permissions: append([]domain.Permission(nil), role.Permissions...),
		})
	}

	return actorResponse{
		TenantID:   actor.Tenant.ID,
		TenantName: actor.Tenant.Name,
		UserID:     actor.User.ID,
		UserName:   actor.User.Name,
		Email:      actor.User.Email,
		Roles:      roles,
	}
}

func submittedBy(request *http.Request, fallback string) string {
	if actor, ok := currentActor(request); ok {
		return actor.User.ID
	}

	return fallback
}

type orderResponse struct {
	ID                string                   `json:"id"`
	TenantID          string                   `json:"tenant_id"`
	ExternalOrderNo   string                   `json:"external_order_no"`
	CustomerID        string                   `json:"customer_id"`
	PaymentStatus     domain.PaymentStatus     `json:"payment_status"`
	FulfillmentStatus domain.FulfillmentStatus `json:"fulfillment_status"`
	PaidAmount        int64                    `json:"paid_amount"`
	RefundedAmount    int64                    `json:"refunded_amount"`
	RefundableAmount  int64                    `json:"refundable_amount"`
	Currency          string                   `json:"currency"`
	PaidAt            string                   `json:"paid_at"`
	CanRefund         bool                     `json:"can_refund"`
}

type refundRequestResponse struct {
	ID                         string                     `json:"id"`
	TenantID                   string                     `json:"tenant_id"`
	RequestNo                  string                     `json:"request_no"`
	OrderID                    string                     `json:"order_id"`
	OrderSnapshot              orderResponse              `json:"order_snapshot"`
	RequestedAmount            int64                      `json:"requested_amount"`
	ReasonCode                 string                     `json:"reason_code"`
	ReasonNote                 string                     `json:"reason_note"`
	Status                     domain.RefundRequestStatus `json:"status"`
	SubmittedBy                string                     `json:"submitted_by"`
	SubmittedAt                string                     `json:"submitted_at"`
	RequiresHighAmountApproval bool                       `json:"requires_high_amount_approval"`
	// Approvals 按时间线返回，方便前端展示审核历史。
	Approvals          []approvalResponse          `json:"approvals"`
	RefundTransactions []refundTransactionResponse `json:"refund_transactions"`
}

type approvalResponse struct {
	ID              string                `json:"id"`
	TenantID        string                `json:"tenant_id"`
	RefundRequestID string                `json:"refund_request_id"`
	Level           int                   `json:"level"`
	Status          domain.ApprovalStatus `json:"status"`
	AssigneeID      string                `json:"assignee_id"`
	DecisionBy      string                `json:"decision_by"`
	DecisionAt      string                `json:"decision_at"`
	Comment         string                `json:"comment"`
}

type reviewResultResponse struct {
	Request  refundRequestResponse `json:"request"`
	Approval approvalResponse      `json:"approval"`
}

type refundTransactionResponse struct {
	ID               string                         `json:"id"`
	TenantID         string                         `json:"tenant_id"`
	RefundRequestID  string                         `json:"refund_request_id"`
	Provider         string                         `json:"provider"`
	ProviderRefundNo string                         `json:"provider_refund_no"`
	Amount           int64                          `json:"amount"`
	Status           domain.RefundTransactionStatus `json:"status"`
	FailureReason    string                         `json:"failure_reason"`
	ProcessedBy      string                         `json:"processed_by"`
	ProcessedAt      string                         `json:"processed_at"`
}

type transactionResultResponse struct {
	Request     refundRequestResponse     `json:"request"`
	Transaction refundTransactionResponse `json:"transaction"`
}

func newOrderResponse(order domain.Order) orderResponse {
	return orderResponse{
		ID:                order.ID,
		TenantID:          order.TenantID,
		ExternalOrderNo:   order.ExternalOrderNo,
		CustomerID:        order.CustomerID,
		PaymentStatus:     order.PaymentStatus,
		FulfillmentStatus: order.FulfillmentStatus,
		PaidAmount:        order.PaidAmount,
		RefundedAmount:    order.RefundedAmount,
		RefundableAmount:  order.RefundableAmount(),
		Currency:          order.Currency,
		PaidAt:            order.PaidAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
		CanRefund:         order.CanRequestUnshippedRefund(),
	}
}

func newRefundRequestResponse(detail apprefund.RequestDetail) refundRequestResponse {
	request := detail.Request
	approvals := make([]approvalResponse, 0, len(detail.Approvals))
	for _, approval := range detail.Approvals {
		approvals = append(approvals, newApprovalResponse(approval))
	}
	transactions := make([]refundTransactionResponse, 0, len(detail.RefundTransactions))
	for _, transaction := range detail.RefundTransactions {
		transactions = append(transactions, newRefundTransactionResponse(transaction))
	}
	return refundRequestResponse{
		ID:                         request.ID,
		TenantID:                   request.TenantID,
		RequestNo:                  request.RequestNo,
		OrderID:                    request.OrderID,
		OrderSnapshot:              newOrderResponse(request.OrderSnapshot),
		RequestedAmount:            request.RequestedAmount,
		ReasonCode:                 request.ReasonCode,
		ReasonNote:                 request.ReasonNote,
		Status:                     request.Status,
		SubmittedBy:                request.SubmittedBy,
		SubmittedAt:                request.SubmittedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
		RequiresHighAmountApproval: detail.RequiresHighAmountApproval,
		Approvals:                  approvals,
		RefundTransactions:         transactions,
	}
}

func newApprovalResponse(approval domain.Approval) approvalResponse {
	decisionAt := ""
	if approval.DecisionAt != nil {
		decisionAt = approval.DecisionAt.UTC().Format("2006-01-02T15:04:05Z07:00")
	}

	return approvalResponse{
		ID:              approval.ID,
		TenantID:        approval.TenantID,
		RefundRequestID: approval.RefundRequestID,
		Level:           approval.Level,
		Status:          approval.Status,
		AssigneeID:      approval.AssigneeID,
		DecisionBy:      approval.DecisionBy,
		DecisionAt:      decisionAt,
		Comment:         approval.Comment,
	}
}

func newRefundTransactionResponse(transaction domain.RefundTransaction) refundTransactionResponse {
	return refundTransactionResponse{
		ID:               transaction.ID,
		TenantID:         transaction.TenantID,
		RefundRequestID:  transaction.RefundRequestID,
		Provider:         transaction.Provider,
		ProviderRefundNo: transaction.ProviderRefundNo,
		Amount:           transaction.Amount,
		Status:           transaction.Status,
		FailureReason:    transaction.FailureReason,
		ProcessedBy:      transaction.ProcessedBy,
		ProcessedAt:      transaction.ProcessedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
	}
}

func writeJSON(writer http.ResponseWriter, status int, payload any) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(payload)
}

func writeAuthError(writer http.ResponseWriter, err error) {
	switch {
	// 400 请求参数缺失或格式不满足认证接口要求
	case errors.Is(err, auth.ErrEmailRequired),
		errors.Is(err, auth.ErrPasswordRequired),
		errors.Is(err, auth.ErrTenantRequired):
		writeError(writer, http.StatusBadRequest, "validation_failed", err.Error())
	// 401 请求未通过身份认证，或认证凭证缺失、无效、已过期
	case errors.Is(err, auth.ErrInvalidCredentials),
		errors.Is(err, auth.ErrInvalidToken),
		errors.Is(err, auth.ErrSessionExpired),
		errors.Is(err, auth.ErrTokenRequired):
		writeError(writer, http.StatusUnauthorized, "unauthorized", err.Error())
	// 403 当前用户无法确定或访问有效的租户上下文
	// 为避免暴露租户存在性及成员关系，对外统一返回 403 Forbidden
	case errors.Is(err, auth.ErrTenantNotFound),
		errors.Is(err, auth.ErrNoActiveMembership),
		errors.Is(err, auth.ErrAmbiguousMembership):
		writeError(writer, http.StatusForbidden, "forbidden", err.Error())
	// 403 用户已完成身份认证，但缺少执行当前操作所需的权限
	case errors.Is(err, auth.ErrPermissionDenied):
		writeError(writer, http.StatusForbidden, "forbidden", err.Error())
	// 500 未知错误
	default:
		writeError(writer, http.StatusInternalServerError, "internal_error", "internal server error")
	}
}

func writeServiceError(writer http.ResponseWriter, err error) {
	// 将领域错误映射成前端更容易处理的 HTTP 状态码。
	switch {
	// 404 资源不存在
	case errors.Is(err, apprefund.ErrOrderNotFound), errors.Is(err, apprefund.ErrRefundRequestNotFound):
		writeError(writer, http.StatusNotFound, "not_found", err.Error())
	// 409 状态冲突
	case errors.Is(err, apprefund.ErrActiveRefundRequestExists):
		writeError(writer, http.StatusConflict, "active_refund_request_exists", err.Error())
	case errors.Is(err, apprefund.ErrIdempotencyKeyConflict):
		writeError(writer, http.StatusConflict, "idempotency_key_conflict", err.Error())
	// 422 业务规则不允许
	case errors.Is(err, apprefund.ErrOrderNotRefundable), errors.Is(err, apprefund.ErrAmountExceedsRefundable):
		writeError(writer, http.StatusUnprocessableEntity, "refund_request_not_allowed", err.Error())
	// 400 参数校验失败
	case errors.Is(err, apprefund.ErrExternalOrderNoRequired),
		errors.Is(err, apprefund.ErrTenantRequired),
		errors.Is(err, apprefund.ErrIdempotencyKeyRequired),
		errors.Is(err, apprefund.ErrIdempotencyKeyTooLong),
		errors.Is(err, apprefund.ErrRequestedAmountPositive),
		errors.Is(err, apprefund.ErrReasonCodeRequired),
		errors.Is(err, apprefund.ErrSubmittedByRequired),
		errors.Is(err, apprefund.ErrReviewRequestNoRequired),
		errors.Is(err, apprefund.ErrApprovalDecisionByRequired),
		errors.Is(err, apprefund.ErrApprovalCommentRequired):
		writeError(writer, http.StatusBadRequest, "validation_failed", err.Error())
	// 403 权限禁止
	case errors.Is(err, apprefund.ErrApprovalActorSameAsSubmitter):
		writeError(writer, http.StatusForbidden, "forbidden", err.Error())
	// 409 退款申请状态错误
	case errors.Is(err, apprefund.ErrRefundRequestNotReviewable):
		writeError(writer, http.StatusConflict, "refund_request_not_reviewable", err.Error())
	// 409 退款未批准
	case errors.Is(err, apprefund.ErrRefundRequestNotApproved):
		writeError(writer, http.StatusConflict, "refund_request_not_approved", err.Error())
	// 409 退款流水号冲突
	case errors.Is(err, apprefund.ErrProviderRefundNoExists):
		writeError(writer, http.StatusConflict, "provider_refund_no_exists", err.Error())
	// 400 退款交易参数错误
	case errors.Is(err, apprefund.ErrTransactionRequestNoRequired),
		errors.Is(err, apprefund.ErrTransactionProviderRequired),
		errors.Is(err, apprefund.ErrProviderRefundNoRequired),
		errors.Is(err, apprefund.ErrTransactionAmountPositive),
		errors.Is(err, apprefund.ErrTransactionAmountMismatch),
		errors.Is(err, apprefund.ErrTransactionProcessedByNeeded),
		errors.Is(err, apprefund.ErrTransactionStatusInvalid),
		errors.Is(err, apprefund.ErrFailureReasonRequired):
		writeError(writer, http.StatusBadRequest, "validation_failed", err.Error())
	// 500 未知错误
	default:
		writeError(writer, http.StatusInternalServerError, "internal_error", "internal server error")
	}
}

func writeError(writer http.ResponseWriter, status int, code, message string) {
	writeJSON(writer, status, map[string]any{
		"error": map[string]string{
			"code":    code,
			"message": message,
		},
	})
}
