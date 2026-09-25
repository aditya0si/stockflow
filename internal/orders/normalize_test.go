package orders

import (
	"errors"
	"testing"

	"github.com/oliaditya05/stockflow/internal/platform/apperr"
)

func TestCanonicalizeNormalizesDuplicateLines(t *testing.T) {
	lines, err := Canonicalize([]LineRequest{
		{SKU: " B ", Quantity: 2},
		{SKU: "A", Quantity: 1},
		{SKU: "B", Quantity: 3},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(lines) != 2 {
		t.Fatalf("expected 2 normalized lines, got %d", len(lines))
	}
	if lines[0].SKU != "A" || lines[0].Quantity != 1 {
		t.Fatalf("unexpected first line: %+v", lines[0])
	}
	if lines[1].SKU != "B" || lines[1].Quantity != 5 {
		t.Fatalf("expected B to be summed to 5, got %+v", lines[1])
	}
}

func TestCanonicalizeRejectsBadInput(t *testing.T) {
	cases := []struct {
		name  string
		lines []LineRequest
		code  string
	}{
		{"empty", nil, "invalid_lines"},
		{"blank sku", []LineRequest{{SKU: " ", Quantity: 1}}, "invalid_sku"},
		{"zero quantity", []LineRequest{{SKU: "A", Quantity: 0}}, "invalid_quantity"},
		{"negative quantity", []LineRequest{{SKU: "A", Quantity: -1}}, "invalid_quantity"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := Canonicalize(testCase.lines)
			var domainErr *apperr.Error
			if !errors.As(err, &domainErr) {
				t.Fatalf("expected apperr.Error, got %v", err)
			}
			if domainErr.Code != testCase.code {
				t.Fatalf("expected code %s, got %s", testCase.code, domainErr.Code)
			}
			if domainErr.Status != 400 {
				t.Fatalf("expected status 400, got %d", domainErr.Status)
			}
		})
	}
}

func TestRequestHashIsStableAndPayloadSensitive(t *testing.T) {
	first := mustCanonicalize(t, []LineRequest{
		{SKU: "A", Quantity: 1},
		{SKU: "B", Quantity: 2},
	})
	reordered := mustCanonicalize(t, []LineRequest{
		{SKU: "B", Quantity: 2},
		{SKU: "A", Quantity: 1},
	})
	if RequestHash(first) != RequestHash(reordered) {
		t.Fatal("hash should not depend on request line order")
	}

	changed := mustCanonicalize(t, []LineRequest{
		{SKU: "A", Quantity: 1},
		{SKU: "B", Quantity: 3},
	})
	if RequestHash(first) == RequestHash(changed) {
		t.Fatal("hash should change when a quantity changes")
	}
}

func mustCanonicalize(t *testing.T, lines []LineRequest) []normalizedLine {
	t.Helper()
	normalized, err := Canonicalize(lines)
	if err != nil {
		t.Fatalf("canonicalize: %v", err)
	}
	return normalized
}
