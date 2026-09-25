package fulfilment

import "testing"

func TestIsLegalTransition(t *testing.T) {
	legal := []struct{ from, to string }{
		{"accepted", "picking"},
		{"picking", "packed"},
		{"packed", "shipped"},
	}
	for _, transition := range legal {
		if !IsLegalTransition(transition.from, transition.to) {
			t.Fatalf("expected %s -> %s to be legal", transition.from, transition.to)
		}
	}

	illegal := []struct{ from, to string }{
		{"accepted", "packed"},
		{"accepted", "shipped"},
		{"picking", "shipped"},
		{"picking", "picking"},
		{"packed", "picking"},
		{"cancelled", "shipped"},
		{"shipped", "picking"},
		{"bogus", "shipped"},
	}
	for _, transition := range illegal {
		if IsLegalTransition(transition.from, transition.to) {
			t.Fatalf("expected %s -> %s to be illegal", transition.from, transition.to)
		}
	}
}
