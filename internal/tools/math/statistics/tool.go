// Package statistics provides a statistical analysis tool.
package statistics

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/KPO-Tech/seshat-runtime/internal/tools/math/engine"
	tool "github.com/KPO-Tech/seshat-runtime/internal/tools/registry"
	"github.com/KPO-Tech/seshat-runtime/internal/tools/schema"
)

const (
	ToolName = "statistics"

	ToolDescription = `Perform statistical analysis on a numeric dataset.

## Operations

| Operation  | Returns                                           |
|------------|---------------------------------------------------|
| mean       | Arithmetic mean (average)                         |
| median     | Middle value of the sorted dataset                |
| mode       | Most frequent value(s)                            |
| std_dev    | Sample standard deviation                         |
| variance   | Sample variance                                   |
| percentile | Map of P25/P50/P75/P90/P95/P99                    |
| summary    | All of the above in one call                      |
| range      | max − min                                         |

## When to use
- Describing a dataset (what's the average? spread? outliers?)
- Data quality checks (variance too high → noisy data)
- Reporting numeric summaries to the user

## When NOT to use
- Financial projections → use financial_calc tool
- Unit conversions → use unit_convert tool`
)

// New builds and returns the statistics tool.
func New() (tool.Tool, error) {
	return tool.NewBuilder(ToolName).
		WithDisplayName("Statistics").
		WithSearchHint("statistics mean median standard deviation variance regression correlation").
		WithDescription(ToolDescription).
		WithCategory("math").
		ReadOnly().
		ConcurrencySafe().
		NoPermission().
		WithInputSchema(inputSchema()).
		WithHandler(handle).
		Build()
}

func handle(_ context.Context, input tool.CallInput, _ tool.ToolUseContext) (tool.CallResult, error) {
	operation, _ := input.Parsed["operation"].(string)

	rawData, ok := input.Parsed["data"].([]any)
	if !ok {
		return tool.NewErrorResult(fmt.Errorf("data must be an array of numbers")), nil
	}
	data := make([]float64, 0, len(rawData))
	for i, v := range rawData {
		if v == nil {
			return tool.NewErrorResult(fmt.Errorf("data[%d] is not a number", i)), nil
		}
		data = append(data, floatFromAny(v))
	}

	calc := engine.NewStatisticsCalculator()

	switch operation {
	case "summary":
		summary, err := calc.Summary(data)
		if err != nil {
			return tool.NewErrorResult(err), nil
		}
		return jsonResult(summary)
	case "range":
		r, err := calc.Range(data)
		if err != nil {
			return tool.NewErrorResult(err), nil
		}
		return tool.NewTextResult(fmt.Sprintf("%.10g", r)), nil
	default:
		result, err := calc.Calculate(engine.StatisticsRequest{
			Operation: operation,
			Data:      data,
		})
		if err != nil {
			return tool.NewErrorResult(err), nil
		}
		return jsonResult(result.Result)
	}
}

func jsonResult(v any) (tool.CallResult, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return tool.NewErrorResult(fmt.Errorf("failed to marshal result: %w", err)), nil
	}
	return tool.NewTextResult(string(b)), nil
}

func inputSchema() schema.JSONSchema {
	return schema.FromMap(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"operation": map[string]any{
				"type":        "string",
				"enum":        []string{"mean", "median", "mode", "std_dev", "variance", "percentile", "summary", "range"},
				"description": "Statistical operation to perform",
			},
			"data": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "number"},
				"description": "Numeric dataset to analyze",
				"minItems":    1,
			},
		},
		"required": []string{"operation", "data"},
	})
}

func floatFromAny(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	case int64:
		return float64(n)
	case json.Number:
		f, _ := n.Float64()
		return f
	}
	return 0
}
