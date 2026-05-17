package app

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/RecoveryAshes/opencode/internal/domain/session"
)

type sessionStats struct {
	TotalSessions          int                   `json:"totalSessions"`
	TotalMessages          int                   `json:"totalMessages"`
	TotalCost              float64               `json:"totalCost"`
	TotalTokens            statsTokens           `json:"totalTokens"`
	ToolUsage              map[string]int        `json:"toolUsage"`
	ModelUsage             map[string]modelUsage `json:"modelUsage"`
	DateRange              statsDateRange        `json:"dateRange"`
	Days                   int                   `json:"days"`
	CostPerDay             float64               `json:"costPerDay"`
	TokensPerSession       float64               `json:"tokensPerSession"`
	MedianTokensPerSession float64               `json:"medianTokensPerSession"`
}

type statsTokens struct {
	Input     int        `json:"input"`
	Output    int        `json:"output"`
	Reasoning int        `json:"reasoning"`
	Cache     statsCache `json:"cache"`
}

type statsCache struct {
	Read  int `json:"read"`
	Write int `json:"write"`
}

type modelUsage struct {
	Messages int         `json:"messages"`
	Tokens   modelTokens `json:"tokens"`
	Cost     float64     `json:"cost"`
}

type modelTokens struct {
	Input  int        `json:"input"`
	Output int        `json:"output"`
	Cache  statsCache `json:"cache"`
}

type statsDateRange struct {
	Earliest int64 `json:"earliest"`
	Latest   int64 `json:"latest"`
}

func statsCommand(ctx context.Context, args []string, stdout io.Writer, stderr io.Writer) int {
	fs := flag.NewFlagSet("stats", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dbPath := fs.String("db", "", "SQLite database path; empty uses OPENCODE_DB or in-memory storage")
	days := fs.Int("days", -1, "show stats for the last N days; 0 means today")
	toolsLimit := fs.Int("tools", 0, "number of tools to show; 0 shows all")
	jsonOutput := fs.Bool("json", false, "write raw statistics JSON")
	modelsValue := fs.String("models", "", "show model statistics; optional top-N limit")
	args = normalizeStatsArgs(args)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		_, _ = fmt.Fprintln(stderr, "usage: opencode stats [--db PATH] [--json] [--days N] [--tools N] [--models[=N]]")
		return 2
	}
	sessionRepo, messageRepo, _, closeRepo, err := openRepositories(*dbPath)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "open db failed: %v\n", err)
		return 1
	}
	defer closeRepo()

	stats, err := aggregateSessionStats(ctx, sessionRepo, messageRepo, *days)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "aggregate stats failed: %v\n", err)
		return 1
	}
	if *jsonOutput {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(stats); err != nil {
			return 1
		}
		return 0
	}
	modelLimit, modelsEnabled, err := parseStatsModelLimit(*modelsValue)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "%v\n", err)
		return 2
	}
	if err := displayStats(stdout, stats, *toolsLimit, modelLimit, modelsEnabled); err != nil {
		return 1
	}
	return 0
}

func normalizeStatsArgs(args []string) []string {
	result := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg != "--models" {
			result = append(result, arg)
			continue
		}
		if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
			result = append(result, "--models="+args[i+1])
			i++
			continue
		}
		result = append(result, "--models=true")
	}
	return result
}

