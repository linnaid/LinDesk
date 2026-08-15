package refund

import (
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"lindesk/internal/domain"
)

type stubScanner struct {
	values []any
	err    error
}

func (scanner stubScanner) Scan(dest ...any) error {
	if scanner.err != nil {
		return scanner.err
	}
	for index, value := range scanner.values {
		switch target := dest[index].(type) {
		case *string:
			*target = value.(string)
		case *int:
			*target = value.(int)
		case *int64:
			*target = value.(int64)
		case *time.Time:
			*target = value.(time.Time)
		case *[]byte:
			*target = value.([]byte)
		case *sql.NullString:
			*target = value.(sql.NullString)
		case *sql.NullTime:
			*target = value.(sql.NullTime)
		default:
			return errors.New("unsupported scan target")
		}
	}
	return nil
}

func TestScanRefundRequestRestoresOrderSnapshot(t *testing.T) {
	paidAt := time.Date(2026, time.August, 4, 2, 30, 0, 0, time.UTC)
	snapshot, err := json.Marshal(domain.Order{
		ID:                "order_1001",
		TenantID:          "tenant_demo",
		ExternalOrderNo:   "LD202608040001",
		CustomerID:        "customer_1001",
		PaymentStatus:     domain.PaymentStatusPaid,
		FulfillmentStatus: domain.FulfillmentStatusNotShipped,
		PaidAmount:        12_900,
		Currency:          "CNY",
		PaidAt:            paidAt,
	})
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}

	request, err := scanRefundRequest(stubScanner{values: []any{
		"refund_tenant_demo_RR202608040001",
		"tenant_demo",
		"RR202608040001",
		"order_1001",
		snapshot,
		int64(12_900),
		"CUSTOMER_CANCELLED",
		"客户取消未发货订单",
		"PENDING_REVIEW",
		"user_cs_001",
		paidAt,
	}})
	if err != nil {
		t.Fatalf("scanRefundRequest() error = %v", err)
	}
	if request.TenantID != "tenant_demo" || request.Status != domain.RefundRequestStatusPendingReview {
		t.Fatalf("request = %+v", request)
	}
	if request.OrderSnapshot.ExternalOrderNo != "LD202608040001" || request.OrderSnapshot.PaymentStatus != domain.PaymentStatusPaid {
		t.Fatalf("snapshot = %+v", request.OrderSnapshot)
	}
}

func TestNullString(t *testing.T) {
	if value := nullString(""); value.Valid {
		t.Fatalf("nullString(empty).Valid = true, want false")
	}
	if value := nullString("ALI202608040001"); !value.Valid || value.String != "ALI202608040001" {
		t.Fatalf("nullString(non-empty) = %+v", value)
	}
}

func TestTimeOrNowUsesProvidedValue(t *testing.T) {
	now := time.Date(2026, time.August, 4, 3, 0, 0, 0, time.UTC)
	if got := timeOrNow(&now); !got.Equal(now) {
		t.Fatalf("timeOrNow() = %s, want %s", got, now)
	}
}
