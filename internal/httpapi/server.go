package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"

	"lindesk/internal/domain"
	apprefund "lindesk/internal/refund"
)

type Dependencies struct {
	Refunds *apprefund.Service
}

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
	if deps.Refunds != nil {
		mux.HandleFunc("GET /orders/{external_order_no}", handleGetOrder(deps.Refunds))
		mux.HandleFunc("POST /refund-requests", handleCreateRefundRequest(deps.Refunds))
		mux.HandleFunc("GET /refund-requests/{request_no}", handleGetRefundRequest(deps.Refunds))
		mux.HandleFunc("POST /refund-requests/{request_no}/approve", handleApproveRefundRequest(deps.Refunds))
		mux.HandleFunc("POST /refund-requests/{request_no}/reject", handleRejectRefundRequest(deps.Refunds))
		mux.HandleFunc("POST /refund-requests/{request_no}/refund-transactions", handleRecordRefundTransaction(deps.Refunds))
	}

	return mux
}

func resolveDependencies(dependencies []Dependencies) Dependencies {
	if len(dependencies) == 0 {
		return Dependencies{}
	}

	return dependencies[0]
}

func handleGetOrder(refunds *apprefund.Service) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		order, err := refunds.GetOrder(request.Context(), request.PathValue("external_order_no"))
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
			ExternalOrderNo: body.ExternalOrderNo,
			RequestedAmount: body.RequestedAmount,
			ReasonCode:      body.ReasonCode,
			ReasonNote:      body.ReasonNote,
			SubmittedBy:     body.SubmittedBy,
		})
		if err != nil {
			writeServiceError(writer, err)
			return
		}

		writeJSON(writer, http.StatusCreated, newRefundRequestResponse(detail))
	}
}

func handleGetRefundRequest(refunds *apprefund.Service) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		detail, err := refunds.GetRequest(request.Context(), request.PathValue("request_no"))
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
		// 先用 X-Actor-ID 模拟登录态；后续接 JWT 后只需替换这里的取值来源。
		actorID := request.Header.Get("X-Actor-ID")
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
		actorID := request.Header.Get("X-Actor-ID")
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

		writeJSON(writer, http.StatusOK, transactionResultResponse{
			Request:     newRefundRequestResponse(result.Request),
			Transaction: newRefundTransactionResponse(result.Transaction),
		})
	}
}

type orderResponse struct {
	ID                string                   `json:"id"`
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

func writeServiceError(writer http.ResponseWriter, err error) {
	// 将领域错误映射成前端更容易处理的 HTTP 状态码。
	switch {
	case errors.Is(err, apprefund.ErrOrderNotFound), errors.Is(err, apprefund.ErrRefundRequestNotFound):
		writeError(writer, http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, apprefund.ErrActiveRefundRequestExists):
		writeError(writer, http.StatusConflict, "active_refund_request_exists", err.Error())
	case errors.Is(err, apprefund.ErrOrderNotRefundable), errors.Is(err, apprefund.ErrAmountExceedsRefundable):
		writeError(writer, http.StatusUnprocessableEntity, "refund_request_not_allowed", err.Error())
	case errors.Is(err, apprefund.ErrExternalOrderNoRequired),
		errors.Is(err, apprefund.ErrRequestedAmountPositive),
		errors.Is(err, apprefund.ErrReasonCodeRequired),
		errors.Is(err, apprefund.ErrSubmittedByRequired),
		errors.Is(err, apprefund.ErrReviewRequestNoRequired),
		errors.Is(err, apprefund.ErrApprovalDecisionByRequired),
		errors.Is(err, apprefund.ErrApprovalCommentRequired):
		writeError(writer, http.StatusBadRequest, "validation_failed", err.Error())
	case errors.Is(err, apprefund.ErrApprovalActorSameAsSubmitter):
		writeError(writer, http.StatusForbidden, "forbidden", err.Error())
	case errors.Is(err, apprefund.ErrRefundRequestNotReviewable):
		writeError(writer, http.StatusConflict, "refund_request_not_reviewable", err.Error())
	case errors.Is(err, apprefund.ErrRefundRequestNotApproved):
		writeError(writer, http.StatusConflict, "refund_request_not_approved", err.Error())
	case errors.Is(err, apprefund.ErrProviderRefundNoExists):
		writeError(writer, http.StatusConflict, "provider_refund_no_exists", err.Error())
	case errors.Is(err, apprefund.ErrTransactionRequestNoRequired),
		errors.Is(err, apprefund.ErrTransactionProviderRequired),
		errors.Is(err, apprefund.ErrProviderRefundNoRequired),
		errors.Is(err, apprefund.ErrTransactionAmountPositive),
		errors.Is(err, apprefund.ErrTransactionAmountMismatch),
		errors.Is(err, apprefund.ErrTransactionProcessedByNeeded),
		errors.Is(err, apprefund.ErrTransactionStatusInvalid),
		errors.Is(err, apprefund.ErrFailureReasonRequired):
		writeError(writer, http.StatusBadRequest, "validation_failed", err.Error())
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