func aggregateSessionStats(ctx context.Context, sessionRepo session.Repository, messageRepo session.MessageRepository, days int) (sessionStats, error) {
	now := time.Now()
	cutoff := int64(0)
	windowDays := 0
	if days >= 0 {
		windowDays = max(1, days)
		if days == 0 {
			start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
			cutoff = start.UnixMilli()
		} else {
			cutoff = now.Add(-time.Duration(days) * 24 * time.Hour).UnixMilli()
		}
	}
	sessions, err := sessionRepo.List(ctx, session.ListFilter{Start: cutoff})
	if err != nil {
		return sessionStats{}, err
	}
	stats := sessionStats{
		TotalSessions: len(sessions),
		ToolUsage:     map[string]int{},
		ModelUsage:    map[string]modelUsage{},
		DateRange:     statsDateRange{Earliest: now.UnixMilli(), Latest: now.UnixMilli()},
	}
	if len(sessions) == 0 {
		stats.Days = windowDays
		return stats, nil
	}
	earliest := now.UnixMilli()
	latest := int64(0)
	sessionTokenTotals := make([]int, 0, len(sessions))
	for _, info := range sessions {
		messages, err := messageRepo.Messages(ctx, info.ID, 0)
		if err != nil {
			return sessionStats{}, err
		}
		sessionTokens := tokenUsageOrZero(info.Tokens)
		sessionTotal := totalTokenCount(sessionTokens)
		sessionTokenTotals = append(sessionTokenTotals, sessionTotal)
		stats.TotalMessages += len(messages)
		stats.TotalCost += info.Cost
		stats.TotalTokens.Input += sessionTokens.Input
		stats.TotalTokens.Output += sessionTokens.Output
		stats.TotalTokens.Reasoning += sessionTokens.Reasoning
		stats.TotalTokens.Cache.Read += sessionTokens.Cache.Read
		stats.TotalTokens.Cache.Write += sessionTokens.Cache.Write
		earliestSource := info.Time.Created
		if cutoff > 0 {
			earliestSource = info.Time.Updated
		}
		if earliestSource < earliest {
			earliest = earliestSource
		}
		if info.Time.Updated > latest {
			latest = info.Time.Updated
		}
		accumulateMessageStats(&stats, messages)
	}
	effectiveDays := windowDays
	if effectiveDays == 0 {
		effectiveDays = max(1, int(math.Ceil(float64(latest-earliest)/float64(24*time.Hour/time.Millisecond))))
	}
	stats.DateRange = statsDateRange{Earliest: earliest, Latest: latest}
	stats.Days = effectiveDays
	if effectiveDays > 0 {
		stats.CostPerDay = stats.TotalCost / float64(effectiveDays)
	}
	totalTokens := stats.TotalTokens.Input + stats.TotalTokens.Output + stats.TotalTokens.Reasoning + stats.TotalTokens.Cache.Read + stats.TotalTokens.Cache.Write
	stats.TokensPerSession = float64(totalTokens) / float64(len(sessions))
	stats.MedianTokensPerSession = medianInt(sessionTokenTotals)
	return stats, nil
}

func accumulateMessageStats(stats *sessionStats, messages []session.WithParts) {
	for _, message := range messages {
		if message.Info.Role == "assistant" {
			modelKey := message.Info.ProviderID + "/" + message.Info.ModelID
			usage := stats.ModelUsage[modelKey]
			usage.Messages++
			if message.Info.Cost != nil {
				usage.Cost += *message.Info.Cost
			}
			if message.Info.Tokens != nil {
				usage.Tokens.Input += message.Info.Tokens.Input
				usage.Tokens.Output += message.Info.Tokens.Output + message.Info.Tokens.Reasoning
				usage.Tokens.Cache.Read += message.Info.Tokens.Cache.Read
				usage.Tokens.Cache.Write += message.Info.Tokens.Cache.Write
			}
			stats.ModelUsage[modelKey] = usage
		}
		for _, part := range message.Parts {
			if part.Type != "tool" {
				continue
			}
			if tool, ok := part.Data["tool"].(string); ok && tool != "" {
				stats.ToolUsage[tool]++
			}
		}
	}
}

func tokenUsageOrZero(value *session.TokenUsage) session.TokenUsage {
	if value == nil {
		return session.TokenUsage{}
	}
	return *value
}

func totalTokenCount(tokens session.TokenUsage) int {
	if tokens.Total != nil {
		return *tokens.Total
	}
	return tokens.Input + tokens.Output + tokens.Reasoning + tokens.Cache.Read + tokens.Cache.Write
}

func medianInt(values []int) float64 {
	if len(values) == 0 {
		return 0
	}
	slices.Sort(values)
	mid := len(values) / 2
	if len(values)%2 == 0 {
		return float64(values[mid-1]+values[mid]) / 2
	}
	return float64(values[mid])
}

func parseStatsModelLimit(input string) (int, bool, error) {
	if input == "" {
		return 0, false, nil
	}
	if input == "true" {
		return 0, true, nil
	}
	limit, err := strconv.Atoi(input)
	if err != nil || limit < 0 {
		return 0, false, fmt.Errorf("--models must be a non-negative integer when a value is provided")
	}
	return limit, true, nil
}

