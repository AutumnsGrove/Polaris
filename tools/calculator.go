// calculator gives the model an exact arithmetic evaluator to fall back
// on instead of computing derived numbers (ratios, date deltas, percent
// math) silently in free-text generation, where a wrong digit is
// unverifiable. Motivated by a real LiveNewsBench miss (see issue #23):
// the agent found correct source figures but then miscalculated a
// division in its final answer.
//
// Single free-form `expression` string, not decomposed into operand/op
// params — see the design discussion in thread "Go Agent Calculator
// Libraries" (2026-08-31). A restricted evaluator (expr-lang/expr) with a
// minimal function allowlist is the entire safety model: there's no
// validation step separate from execution that a bug could skip, because
// the evaluator's environment contains nothing but the handful of
// registered math functions/constants below. An expression referencing
// anything else (a variable, a builtin like `open`) fails to *compile* —
// it never reaches evaluation at all. Confirmed locally against both
// expr-lang/expr and govaluate before picking expr-lang: both reject
// unknown identifiers at parse time, but expr-lang is the actively
// maintained one and its two-phase Compile/Run split maps directly onto
// "validate before running."
package tools

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/expr-lang/expr"

	"polaris/llm"
)

var calculatorDef = llm.ToolDef{
	Type: "function",
	Function: llm.ToolFunctionDef{
		Name: "calculator",
		// Description is populated at call time by Defs()/AllDefs() from
		// tools/descriptions/calculator.yaml — see tools/catalog.go.
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"expression": map[string]interface{}{
					"type": "string",
					"description": "An arithmetic expression to evaluate exactly, e.g. \"551368 / 82\", " +
						"\"sqrt(144) * 8\", or \"days_between(\\\"2026-01-01\\\", \\\"2026-09-01\\\")\". Supports " +
						"+ - * / ** (power) % (remainder) and the functions sqrt, pow, abs, floor, ceil, round, " +
						"trunc, sign, log, log2, log10, exp, sin, cos, tan, hypot, factorial, plus the variadic " +
						"min(...), max(...), sum(...), avg(...) (any number of arguments, not just two), and " +
						"days_between(date1, date2) [YYYY-MM-DD, returns date2 minus date1 in days], plus the " +
						"constants pi and e. For a multi-step calculation with several named intermediate " +
						"values, chain them with let: \"let principal = 10000; let rate = 0.05; let years = 10; " +
						"principal * (1 + rate) ** years\" — each let introduces one name, separated by ; , and " +
						"the final expression after the last ; is what's returned. A let name can't reuse a " +
						"function/constant name already listed above (e.g. \"let e = ...\" or \"let sum = ...\" " +
						"fails to compile) — pick a different name. No free variables outside a let you declared " +
						"yourself in the same call, no string/file/code operations beyond the two date args to " +
						"days_between.",
				},
			},
			"required": []string{"expression"},
		},
	},
}

func init() { Register("calculator", handleCalculator) }

// maxCalculatorExpressionLen mirrors the 200-500 char cap discussed in
// the design thread — long enough for any real arithmetic expression,
// short enough to keep the parser's work bounded.
const maxCalculatorExpressionLen = 500

// calculatorEnv is the entire allowlist: expr-lang's compiler resolves
// identifiers only against this map, so anything not listed here (a
// variable, a builtin function, a method call) fails to compile rather
// than silently doing something else. Keep this list math-only —
// see the package doc comment on why that boundary matters.
var calculatorEnv = map[string]interface{}{
	"sqrt":  math.Sqrt,
	"pow":   math.Pow,
	"abs":   math.Abs,
	"floor": math.Floor,
	"ceil":  math.Ceil,
	"round": math.Round,
	"trunc": math.Trunc,
	"sign":  calculatorSign,
	"log":   math.Log,
	"log2":  math.Log2,
	"log10": math.Log10,
	"exp":   math.Exp,
	"sin":   math.Sin,
	"cos":   math.Cos,
	"tan":   math.Tan,
	"hypot": math.Hypot,
	"pi":    math.Pi,
	"e":     math.E,

	// min/max/sum/avg are variadic, not fixed at two arguments — an
	// earlier version wired these straight to math.Min/math.Max, which
	// only take two floats each; expr.Compile happily accepted
	// max(a,b,c) syntactically but then rejected it at runtime with "too
	// many arguments to call max", a footgun found only by actually
	// stress-testing multi-argument calls, not by reading the stdlib
	// signature. Real inputs ("the largest of these dozen readings")
	// need N-ary versions.
	"min": calculatorMin,
	"max": calculatorMax,
	"sum": calculatorSum,
	"avg": calculatorAvg,

	"factorial": calculatorFactorial,

	// days_between parses two YYYY-MM-DD dates and returns the signed
	// difference in days (date2 - date1) — the "date-interval math" case
	// issue #23 calls out alongside plain ratios. The only place a string
	// literal is meaningful in this environment; everything else here is
	// pure numeric math, so this doesn't widen the allowlist's actual
	// capability beyond "arithmetic, including on dates."
	"days_between": calculatorDaysBetween,
}

