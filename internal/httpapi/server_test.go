package httpapi

import "testing"

func TestHistoryLimitClampsRequestedPageSize(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		raw      string
		expected int
	}{
		{name: "missing", raw: "", expected: defaultHistoryLimit},
		{name: "blank", raw: "   ", expected: defaultHistoryLimit},
		{name: "not a number", raw: "many", expected: defaultHistoryLimit},
		{name: "zero", raw: "0", expected: defaultHistoryLimit},
		{name: "negative", raw: "-5", expected: defaultHistoryLimit},
		{name: "within range", raw: "7", expected: 7},
		{name: "padded", raw: " 7 ", expected: 7},
		{name: "at the maximum", raw: "100", expected: maxHistoryLimit},
		{name: "above the maximum", raw: "100000", expected: maxHistoryLimit},
		{name: "overflowing an int", raw: "999999999999999999999", expected: defaultHistoryLimit},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			if limit := historyLimit(testCase.raw); limit != testCase.expected {
				t.Fatalf("historyLimit(%q) = %d, want %d", testCase.raw, limit, testCase.expected)
			}
		})
	}
}
