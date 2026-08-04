package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"lindesk/internal/refund"
)

type fixedHTTPClock struct {
	now time.Time
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

func newTestHandler() http.Handler {
	repository := refund.NewInMemoryRepository(refund.DemoOrders())
	service := refund.NewService(
		repository,
		50_000,
		fixedHTTPClock{now: time.Date(2026, time.August, 4, 3, 0, 0, 0, time.UTC)},
		refund.NewSequentialRequestNumberGenerator(),
	)

	return NewHandler("lindesk", "test", Dependencies{Refunds: service})
}