func calculatorMin(nums ...float64) (float64, error) {
	if len(nums) == 0 {
		return 0, fmt.Errorf("min requires at least one argument")
	}
	m := nums[0]
	for _, n := range nums[1:] {
		if n < m {
			m = n
		}
	}
	return m, nil
}

func calculatorMax(nums ...float64) (float64, error) {
	if len(nums) == 0 {
		return 0, fmt.Errorf("max requires at least one argument")
	}
	m := nums[0]
	for _, n := range nums[1:] {
		if n > m {
			m = n
		}
	}
	return m, nil
}

func calculatorSum(nums ...float64) float64 {
	total := 0.0
	for _, n := range nums {
		total += n
	}
	return total
}

func calculatorAvg(nums ...float64) (float64, error) {
	if len(nums) == 0 {
		return 0, fmt.Errorf("avg requires at least one argument")
	}
	return calculatorSum(nums...) / float64(len(nums)), nil
}

func calculatorSign(x float64) float64 {
	switch {
	case x > 0:
		return 1
	case x < 0:
		return -1
	default:
		return 0
	}
}

// calculatorFactorial guards against inputs that would overflow float64
// (170! is the largest that doesn't reach +Inf) or aren't a non-negative
// integer to begin with, rather than silently returning +Inf or rounding
// a fractional input.
func calculatorFactorial(n float64) (float64, error) {
	if n < 0 || n != math.Trunc(n) {
		return 0, fmt.Errorf("factorial requires a non-negative integer, got %v", n)
	}
	if n > 170 {
		return 0, fmt.Errorf("factorial(%v) overflows — max supported input is 170", n)
	}
	result := 1.0
	for i := 2.0; i <= n; i++ {
		result *= i
	}
	return result, nil
}

func calculatorDaysBetween(date1, date2 string) (float64, error) {
	t1, err := time.Parse("2006-01-02", date1)
	if err != nil {
		return 0, fmt.Errorf("parsing date %q (want YYYY-MM-DD): %w", date1, err)
	}
	t2, err := time.Parse("2006-01-02", date2)
	if err != nil {
		return 0, fmt.Errorf("parsing date %q (want YYYY-MM-DD): %w", date2, err)
	}
	return t2.Sub(t1).Hours() / 24, nil
}

func handleCalculator(argsJSON string, ctx *Context) string {
	var args struct {
		Expression string `json:"expression"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return emitToolError(ctx, "calculator", nil, "error: "+err.Error())
	}
	expression := strings.TrimSpace(args.Expression)
	if expression == "" {
		return emitToolError(ctx, "calculator", map[string]interface{}{"expression": args.Expression}, "error: expression is required")
	}
	if len(expression) > maxCalculatorExpressionLen {
		return emitToolError(ctx, "calculator", map[string]interface{}{"expression": expression},
			fmt.Sprintf("error: expression too long (%d chars, max %d)", len(expression), maxCalculatorExpressionLen))
	}

	ctx.Emit("tool_call", map[string]interface{}{
		"tool": "calculator",
		"args": map[string]interface{}{"expression": expression},
	})

	result, err := evalCalculatorExpression(expression)
	if err != nil {
		result = "error: " + err.Error()
		log.Warn("calculator failed", "expression", expression, "err", err)
		ctx.Emit("tool_result", map[string]interface{}{"tool": "calculator", "result": result})
		return result
	}

	log.Info("calculator", "expression", expression, "result", result)
	ctx.Emit("tool_result", map[string]interface{}{"tool": "calculator", "result": result})
	return result
}

func evalCalculatorExpression(expression string) (string, error) {
	program, err := expr.Compile(expression, expr.Env(calculatorEnv), expr.AsFloat64())
	if err != nil {
		return "", fmt.Errorf("invalid expression: %w", err)
	}

	out, err := expr.Run(program, calculatorEnv)
	if err != nil {
		return "", fmt.Errorf("evaluating expression: %w", err)
	}
	value, ok := out.(float64)
	if !ok {
		return "", fmt.Errorf("expression did not evaluate to a number (got %T)", out)
	}
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return "", fmt.Errorf("expression produced a non-finite result")
	}

	return fmt.Sprintf("%s = %s", expression, strconv.FormatFloat(value, 'g', -1, 64)), nil
}
