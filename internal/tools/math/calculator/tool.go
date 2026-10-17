// Package calculator provides a calculator tool for basic arithmetic, advanced
// mathematical functions, and expression evaluation.
package calculator

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/KPO-Tech/seshat-runtime/internal/tools/math/engine"
	tool "github.com/KPO-Tech/seshat-runtime/internal/tools/registry"
	"github.com/KPO-Tech/seshat-runtime/internal/tools/schema"
)

const (
	ToolName = "calculator"

	ToolDescription = `Perform mathematical calculations: basic arithmetic, advanced functions, and expression evaluation.

## Modes

**basic** — arithmetic on a list of numbers
- operations: add, subtract, multiply, divide
- operands: array of ≥2 numbers
- precision: optional decimal places (default 2)

**advanced** — single-value mathematical functions
- functions: sin, cos, tan, asin, acos, atan, log, log10, ln, sqrt, abs, factorial, pow, exp
- unit: "radians" (default) or "degrees" for trig functions
- exponent: required for pow

**expression** — evaluate a mathematical expression string
- expression: e.g. "2*x + sin(y)", "pi * r^2", "(a + b) / c"
- variables: optional map of variable values {"x": 3, "r": 5}
- built-in constants: pi, e

## When to use
- Any computation the agent should not attempt in natural language
- Multi-step formulas where float precision matters
- Expressions with variables that the user provides

## When NOT to use
- Statistical analysis on datasets → use statistics tool
- Financial calculations → use financial_calc tool
- Unit conversions → use unit_convert tool`
)

// New builds and returns the calculator tool.
func New() (tool.Tool, error) {
	return tool.NewBuilder(ToolName).
		WithDisplayName("Calculator").
		WithSearchHint("evaluate a math expression arithmetic algebra calculus").
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
	mode, _ := input.Parsed["mode"].(string)
	switch mode {
	case "basic":
		return handleBasic(input.Parsed)
	case "advanced":
		return handleAdvanced(input.Parsed)
	case "expression":
		return handleExpression(input.Parsed)
	default:
		return tool.NewErrorResult(fmt.Errorf("unknown mode %q — must be basic, advanced, or expression", mode)), nil
	}
}

func handleBasic(params map[string]any) (tool.CallResult, error) {
	operation, _ := params["operation"].(string)
	precision := int(floatFromAny(params["precision"]))

	rawOperands, ok := params["operands"].([]any)
	if !ok {
		return tool.NewErrorResult(fmt.Errorf("operands must be an array of numbers")), nil
	}
	operands := make([]float64, 0, len(rawOperands))
	for i, v := range rawOperands {
		if v == nil {
			return tool.NewErrorResult(fmt.Errorf("operand[%d] is not a number", i)), nil
		}
		operands = append(operands, floatFromAny(v))
	}

	calc := engine.NewBasicCalculator()
	result, err := calc.Calculate(engine.BasicMathRequest{
		Operation: operation,
		Operands:  operands,
		Precision: precision,
	})
	if err != nil {
		return tool.NewErrorResult(err), nil
	}
	return tool.NewTextResult(fmt.Sprintf("%.10g", result.Result)), nil
}

func handleAdvanced(params map[string]any) (tool.CallResult, error) {
	function, _ := params["function"].(string)
	value := floatFromAny(params["value"])
	exponent := floatFromAny(params["exponent"])
	unit, _ := params["unit"].(string)

	calc := engine.NewAdvancedCalculator()
	result, err := calc.Calculate(engine.AdvancedMathRequest{
		Function: function,
		Value:    value,
		Exponent: exponent,
		Unit:     unit,
	})
	if err != nil {
		return tool.NewErrorResult(err), nil
	}
	return tool.NewTextResult(fmt.Sprintf("%.10g", result.Result)), nil
}

func handleExpression(params map[string]any) (tool.CallResult, error) {
	expression, _ := params["expression"].(string)

	vars := make(map[string]float64)
	if rawVars, ok := params["variables"].(map[string]any); ok {
		for k, v := range rawVars {
			vars[k] = floatFromAny(v)
		}
	}

	calc := engine.NewExpressionCalculator()
	result, err := calc.Evaluate(engine.ExpressionRequest{
		Expression: expression,
		Variables:  vars,
	})
	if err != nil {
		return tool.NewErrorResult(err), nil
	}
	return tool.NewTextResult(fmt.Sprintf("%.10g", result.Result)), nil
}

func inputSchema() schema.JSONSchema {
	return schema.FromMap(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"mode": map[string]any{
				"type":        "string",
				"enum":        []string{"basic", "advanced", "expression"},
				"description": "Calculation mode: basic (arithmetic), advanced (functions), expression (formula string)",
			},
			"operation": map[string]any{
				"type":        "string",
				"enum":        []string{"add", "subtract", "multiply", "divide"},
				"description": "[basic] Arithmetic operation to perform",
			},
			"operands": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "number"},
				"description": "[basic] Array of numbers to operate on (≥2)",
			},
			"precision": map[string]any{
				"type":        "integer",
				"minimum":     0,
				"maximum":     15,
				"description": "[basic] Decimal places in result (default 2)",
			},
			"function": map[string]any{
				"type": "string",
				"enum": []string{
					"sin", "cos", "tan", "asin", "acos", "atan",
					"log", "log10", "ln", "sqrt", "abs", "factorial", "pow", "exp",
				},
				"description": "[advanced] Mathematical function to apply",
			},
			"value": map[string]any{
				"type":        "number",
				"description": "[advanced] Input value (base for pow)",
			},
			"exponent": map[string]any{
				"type":        "number",
				"description": "[advanced] Exponent for pow function",
			},
			"unit": map[string]any{
				"type":        "string",
				"enum":        []string{"radians", "degrees"},
				"description": "[advanced] Angle unit for trig functions (default radians)",
			},
			"expression": map[string]any{
				"type":        "string",
				"description": "[expression] Mathematical expression string, e.g. \"2*x + sin(y)\"",
			},
			"variables": map[string]any{
				"type":                 "object",
				"additionalProperties": map[string]any{"type": "number"},
				"description":          "[expression] Variable values to substitute, e.g. {\"x\": 3, \"y\": 1.5}",
			},
		},
		"required": []string{"mode"},
	})
}

// floatFromAny coerces any JSON-decoded numeric type to float64.
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
