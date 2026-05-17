package overflow

import "testing"

func TestIsOverflow(t *testing.T) {
	tests := []struct {
		name   string
		model  Model
		tokens Tokens
		config Config
		want   bool
	}{
		{
			name:   "returns true when token count exceeds usable context",
			model:  createModel(100_000, 0, 32_000),
			tokens: Tokens{Input: 75_000, Output: 5_000},
			want:   true,
		},
		{
			name:   "returns false when token count is within usable context",
			model:  createModel(200_000, 0, 32_000),
			tokens: Tokens{Input: 100_000, Output: 10_000},
			want:   false,
		},
		{
			name:   "includes cache read in token count",
			model:  createModel(100_000, 0, 32_000),
			tokens: Tokens{Input: 60_000, Output: 10_000, Cache: CacheTokens{Read: 10_000}},
			want:   true,
		},
		{
			name:   "includes cache write in token count",
			model:  createModel(100_000, 0, 32_000),
			tokens: Tokens{Input: 60_000, Output: 10_000, Cache: CacheTokens{Write: 10_000}},
			want:   true,
		},
		{
			name:   "reserves headroom when input limit is set",
			model:  createModel(400_000, 272_000, 128_000),
			tokens: Tokens{Input: 271_000, Output: 1_000, Cache: CacheTokens{Read: 2_000}},
			want:   true,
		},
		{
			name:   "returns false when input and output are within input caps",
			model:  createModel(400_000, 272_000, 128_000),
			tokens: Tokens{Input: 200_000, Output: 20_000, Cache: CacheTokens{Read: 10_000}},
			want:   false,
		},
		{
			name:   "returns false when output is within limit with input caps",
			model:  createModel(200_000, 120_000, 10_000),
			tokens: Tokens{Input: 50_000, Output: 9_999},
			want:   false,
		},
		{
			name:   "compacts near input limit boundary",
			model:  createModel(200_000, 200_000, 32_000),
			tokens: Tokens{Input: 180_000, Output: 15_000, Cache: CacheTokens{Read: 3_000}},
			want:   true,
		},
		{
			name:   "without input limit compacts using context minus output",
			model:  createModel(200_000, 0, 32_000),
			tokens: Tokens{Input: 180_000, Output: 15_000, Cache: CacheTokens{Read: 3_000}},
			want:   true,
		},
		{
			name:   "returns false when model context limit is zero",
			model:  createModel(0, 0, 32_000),
			tokens: Tokens{Input: 100_000, Output: 10_000},
			want:   false,
		},
		{
			name:   "returns false when compaction auto is disabled",
			model:  createModel(100_000, 0, 32_000),
			tokens: Tokens{Input: 75_000, Output: 5_000},
			config: Config{Compaction: &CompactionConfig{Auto: boolPtr(false)}},
			want:   false,
		},
		{
			name:   "uses total token count when present",
			model:  createModel(100_000, 0, 32_000),
			tokens: Tokens{Total: 80_000, Input: 1},
			want:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsOverflow(CheckInput{
				Config: tt.config,
				Model:  tt.model,
				Tokens: tt.tokens,
			})
			if got != tt.want {
				t.Fatalf("IsOverflow() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestUsable(t *testing.T) {
	tests := []struct {
		name  string
		input Options
		want  int
	}{
		{
			name:  "zero context has zero usable tokens",
			input: Options{Model: createModel(0, 0, 32_000)},
			want:  0,
		},
		{
			name:  "without input limit reserves model output",
			input: Options{Model: createModel(100_000, 0, 32_000)},
			want:  68_000,
		},
		{
			name:  "with input limit reserves compaction buffer",
			input: Options{Model: createModel(400_000, 272_000, 128_000)},
			want:  252_000,
		},
		{
			name: "uses configured reserved tokens",
			input: Options{
				Config: Config{Compaction: &CompactionConfig{Reserved: intPtr(5_000)}},
				Model:  createModel(400_000, 272_000, 128_000),
			},
			want: 267_000,
		},
		{
			name:  "output max override lowers reservation without input limit",
			input: Options{Model: createModel(100_000, 0, 32_000), OutputTokenMax: 8_000},
			want:  92_000,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Usable(tt.input)
			if got != tt.want {
				t.Fatalf("Usable() = %d, want %d", got, tt.want)
			}
		})
	}
}

func createModel(context int, input int, output int) Model {
	return Model{Limit: Limit{Context: context, Input: input, Output: output}}
}

func boolPtr(value bool) *bool {
	return &value
}

func intPtr(value int) *int {
	return &value
}