func displayStats(stdout io.Writer, stats sessionStats, toolLimit int, modelLimit int, modelsEnabled bool) error {
	if _, err := fmt.Fprintln(stdout, "OVERVIEW"); err != nil {
		return err
	}
	for _, row := range []string{
		fmt.Sprintf("Sessions: %d", stats.TotalSessions),
		fmt.Sprintf("Messages: %d", stats.TotalMessages),
		fmt.Sprintf("Days: %d", stats.Days),
		"",
		"COST & TOKENS",
		fmt.Sprintf("Total Cost: $%.2f", stats.TotalCost),
		fmt.Sprintf("Avg Cost/Day: $%.2f", stats.CostPerDay),
		fmt.Sprintf("Avg Tokens/Session: %s", formatStatsNumber(int(math.Round(stats.TokensPerSession)))),
		fmt.Sprintf("Median Tokens/Session: %s", formatStatsNumber(int(math.Round(stats.MedianTokensPerSession)))),
		fmt.Sprintf("Input: %s", formatStatsNumber(stats.TotalTokens.Input)),
		fmt.Sprintf("Output: %s", formatStatsNumber(stats.TotalTokens.Output)),
		fmt.Sprintf("Cache Read: %s", formatStatsNumber(stats.TotalTokens.Cache.Read)),
		fmt.Sprintf("Cache Write: %s", formatStatsNumber(stats.TotalTokens.Cache.Write)),
	} {
		if _, err := fmt.Fprintln(stdout, row); err != nil {
			return err
		}
	}
	if modelsEnabled && len(stats.ModelUsage) > 0 {
		if _, err := fmt.Fprintln(stdout, "\nMODEL USAGE"); err != nil {
			return err
		}
		models := sortedModelUsage(stats.ModelUsage)
		if modelLimit > 0 && modelLimit < len(models) {
			models = models[:modelLimit]
		}
		for _, item := range models {
			if _, err := fmt.Fprintf(stdout, "%s: %d messages, %s input, %s output, $%.4f\n",
				item.Name,
				item.Usage.Messages,
				formatStatsNumber(item.Usage.Tokens.Input),
				formatStatsNumber(item.Usage.Tokens.Output),
				item.Usage.Cost,
			); err != nil {
				return err
			}
		}
	}
	if len(stats.ToolUsage) > 0 {
		if _, err := fmt.Fprintln(stdout, "\nTOOL USAGE"); err != nil {
			return err
		}
		total := 0
		for _, count := range stats.ToolUsage {
			total += count
		}
		if total == 0 {
			total = 1
		}
		tools := sortedToolUsage(stats.ToolUsage)
		if toolLimit > 0 && toolLimit < len(tools) {
			tools = tools[:toolLimit]
		}
		for _, item := range tools {
			percentage := float64(item.Count) / float64(total) * 100
			if _, err := fmt.Fprintf(stdout, "%s: %d (%.1f%%)\n", item.Name, item.Count, percentage); err != nil {
				return err
			}
		}
	}
	return nil
}

type namedModelUsage struct {
	Name  string
	Usage modelUsage
}

func sortedModelUsage(input map[string]modelUsage) []namedModelUsage {
	result := make([]namedModelUsage, 0, len(input))
	for name, usage := range input {
		result = append(result, namedModelUsage{Name: name, Usage: usage})
	}
	slices.SortFunc(result, func(left, right namedModelUsage) int {
		if left.Usage.Messages != right.Usage.Messages {
			return right.Usage.Messages - left.Usage.Messages
		}
		return strings.Compare(left.Name, right.Name)
	})
	return result
}

type namedToolUsage struct {
	Name  string
	Count int
}

func sortedToolUsage(input map[string]int) []namedToolUsage {
	result := make([]namedToolUsage, 0, len(input))
	for name, count := range input {
		result = append(result, namedToolUsage{Name: name, Count: count})
	}
	slices.SortFunc(result, func(left, right namedToolUsage) int {
		if left.Count != right.Count {
			return right.Count - left.Count
		}
		return strings.Compare(left.Name, right.Name)
	})
	return result
}

func formatStatsNumber(value int) string {
	switch {
	case value >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(value)/1_000_000)
	case value >= 1_000:
		return fmt.Sprintf("%.1fK", float64(value)/1_000)
	default:
		return strconv.Itoa(value)
	}
}
