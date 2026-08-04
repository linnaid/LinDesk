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
	}
}

func writeJSON(writer http.ResponseWriter, status int, payload any) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(payload)
}

func writeServiceError(writer http.ResponseWriter, err error) {
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
		errors.Is(err, apprefund.ErrSubmittedByRequired):
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
