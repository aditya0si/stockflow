package orders

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/oliaditya05/stockflow/internal/platform/apperr"
)

const (
	maxOrderLines   = 100
	maxLineQuantity = 1_000_000
)

type normalizedLine struct {
	SKU      string `json:"sku"`
	Quantity int    `json:"quantity"`
}

func Canonicalize(lines []LineRequest) ([]normalizedLine, error) {
	if len(lines) == 0 {
		return nil, apperr.BadRequest("invalid_lines", "at least one order line is required")
	}
	if len(lines) > maxOrderLines {
		return nil, apperr.BadRequest("too_many_lines", fmt.Sprintf("at most %d order lines are allowed", maxOrderLines))
	}

	quantities := make(map[string]int, len(lines))
	for _, line := range lines {
		code := strings.TrimSpace(line.SKU)
		if code == "" {
			return nil, apperr.BadRequest("invalid_sku", "each order line requires a sku code")
		}
		if line.Quantity <= 0 {
			return nil, apperr.BadRequest("invalid_quantity", "quantity for "+code+" must be a positive integer")
		}

		sum := quantities[code] + line.Quantity
		if sum > maxLineQuantity {
			return nil, apperr.BadRequest("quantity_too_large", fmt.Sprintf("total quantity for %s exceeds %d", code, maxLineQuantity))
		}
		quantities[code] = sum
	}

	codes := make([]string, 0, len(quantities))
	for code := range quantities {
		codes = append(codes, code)
	}
	sort.Strings(codes)

	normalized := make([]normalizedLine, 0, len(codes))
	for _, code := range codes {
		normalized = append(normalized, normalizedLine{SKU: code, Quantity: quantities[code]})
	}
	return normalized, nil
}

func RequestHash(lines []normalizedLine) string {
	canonical, _ := json.Marshal(lines)
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:])
}
