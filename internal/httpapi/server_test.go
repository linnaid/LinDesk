package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"lindesk/internal/auth"
	"lindesk/internal/refund"
)

type fixedHTTPClock struct {
	now time.Time
}

const (
	demoTenantID = "tenant_demo"
	acmeTenantID = "tenant_acme"
)

type defaultTenantHandler struct {
	next http.Handler
}

func (handler defaultTenantHandler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if request.Header.Get("X-Tenant-ID") == "" {
		request.Header.Set("X-Tenant-ID", demoTenantID)
	}

	handler.next.ServeHTTP(writer, request)
}

func (clock fixedHTTPClock) Now() time.Time {
	return clock.now
}

func TestHealthz(t *testing.T) {
	handler := NewHandler("lindesk", "test")
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	if contentType := response.Header().Get("Content-Type"); contentType != "application/json; charset=utf-8" {
		t.Fatalf("Content-Type = %q", contentType)
	}
}

func TestGetOrder(t *testing.T) {
	handler := newTestHandler()
	request := httptest.NewRequest(http.MethodGet, "/orders/LD202608040001", nil)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	var body struct {
		ExternalOrderNo  string `json:"external_order_no"`
		RefundableAmount int64  `json:"refundable_amount"`
		CanRefund        bool   `json:"can_refund"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.ExternalOrderNo != "LD202608040001" || body.RefundableAmount != 12_900 || !body.CanRefund {
		t.Fatalf("body = %+v", body)
	}
}

func TestCreateAndGetRefundRequest(t *testing.T) {
	handler := newTestHandler()
	createBody := strings.NewReader(`{
  "external_order_no": "LD202608040001",
  "requested_amount": 12900,
  "reason_code": "CUSTOMER_CANCELLED",
  "reason_note": "客户取消未发货订单",
  "submitted_by": "user_cs_001"
}`)
	createRequest := httptest.NewRequest(http.MethodPost, "/refund-requests", createBody)
	createResponse := httptest.NewRecorder()

	handler.ServeHTTP(createResponse, createRequest)

	if createResponse.Code != http.StatusCreated {
		t.Fatalf("create status = %d, want %d, body = %s", createResponse.Code, http.StatusCreated, createResponse.Body.String())
	}
	var created struct {
		RequestNo string `json:"request_no"`
		Status    string `json:"status"`
	}
	if err := json.NewDecoder(createResponse.Body).Decode(&created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	if created.RequestNo != "RR202608040001" || created.Status != "PENDING_REVIEW" {
		t.Fatalf("created = %+v", created)
	}

	getRequest := httptest.NewRequest(http.MethodGet, "/refund-requests/"+created.RequestNo, nil)
	getResponse := httptest.NewRecorder()

	handler.ServeHTTP(getResponse, getRequest)

	if getResponse.Code != http.StatusOK {
		t.Fatalf("get status = %d, want %d", getResponse.Code, http.StatusOK)
	}
}

func TestCreateRefundRequestRejectsShippedOrder(t *testing.T) {
	handler := newTestHandler()
	createBody := strings.NewReader(`{
  "external_order_no": "LD202608040002",
  "requested_amount": 8800,
  "reason_code": "CUSTOMER_CANCELLED",
  "submitted_by": "user_cs_001"
}`)
	request := httptest.NewRequest(http.MethodPost, "/refund-requests", createBody)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusUnprocessableEntity)
	}
}

func TestApproveRefundRequest(t *testing.T) {
	handler := newTestHandler()
	createBody := strings.NewReader(`{
  "external_order_no": "LD202608040001",
  "requested_amount": 12900,
  "reason_code": "CUSTOMER_CANCELLED",
  "submitted_by": "user_cs_001"
}`)
	createRequest := httptest.NewRequest(http.MethodPost, "/refund-requests", createBody)
	createResponse := httptest.NewRecorder()
	handler.ServeHTTP(createResponse, createRequest)
	if createResponse.Code != http.StatusCreated {
		t.Fatalf("create status = %d, want %d", createResponse.Code, http.StatusCreated)
	}

	approveRequest := httptest.NewRequest(http.MethodPost, "/refund-requests/RR202608040001/approve", strings.NewReader(`{
  "comment": "订单未发货，符合退款规则"
}`))
	approveRequest.Header.Set("X-Actor-ID", "user_supervisor_001")
	approveResponse := httptest.NewRecorder()

	handler.ServeHTTP(approveResponse, approveRequest)

	if approveResponse.Code != http.StatusOK {
		t.Fatalf("approve status = %d, want %d, body = %s", approveResponse.Code, http.StatusOK, approveResponse.Body.String())
	}
	var body struct {
		Request struct {
			Status    string `json:"status"`
			Approvals []struct {
				Status  string `json:"status"`
				Comment string `json:"comment"`
			} `json:"approvals"`
		} `json:"request"`
		Approval struct {
			Status  string `json:"status"`
			Comment string `json:"comment"`
		} `json:"approval"`
	}
	if err := json.NewDecoder(approveResponse.Body).Decode(&body); err != nil {
		t.Fatalf("decode approve response: %v", err)
	}
	if body.Request.Status != "APPROVED" || body.Approval.Status != "APPROVED" {
		t.Fatalf("body = %+v", body)
	}
	if len(body.Request.Approvals) != 1 || body.Request.Approvals[0].Comment != "订单未发货，符合退款规则" {
		t.Fatalf("approvals = %+v", body.Request.Approvals)
	}
}

func TestApproveRefundRequestRejectsSelfApproval(t *testing.T) {
	handler := newTestHandler()
	createBody := strings.NewReader(`{
  "external_order_no": "LD202608040001",
  "requested_amount": 12900,
  "reason_code": "CUSTOMER_CANCELLED",
  "submitted_by": "user_cs_001"
}`)
	createRequest := httptest.NewRequest(http.MethodPost, "/refund-requests", createBody)
	createResponse := httptest.NewRecorder()
	handler.ServeHTTP(createResponse, createRequest)

	approveRequest := httptest.NewRequest(http.MethodPost, "/refund-requests/RR202608040001/approve", strings.NewReader(`{
  "comment": "不应通过"
}`))
	approveRequest.Header.Set("X-Actor-ID", "user_cs_001")
	approveResponse := httptest.NewRecorder()

	handler.ServeHTTP(approveResponse, approveRequest)

	if approveResponse.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", approveResponse.Code, http.StatusForbidden)
	}
}

func TestRefundFlowWithLoginAndRBAC(t *testing.T) {
	handler := newSecureTestHandler()

	csToken := loginToken(t, handler, "cs@lindesk.local")
	supervisorToken := loginToken(t, handler, "supervisor@lindesk.local")
	financeToken := loginToken(t, handler, "finance@lindesk.local")

	createRequest := httptest.NewRequest(http.MethodPost, "/refund-requests", strings.NewReader(`{
  "external_order_no": "LD202608040001",
  "requested_amount": 12900,
  "reason_code": "CUSTOMER_CANCELLED",
  "reason_note": "客户取消未发货订单"
}`))
	createRequest.Header.Set("Authorization", "Bearer "+csToken)
	createResponse := httptest.NewRecorder()
	handler.ServeHTTP(createResponse, createRequest)
	if createResponse.Code != http.StatusCreated {
		t.Fatalf("create status = %d, want %d, body = %s", createResponse.Code, http.StatusCreated, createResponse.Body.String())
	}

	approveRequest := httptest.NewRequest(http.MethodPost, "/refund-requests/RR202608040001/approve", strings.NewReader(`{
  "comment": "订单未发货，符合退款规则"
}`))
	approveRequest.Header.Set("Authorization", "Bearer "+supervisorToken)
	approveResponse := httptest.NewRecorder()
	handler.ServeHTTP(approveResponse, approveRequest)
	if approveResponse.Code != http.StatusOK {
		t.Fatalf("approve status = %d, want %d, body = %s", approveResponse.Code, http.StatusOK, approveResponse.Body.String())
	}

	transactionRequest := httptest.NewRequest(http.MethodPost, "/refund-requests/RR202608040001/refund-transactions", strings.NewReader(`{
  "provider": "alipay",
  "provider_refund_no": "ALI202608040001",
  "amount": 12900,
  "status": "SUCCEEDED"
}`))
	transactionRequest.Header.Set("Authorization", "Bearer "+financeToken)
	transactionResponse := httptest.NewRecorder()
	handler.ServeHTTP(transactionResponse, transactionRequest)
	if transactionResponse.Code != http.StatusOK {
		t.Fatalf("transaction status = %d, want %d, body = %s", transactionResponse.Code, http.StatusOK, transactionResponse.Body.String())
	}
}

func TestProtectedRouteRejectsMissingToken(t *testing.T) {
	handler := newSecureTestHandler()
	request := httptest.NewRequest(http.MethodGet, "/orders/LD202608040001", nil)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusUnauthorized)
	}
}

func TestLogoutRevokesBearerToken(t *testing.T) {
	handler := newSecureTestHandler()
	token := loginToken(t, handler, "cs@lindesk.local")

	logoutRequest := httptest.NewRequest(http.MethodPost, "/auth/logout", nil)
	logoutRequest.Header.Set("Authorization", "Bearer "+token)
	logoutResponse := httptest.NewRecorder()
	handler.ServeHTTP(logoutResponse, logoutRequest)
	if logoutResponse.Code != http.StatusOK {
		t.Fatalf("logout status = %d, want %d, body = %s", logoutResponse.Code, http.StatusOK, logoutResponse.Body.String())
	}

	orderRequest := httptest.NewRequest(http.MethodGet, "/orders/LD202608040001", nil)
	orderRequest.Header.Set("Authorization", "Bearer "+token)
	orderResponse := httptest.NewRecorder()
	handler.ServeHTTP(orderResponse, orderRequest)
	if orderResponse.Code != http.StatusUnauthorized {
		t.Fatalf("order status = %d, want %d", orderResponse.Code, http.StatusUnauthorized)
	}
}

func TestSecureRefundRequestsAreTenantIsolated(t *testing.T) {
	handler := newSecureTestHandler()
	demoToken := loginToken(t, handler, "cs@lindesk.local")
	acmeToken := loginToken(t, handler, "acme.cs@lindesk.local")

	demoCreated := createSecureRefundRequest(t, handler, demoToken, 12_900)
	if demoCreated.TenantID != demoTenantID {
		t.Fatalf("demo tenant_id = %q, want %q", demoCreated.TenantID, demoTenantID)
	}

	acmeCrossTenantGet := httptest.NewRequest(http.MethodGet, "/refund-requests/"+demoCreated.RequestNo, nil)
	acmeCrossTenantGet.Header.Set("Authorization", "Bearer "+acmeToken)
	acmeCrossTenantResponse := httptest.NewRecorder()
	handler.ServeHTTP(acmeCrossTenantResponse, acmeCrossTenantGet)
	if acmeCrossTenantResponse.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant get status = %d, want %d", acmeCrossTenantResponse.Code, http.StatusNotFound)
	}

	acmeCreated := createSecureRefundRequest(t, handler, acmeToken, 25_900)
	if acmeCreated.TenantID != acmeTenantID {
		t.Fatalf("acme tenant_id = %q, want %q", acmeCreated.TenantID, acmeTenantID)
	}
	if acmeCreated.RequestNo != demoCreated.RequestNo {
		t.Fatalf("request_no = %q and %q, want same tenant-scoped number", demoCreated.RequestNo, acmeCreated.RequestNo)
	}

	demoFetched := getSecureRefundRequest(t, handler, demoToken, demoCreated.RequestNo)
	acmeFetched := getSecureRefundRequest(t, handler, acmeToken, acmeCreated.RequestNo)
	if demoFetched.TenantID != demoTenantID || demoFetched.RequestedAmount != 12_900 {
		t.Fatalf("demo fetched = %+v", demoFetched)
	}
	if acmeFetched.TenantID != acmeTenantID || acmeFetched.RequestedAmount != 25_900 {
		t.Fatalf("acme fetched = %+v", acmeFetched)
	}
}

func TestRecordRefundTransaction(t *testing.T) {
	handler := newTestHandler()
	createApprovedRefundRequest(t, handler)

	transactionRequest := httptest.NewRequest(http.MethodPost, "/refund-requests/RR202608040001/refund-transactions", strings.NewReader(`{
  "provider": "alipay",
  "provider_refund_no": "ALI202608040001",
  "amount": 12900,
  "status": "SUCCEEDED"
}`))
	transactionRequest.Header.Set("X-Actor-ID", "user_finance_001")
	transactionResponse := httptest.NewRecorder()

	handler.ServeHTTP(transactionResponse, transactionRequest)

	if transactionResponse.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body = %s", transactionResponse.Code, http.StatusOK, transactionResponse.Body.String())
	}
	var body struct {
		Request struct {
			Status             string `json:"status"`
			RefundTransactions []struct {
				Status           string `json:"status"`
				ProviderRefundNo string `json:"provider_refund_no"`
			} `json:"refund_transactions"`
		} `json:"request"`
		Transaction struct {
			Status      string `json:"status"`
			ProcessedBy string `json:"processed_by"`
		} `json:"transaction"`
	}
	if err := json.NewDecoder(transactionResponse.Body).Decode(&body); err != nil {
		t.Fatalf("decode transaction response: %v", err)
	}
	if body.Request.Status != "SUCCEEDED" || body.Transaction.Status != "SUCCEEDED" {
		t.Fatalf("body = %+v", body)
	}
	if body.Transaction.ProcessedBy != "user_finance_001" {
		t.Fatalf("ProcessedBy = %q", body.Transaction.ProcessedBy)
	}
	if len(body.Request.RefundTransactions) != 1 || body.Request.RefundTransactions[0].ProviderRefundNo != "ALI202608040001" {
		t.Fatalf("refund transactions = %+v", body.Request.RefundTransactions)
	}
}

func TestRecordRefundTransactionRejectsPendingReviewRequest(t *testing.T) {
	handler := newTestHandler()
	createBody := strings.NewReader(`{
  "external_order_no": "LD202608040001",
  "requested_amount": 12900,
  "reason_code": "CUSTOMER_CANCELLED",
  "submitted_by": "user_cs_001"
}`)
	createRequest := httptest.NewRequest(http.MethodPost, "/refund-requests", createBody)
	createResponse := httptest.NewRecorder()
	handler.ServeHTTP(createResponse, createRequest)

	transactionRequest := httptest.NewRequest(http.MethodPost, "/refund-requests/RR202608040001/refund-transactions", strings.NewReader(`{
  "provider": "alipay",
  "provider_refund_no": "ALI202608040001",
  "amount": 12900,
  "status": "SUCCEEDED"
}`))
	transactionRequest.Header.Set("X-Actor-ID", "user_finance_001")
	transactionResponse := httptest.NewRecorder()

	handler.ServeHTTP(transactionResponse, transactionRequest)

	if transactionResponse.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d", transactionResponse.Code, http.StatusConflict)
	}
}

func createApprovedRefundRequest(t *testing.T, handler http.Handler) {
	t.Helper()

	createBody := strings.NewReader(`{
  "external_order_no": "LD202608040001",
  "requested_amount": 12900,
  "reason_code": "CUSTOMER_CANCELLED",
  "submitted_by": "user_cs_001"
}`)
	createRequest := httptest.NewRequest(http.MethodPost, "/refund-requests", createBody)
	createResponse := httptest.NewRecorder()
	handler.ServeHTTP(createResponse, createRequest)
	if createResponse.Code != http.StatusCreated {
		t.Fatalf("create status = %d, want %d", createResponse.Code, http.StatusCreated)
	}

	approveRequest := httptest.NewRequest(http.MethodPost, "/refund-requests/RR202608040001/approve", strings.NewReader(`{
  "comment": "订单未发货，符合退款规则"
}`))
	approveRequest.Header.Set("X-Actor-ID", "user_supervisor_001")
	approveResponse := httptest.NewRecorder()
	handler.ServeHTTP(approveResponse, approveRequest)
	if approveResponse.Code != http.StatusOK {
		t.Fatalf("approve status = %d, want %d", approveResponse.Code, http.StatusOK)
	}
}

func newTestHandler() http.Handler {
	repository := refund.NewInMemoryRepository(refund.DemoOrders())
	service := refund.NewService(
		repository,
		50_000,
		fixedHTTPClock{now: time.Date(2026, time.August, 4, 3, 0, 0, 0, time.UTC)},
		refund.NewSequentialRequestNumberGenerator(),
	)

	return defaultTenantHandler{next: NewHandler("lindesk", "test", Dependencies{Refunds: service})}
}

func newSecureTestHandler() http.Handler {
	repository := refund.NewInMemoryRepository(refund.DemoOrders())
	refundService := refund.NewService(
		repository,
		50_000,
		fixedHTTPClock{now: time.Date(2026, time.August, 4, 3, 0, 0, 0, time.UTC)},
		refund.NewSequentialRequestNumberGenerator(),
	)
	authService := auth.NewService(auth.DemoTenants(time.Date(2026, time.August, 6, 0, 0, 0, 0, time.UTC)), auth.DemoUsers(time.Date(2026, time.August, 6, 0, 0, 0, 0, time.UTC)), auth.DemoRoles(), auth.DemoMemberships(time.Date(2026, time.August, 6, 0, 0, 0, 0, time.UTC)), func() time.Time {
		return time.Date(2026, time.August, 6, 10, 0, 0, 0, time.UTC)
	})

	return NewHandler("lindesk", "test", Dependencies{Refunds: refundService, Auth: authService})
}

func loginToken(t *testing.T, handler http.Handler, email string) string {
	t.Helper()

	request := httptest.NewRequest(http.MethodPost, "/auth/login", strings.NewReader(fmt.Sprintf(`{
  "email": %q,
  "password": "password123"
}`, email)))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("login status = %d, body = %s", response.Code, response.Body.String())
	}

	var body struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode login response: %v", err)
	}
	if body.Token == "" {
		t.Fatalf("login token is empty")
	}

	return body.Token
}

func createSecureRefundRequest(t *testing.T, handler http.Handler, token string, requestedAmount int64) struct {
	TenantID        string `json:"tenant_id"`
	RequestNo       string `json:"request_no"`
	RequestedAmount int64  `json:"requested_amount"`
} {
	t.Helper()

	request := httptest.NewRequest(http.MethodPost, "/refund-requests", strings.NewReader(fmt.Sprintf(`{
  "external_order_no": "LD202608040001",
  "requested_amount": %d,
  "reason_code": "CUSTOMER_CANCELLED",
  "reason_note": "客户取消未发货订单"
}`, requestedAmount)))
	request.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("create status = %d, want %d, body = %s", response.Code, http.StatusCreated, response.Body.String())
	}

	var body struct {
		TenantID        string `json:"tenant_id"`
		RequestNo       string `json:"request_no"`
		RequestedAmount int64  `json:"requested_amount"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode create response: %v", err)
	}

	return body
}

func getSecureRefundRequest(t *testing.T, handler http.Handler, token string, requestNo string) struct {
	TenantID        string `json:"tenant_id"`
	RequestedAmount int64  `json:"requested_amount"`
} {
	t.Helper()

	request := httptest.NewRequest(http.MethodGet, "/refund-requests/"+requestNo, nil)
	request.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("get status = %d, want %d, body = %s", response.Code, http.StatusOK, response.Body.String())
	}

	var body struct {
		TenantID        string `json:"tenant_id"`
		RequestedAmount int64  `json:"requested_amount"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode get response: %v", err)
	}

	return body
}
