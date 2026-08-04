package refund

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"lindesk/internal/domain"
)

var (
	ErrOrderNotFound             = errors.New("order not found")
	ErrRefundRequestNotFound     = errors.New("refund request not found")
	ErrExternalOrderNoRequired   = errors.New("external order number is required")
	ErrRequestedAmountPositive   = errors.New("requested amount must be positive")
	ErrReasonCodeRequired        = errors.New("reason code is required")
	ErrSubmittedByRequired       = errors.New("submitted by is required")
	ErrOrderNotRefundable        = errors.New("order is not eligible for unshipped refund")
	ErrAmountExceedsRefundable   = errors.New("requested amount exceeds refundable amount")
	ErrActiveRefundRequestExists = errors.New("active refund request exists for order")
)

type Repository interface {
	FindOrderByExternalOrderNo(ctx context.Context, externalOrderNo string) (domain.Order, error)
	CreateRefundRequest(ctx context.Context, request domain.RefundRequest, auditLog domain.AuditLog) error
	FindRefundRequestByRequestNo(ctx context.Context, requestNo string) (domain.RefundRequest, error)
}

type Clock interface {
	Now() time.Time
}

type SystemClock struct{}

func (SystemClock) Now() time.Time {
	return time.Now().UTC()
}

type RequestNumberGenerator interface {
	Next(now time.Time) string
}

type SequentialRequestNumberGenerator struct {
	mutex    sync.Mutex
	sequence int64
}

func NewSequentialRequestNumberGenerator() *SequentialRequestNumberGenerator {
	return &SequentialRequestNumberGenerator{}
}

func (generator *SequentialRequestNumberGenerator) Next(now time.Time) string {
	generator.mutex.Lock()
	defer generator.mutex.Unlock()

	generator.sequence++
	return fmt.Sprintf("RR%s%04d", now.UTC().Format("20060102"), generator.sequence)
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
	ExternalOrderNo string
	RequestedAmount int64
	ReasonCode      string
	ReasonNote      string
	SubmittedBy     string
}

type RequestDetail struct {
	Request                    domain.RefundRequest
	RequiresHighAmountApproval bool
}

func (service *Service) GetOrder(ctx context.Context, externalOrderNo string) (domain.Order, error) {
	externalOrderNo = strings.TrimSpace(externalOrderNo)
	if externalOrderNo == "" {
		return domain.Order{}, ErrExternalOrderNoRequired
	}

	return service.repository.FindOrderByExternalOrderNo(ctx, externalOrderNo)
}

func (service *Service) CreateRequest(ctx context.Context, command CreateRequestCommand) (RequestDetail, error) {
	command.ExternalOrderNo = strings.TrimSpace(command.ExternalOrderNo)
	command.ReasonCode = strings.TrimSpace(command.ReasonCode)
	command.ReasonNote = strings.TrimSpace(command.ReasonNote)
	command.SubmittedBy = strings.TrimSpace(command.SubmittedBy)

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

	order, err := service.repository.FindOrderByExternalOrderNo(ctx, command.ExternalOrderNo)
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
	requestNo := service.requestNumbers.Next(now)
	request := domain.RefundRequest{
		ID:              "refund_" + requestNo,
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
		ID:         "audit_" + requestNo,
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

	if err := service.repository.CreateRefundRequest(ctx, request, auditLog); err != nil {
		return RequestDetail{}, err
	}

	return service.detail(request), nil
}

func (service *Service) GetRequest(ctx context.Context, requestNo string) (RequestDetail, error) {
	requestNo = strings.TrimSpace(requestNo)
	if requestNo == "" {
		return RequestDetail{}, ErrRefundRequestNotFound
	}

	request, err := service.repository.FindRefundRequestByRequestNo(ctx, requestNo)
	if err != nil {
		return RequestDetail{}, err
	}

	return service.detail(request), nil
}

func (service *Service) detail(request domain.RefundRequest) RequestDetail {
	return RequestDetail{
		Request:                    request,
		RequiresHighAmountApproval: request.RequiresHighAmountApproval(service.highAmountApprovalThreshold),
	}
}

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
