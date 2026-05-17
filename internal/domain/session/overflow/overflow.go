// Package overflow contains session context-window overflow rules.
package overflow

const (
	compactionBuffer = 20_000
	outputTokenMax   = 32_000
)

// CompactionConfig contains the session compaction settings used by overflow checks.
type CompactionConfig struct {
	Auto     *bool
	Reserved *int
}

// Config is the subset of OpenCode config required by session overflow checks.
type Config struct {
	Compaction *CompactionConfig
}

// Model is the subset of provider model metadata required by overflow checks.
type Model struct {
	Limit Limit
}

// Limit describes provider token limits.
type Limit struct {
	Context int
	Input   int
	Output  int
}

// Tokens contains the token usage from a completed assistant message.
type Tokens struct {
	Total  int
	Input  int
	Output int
	Cache  CacheTokens
}

// CacheTokens contains prompt-cache token usage.
type CacheTokens struct {
	Read  int
	Write int
}

// Options is the input contract for Usable.
type Options struct {
	Config         Config
	Model          Model
	OutputTokenMax int
}

// CheckInput is the input contract for IsOverflow.
type CheckInput struct {
	Config         Config
	Model          Model
	Tokens         Tokens
	OutputTokenMax int
}

// Usable returns the number of tokens available before compaction is needed.
func Usable(input Options) int {
	context := input.Model.Limit.Context
	if context == 0 {
		return 0
	}

	reserved := 0
	if input.Config.Compaction != nil && input.Config.Compaction.Reserved != nil {
		reserved = *input.Config.Compaction.Reserved
	} else {
		reserved = min(compactionBuffer, MaxOutputTokens(input.Model, input.OutputTokenMax))
	}

	if input.Model.Limit.Input > 0 {
		return max(0, input.Model.Limit.Input-reserved)
	}
	return max(0, context-MaxOutputTokens(input.Model, input.OutputTokenMax))
}

// IsOverflow reports whether a finished assistant message should trigger compaction.
func IsOverflow(input CheckInput) bool {
	if input.Config.Compaction != nil && input.Config.Compaction.Auto != nil && !*input.Config.Compaction.Auto {
		return false
	}
	if input.Model.Limit.Context == 0 {
		return false
	}

	count := input.Tokens.Total
	if count == 0 {
		count = input.Tokens.Input + input.Tokens.Output + input.Tokens.Cache.Read + input.Tokens.Cache.Write
	}
	return count >= Usable(Options{
		Config:         input.Config,
		Model:          input.Model,
		OutputTokenMax: input.OutputTokenMax,
	})
}

// MaxOutputTokens mirrors ProviderTransform.maxOutputTokens.
func MaxOutputTokens(model Model, override int) int {
	if override == 0 {
		override = outputTokenMax
	}
	value := min(model.Limit.Output, override)
	if value == 0 {
		return override
	}
	return value
}
