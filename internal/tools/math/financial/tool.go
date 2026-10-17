// Package financial provides a financial calculation tool.
package financial

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/KPO-Tech/seshat-runtime/internal/tools/math/engine"
	tool "github.com/KPO-Tech/seshat-runtime/internal/tools/registry"
	"github.com/KPO-Tech/seshat-runtime/internal/tools/schema"
)

const (
	ToolName = "financial_calc"

	ToolDescription = `Perform financial calculations: interest, loan payments, ROI, and present/future value.

## Operations

| Operation         | Required inputs                        | Returns                   |
|-------------------|----------------------------------------|---------------------------|
| compound_interest | principal, rate, time [periods]        | final amount + breakdown  |
| simple_interest   | principal, rate, time                  | interest earned           |
| loan_payment      | principal, rate, time [periods]        | periodic payment amount   |
| roi               | principal (initial), future_value      | ROI % + annualized if time given |
| present_value     | future_value, rate, time [periods]     | PV + discount factor      |
| future_value      | principal, rate, time [periods]        | FV same as compound       |
| npv               | cash_flows (array), rate               | Net Present Value         |
| irr               | cash_flows (array, first is negative)  | Internal Rate of Return%  |

## Parameter conventions
- rate: annual percentage (e.g. 5 for 5%)
- time: years (e.g. 10 for 10 years)
- periods: compounding/payment frequency per year (default: 1 for compound/PV/FV, 12 for loan)
- principal: initial investment or loan amount
- future_value: target amount (for PV, ROI)

## When to use
- Answering "how much will I have after X years at Y% interest?"
- Calculating monthly mortgage or loan payments
- Evaluating investment returns`
)

// New builds and returns the financial_calc tool.
func New() (tool.Tool, error) {
	return tool.NewBuilder(ToolName).
		WithDisplayName("Financial Calculator").
		WithSearchHint("financial math loan mortgage interest npv irr amortization").
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
	calc := engine.NewFinancialCalculator()

	switch operation {
	case "npv":
		cashFlows, err := parseCashFlows(input.Parsed)
		if err != nil {
			return tool.NewErrorResult(err), nil
		}
		rate := floatFromAny(input.Parsed["rate"])
		npv, err := calc.NetPresentValue(cashFlows, rate)
		if err != nil {
			return tool.NewErrorResult(err), nil
		}
		return tool.NewTextResult(fmt.Sprintf("%.4f", npv)), nil

	case "irr":
		cashFlows, err := parseCashFlows(input.Parsed)
		if err != nil {
			return tool.NewErrorResult(err), nil
		}
		irr, err := calc.InternalRateOfReturn(cashFlows)
		if err != nil {
			return tool.NewErrorResult(err), nil
		}
		return tool.NewTextResult(fmt.Sprintf("%.4f%%", irr)), nil

	default:
		req := engine.FinancialRequest{
			Operation:   operation,
			Principal:   floatFromAny(input.Parsed["principal"]),
			Rate:        floatFromAny(input.Parsed["rate"]),
			Time:        floatFromAny(input.Parsed["time"]),
			FutureValue: floatFromAny(input.Parsed["future_value"]),
			Periods:     int(floatFromAny(input.Parsed["periods"])),
		}
		result, err := calc.Calculate(req)
		if err != nil {
			return tool.NewErrorResult(err), nil
		}
		out := map[string]any{
			"result":      result.Result,
			"description": result.Description,
		}
		if len(result.Breakdown) > 0 {
			out["breakdown"] = result.Breakdown
		}
		return jsonResult(out)
	}
}

func parseCashFlows(params map[string]any) ([]float64, error) {
	raw, ok := params["cash_flows"].([]any)
	if !ok {
		return nil, fmt.Errorf("cash_flows must be an array of numbers")
	}
	flows := make([]float64, 0, len(raw))
	for i, v := range raw {
		if v == nil {
			return nil, fmt.Errorf("cash_flows[%d] is not a number", i)
		}
		flows = append(flows, floatFromAny(v))
	}
	return flows, nil
}

func jsonResult(v any) (tool.CallResult, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return tool.NewErrorResult(fmt.Errorf("failed to marshal result: %w", err)), nil
	}
	return tool.NewTextResult(string(b)), nil
}

func inputSchema() schema.JSONSchema {
	operations := []string{
		"compound_interest", "simple_interest", "loan_payment",
		"roi", "present_value", "future_value", "npv", "irr",
	}
	return schema.FromMap(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"operation": map[string]any{
				"type":        "string",
				"enum":        operations,
				"description": "Financial operation to perform",
			},
			"principal": map[string]any{
				"type":        "number",
				"description": "Initial amount / loan principal",
			},
			"rate": map[string]any{
				"type":        "number",
				"description": "Annual interest / discount rate as a percentage (e.g. 5 for 5%)",
			},
			"time": map[string]any{
				"type":        "number",
				"description": "Duration in years",
			},
			"periods": map[string]any{
				"type":        "integer",
				"minimum":     1,
				"description": "Compounding or payment frequency per year (default 1 for interest, 12 for loans)",
			},
			"future_value": map[string]any{
				"type":        "number",
				"description": "Target future amount (for roi, present_value)",
			},
			"cash_flows": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "number"},
				"description": "Cash flow series for npv/irr; first entry is typically negative (initial outflow)",
				"minItems":    2,
			},
		},
		"required": []string{"operation"},
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
