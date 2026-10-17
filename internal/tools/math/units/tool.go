// Package units provides a unit conversion tool.
package units

import (
	"context"
	"fmt"
	"strings"

	"github.com/KPO-Tech/seshat-runtime/internal/tools/math/engine"
	tool "github.com/KPO-Tech/seshat-runtime/internal/tools/registry"
	"github.com/KPO-Tech/seshat-runtime/internal/tools/schema"
)

const (
	ToolName = "unit_convert"

	ToolDescription = `Convert values between units of measurement.

## Supported categories

| Category    | Example units                                       |
|-------------|-----------------------------------------------------|
| length      | nm, μm, mm, cm, m, km, in, ft, yd, mi              |
| weight      | mg, g, kg, t (metric ton), oz, lb, st, ton (short) |
| volume      | ml, cl, dl, l, kl, fl_oz, cup, pt, qt, gal, tsp, tbsp, bbl |
| area        | mm2, cm2, m2, km2, in2, ft2, yd2, mi2, acre, ha    |
| temperature | C, F, K, R                                          |

## Examples
- 100 km → miles: category=length, from_unit=km, to_unit=mi, value=100
- 37 C → F: category=temperature, from_unit=C, to_unit=F, value=37
- 5 lbs → kg: category=weight, from_unit=lb, to_unit=kg, value=5

## Batch conversion
Supply a list in the values field to convert multiple numbers at once.`
)

// New builds and returns the unit_convert tool.
func New() (tool.Tool, error) {
	return tool.NewBuilder(ToolName).
		WithDisplayName("Unit Converter").
		WithSearchHint("convert between units of measurement length weight temperature volume").
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
	category, _ := input.Parsed["category"].(string)
	fromUnit, _ := input.Parsed["from_unit"].(string)
	toUnit, _ := input.Parsed["to_unit"].(string)

	converter := engine.NewUnitConverter()

	// Batch path
	if rawVals, ok := input.Parsed["values"].([]any); ok && len(rawVals) > 0 {
		vals := make([]float64, 0, len(rawVals))
		for _, v := range rawVals {
			vals = append(vals, floatFromAny(v))
		}
		results, err := converter.ConvertMultiple(vals, fromUnit, toUnit, category)
		if err != nil {
			return tool.NewErrorResult(err), nil
		}
		parts := make([]string, len(results))
		for i, r := range results {
			parts[i] = fmt.Sprintf("%.10g %s", r, toUnit)
		}
		return tool.NewTextResult(strings.Join(parts, "\n")), nil
	}

	// Single path
	value := floatFromAny(input.Parsed["value"])
	result, err := converter.Convert(engine.UnitConversionRequest{
		Value:    value,
		FromUnit: fromUnit,
		ToUnit:   toUnit,
		Category: category,
	})
	if err != nil {
		return tool.NewErrorResult(err), nil
	}
	return tool.NewTextResult(fmt.Sprintf("%.10g %s", result.Result, toUnit)), nil
}

func inputSchema() schema.JSONSchema {
	categories := []string{"length", "weight", "volume", "area", "temperature"}
	return schema.FromMap(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"category": map[string]any{
				"type":        "string",
				"enum":        categories,
				"description": "Measurement category",
			},
			"from_unit": map[string]any{
				"type":        "string",
				"description": "Source unit (e.g. km, lb, C)",
			},
			"to_unit": map[string]any{
				"type":        "string",
				"description": "Target unit (e.g. mi, kg, F)",
			},
			"value": map[string]any{
				"type":        "number",
				"description": "Single value to convert",
			},
			"values": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "number"},
				"description": "Multiple values to convert (batch mode)",
			},
		},
		"required": []string{"category", "from_unit", "to_unit"},
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
	}
	return 0
}
