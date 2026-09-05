package tools

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestHandleCalculator(t *testing.T) {
	ctx := &Context{Emit: func(string, map[string]interface{}) {}}

	cases := []struct {
		expression string
		want       string
	}{
		{"551368 / 82", "551368 / 82 = 6724"},
		{"sqrt(144) * 8", "sqrt(144) * 8 = 96"},
		{"2 ** 10", "2 ** 10 = 1024"},
		{"pow(2, 10)", "pow(2, 10) = 1024"},
		{"17 % 5", "17 % 5 = 2"},
		{"trunc(4.7)", "trunc(4.7) = 4"},
		{"sign(-5)", "sign(-5) = -1"},
		{"hypot(3, 4)", "hypot(3, 4) = 5"},
		{"log2(8)", "log2(8) = 3"},
		{"log10(1000)", "log10(1000) = 3"},
		{"factorial(5)", "factorial(5) = 120"},
		{`days_between("2026-01-01", "2026-09-01")`, `days_between("2026-01-01", "2026-09-01") = 243`},
		{"max(3, 7, 1, 9, 2)", "max(3, 7, 1, 9, 2) = 9"},
		{"min(3, 7, 1, 9, 2)", "min(3, 7, 1, 9, 2) = 1"},
		{"sum(1,2,3,4,5,6,7,8,9,10,11,12)", "sum(1,2,3,4,5,6,7,8,9,10,11,12) = 78"},
		{"avg(1,2,3,4,5,6,7,8,9,10,11,12)", "avg(1,2,3,4,5,6,7,8,9,10,11,12) = 6.5"},
		{
			// note: no `let e = ...` here — e collides with the registered
			// Euler's-number constant (see TestHandleCalculator_LetCannotRedeclareReservedName).
			"let a = 10; let b = 20; let c = a + b; let d = c * 2; let ee = d - 5; let f = ee / 3; " +
				"let g = f + a; let h = g * b; let i = h - c; let j = i / d; let k = j + ee; let l = k * f; l",
			"let a = 10; let b = 20; let c = a + b; let d = c * 2; let ee = d - 5; let f = ee / 3; " +
				"let g = f + a; let h = g * b; let i = h - c; let j = i / d; let k = j + ee; let l = k * f; l = 1172.3148148148148",
		},
	}
	for _, c := range cases {
		argsBytes, err := json.Marshal(map[string]string{"expression": c.expression})
		if err != nil {
			t.Fatalf("marshaling args for %q: %v", c.expression, err)
		}
		got := handleCalculator(string(argsBytes), ctx)
		if got != c.want {
			t.Errorf("handleCalculator(%q) = %q, want %q", c.expression, got, c.want)
		}
	}
}

func TestHandleCalculator_RejectsUnknownFunction(t *testing.T) {
	ctx := &Context{Emit: func(string, map[string]interface{}) {}}
	got := handleCalculator(`{"expression":"open(\"file\")"}`, ctx)
	if got == "" || got[:6] != "error:" {
		t.Errorf("expected an error result for an unknown function, got %q", got)
	}
}

func TestHandleCalculator_RejectsVariables(t *testing.T) {
	ctx := &Context{Emit: func(string, map[string]interface{}) {}}
	got := handleCalculator(`{"expression":"x + 1"}`, ctx)
	if got == "" || got[:6] != "error:" {
		t.Errorf("expected an error result for an undefined variable, got %q", got)
	}
}

func TestHandleCalculator_EmptyExpression(t *testing.T) {
	ctx := &Context{Emit: func(string, map[string]interface{}) {}}
	got := handleCalculator(`{"expression":""}`, ctx)
	if got == "" || got[:6] != "error:" {
		t.Errorf("expected an error result for an empty expression, got %q", got)
	}
}

func TestHandleCalculator_NonFiniteResultsAreErrors(t *testing.T) {
	ctx := &Context{Emit: func(string, map[string]interface{}) {}}
	for _, expression := range []string{"1 / 0", "sqrt(-1)", "log(-1)", "log(0)", "2 ** 1024"} {
		argsBytes, err := json.Marshal(map[string]string{"expression": expression})
		if err != nil {
			t.Fatalf("marshaling args for %q: %v", expression, err)
		}
		got := handleCalculator(string(argsBytes), ctx)
		if got == "" || got[:6] != "error:" {
			t.Errorf("handleCalculator(%q) = %q, want an error for a non-finite result", expression, got)
		}
	}
}

func TestHandleCalculator_LetCannotRedeclareReservedName(t *testing.T) {
	ctx := &Context{Emit: func(string, map[string]interface{}) {}}
	// e is already bound to Euler's number in calculatorEnv — a let can't
	// shadow it. Locks in this behavior so a future allowlist addition
	// that silently changes it doesn't go unnoticed.
	got := handleCalculator(`{"expression":"let e = 5; e * 2"}`, ctx)
	if got == "" || got[:6] != "error:" {
		t.Errorf("expected an error result for redeclaring a reserved name, got %q", got)
	}
}

func TestHandleCalculator_MaxRejectsZeroArgs(t *testing.T) {
	ctx := &Context{Emit: func(string, map[string]interface{}) {}}
	got := handleCalculator(`{"expression":"max()"}`, ctx)
	if got == "" || got[:6] != "error:" {
		t.Errorf("expected an error result for max() with no arguments, got %q", got)
	}
}

func TestHandleCalculator_FactorialRejectsNonInteger(t *testing.T) {
	ctx := &Context{Emit: func(string, map[string]interface{}) {}}
	got := handleCalculator(`{"expression":"factorial(3.5)"}`, ctx)
	if got == "" || got[:6] != "error:" {
		t.Errorf("expected an error result for a non-integer factorial input, got %q", got)
	}
}

func TestHandleCalculator_DaysBetweenRejectsBadDate(t *testing.T) {
	ctx := &Context{Emit: func(string, map[string]interface{}) {}}
	got := handleCalculator(`{"expression":"days_between(\"not-a-date\", \"2026-09-01\")"}`, ctx)
	if got == "" || got[:6] != "error:" {
		t.Errorf("expected an error result for an unparseable date, got %q", got)
	}
}

func TestHandleCalculator_InvalidJSON(t *testing.T) {
	ctx := &Context{Emit: func(string, map[string]interface{}) {}}
	got := handleCalculator(`not json`, ctx)
	if got == "" || got[:6] != "error:" {
		t.Errorf("expected an error result for invalid JSON, got %q", got)
	}
}

// TestHandleCalculator_AllDocumentedFunctions covers every function the
// tool description promises the model but the original test suite never
// actually exercised (abs/floor/ceil/round/log/exp/sin/cos/tan), plus the
// two bare constants.
func TestHandleCalculator_AllDocumentedFunctions(t *testing.T) {
	ctx := &Context{Emit: func(string, map[string]interface{}) {}}
	cases := []struct{ expression, want string }{
		{"abs(-7)", "abs(-7) = 7"},
		{"abs(7)", "abs(7) = 7"},
		{"floor(4.9)", "floor(4.9) = 4"},
		{"ceil(4.1)", "ceil(4.1) = 5"},
		{"round(4.5)", "round(4.5) = 5"},
		{"round(4.4)", "round(4.4) = 4"},
		{"log(1)", "log(1) = 0"},
		{"exp(0)", "exp(0) = 1"},
		{"sin(0)", "sin(0) = 0"},
		{"cos(0)", "cos(0) = 1"},
		{"tan(0)", "tan(0) = 0"},
		{"pi", "pi = 3.141592653589793"},
		{"e", "e = 2.718281828459045"},
	}
	for _, c := range cases {
		argsBytes, _ := json.Marshal(map[string]string{"expression": c.expression})
		if got := handleCalculator(string(argsBytes), ctx); got != c.want {
			t.Errorf("handleCalculator(%q) = %q, want %q", c.expression, got, c.want)
		}
	}
}

// TestHandleCalculator_VariadicsAcceptSingleArgument covers min/max/sum/
// avg's single-argument edge — the existing test only ever exercised them
// with 5+ arguments.
func TestHandleCalculator_VariadicsAcceptSingleArgument(t *testing.T) {
	ctx := &Context{Emit: func(string, map[string]interface{}) {}}
	for _, expression := range []string{"max(5)", "min(5)", "sum(5)", "avg(5)"} {
		argsBytes, _ := json.Marshal(map[string]string{"expression": expression})
		want := expression + " = 5"
		if got := handleCalculator(string(argsBytes), ctx); got != want {
			t.Errorf("handleCalculator(%q) = %q, want %q", expression, got, want)
		}
	}
}

// TestHandleCalculator_DaysBetween covers both directions and the
// same-date zero case — the existing test only checked one direction.
func TestHandleCalculator_DaysBetween(t *testing.T) {
	ctx := &Context{Emit: func(string, map[string]interface{}) {}}
	cases := []struct{ expression, want string }{
		{`days_between("2026-01-01", "2026-01-01")`, `days_between("2026-01-01", "2026-01-01") = 0`},
		{`days_between("2026-09-01", "2026-01-01")`, `days_between("2026-09-01", "2026-01-01") = -243`},
	}
	for _, c := range cases {
		argsBytes, _ := json.Marshal(map[string]string{"expression": c.expression})
		if got := handleCalculator(string(argsBytes), ctx); got != c.want {
			t.Errorf("handleCalculator(%q) = %q, want %q", c.expression, got, c.want)
		}
	}
}

// TestHandleCalculator_ExpressionLengthCap locks in
// maxCalculatorExpressionLen's boundary: right at the cap must still
// evaluate, one character over must be rejected without ever reaching the
// evaluator (a length check is cheap; a malicious/pathological expression
// shouldn't get as far as expr.Compile just to find out).
func TestHandleCalculator_ExpressionLengthCap(t *testing.T) {
	ctx := &Context{Emit: func(string, map[string]interface{}) {}}

	// "1+1+1+...+1", grown to the longest odd length <= n reachable by
	// repeating "+1" (blindly truncating to exactly n would risk cutting
	// mid-token and producing a syntactically invalid tail like a
	// trailing "+"), then padded to exactly n by widening internal
	// whitespace around the first operator — whitespace is
	// insignificant to the grammar, and padding internally rather than
	// at either end matters here since handleCalculator's length check
	// runs on the *trimmed* expression, so trailing padding wouldn't
	// even count.
	buildExpr := func(n int) string {
		expr := "1"
		for len(expr)+2 <= n {
			expr += "+1"
		}
		if pad := n - len(expr); pad > 0 {
			expr = expr[:1] + strings.Repeat(" ", pad) + expr[1:]
		}
		return expr
	}

	atCap := buildExpr(maxCalculatorExpressionLen)
	if len(atCap) != maxCalculatorExpressionLen {
		t.Fatalf("test setup: built expression of length %d, want %d", len(atCap), maxCalculatorExpressionLen)
	}
	argsBytes, _ := json.Marshal(map[string]string{"expression": atCap})
	got := handleCalculator(string(argsBytes), ctx)
	if len(got) >= 6 && got[:6] == "error:" {
		t.Errorf("an expression exactly at the %d-char cap was rejected: %q", maxCalculatorExpressionLen, got)
	}

	overCap := atCap + "+1"
	argsBytes, _ = json.Marshal(map[string]string{"expression": overCap})
	got = handleCalculator(string(argsBytes), ctx)
	if len(got) < 6 || got[:6] != "error:" || !strings.Contains(got, "too long") {
		t.Errorf("handleCalculator(len=%d) = %q, want a \"too long\" error", len(overCap), got)
	}
}

// TestHandleCalculator_LetCannotRedeclareAnyReservedName broadens the
// existing single-name ("e") regression to cover a function name and a
// multi-arg function name too — the doc comment's claim is "any
// function/constant name already listed above," not just constants.
func TestHandleCalculator_LetCannotRedeclareAnyReservedName(t *testing.T) {
	ctx := &Context{Emit: func(string, map[string]interface{}) {}}
	for _, name := range []string{"e", "pi", "sqrt", "min", "days_between", "factorial"} {
		expression := fmt.Sprintf("let %s = 5; %s", name, name)
		argsBytes, _ := json.Marshal(map[string]string{"expression": expression})
		got := handleCalculator(string(argsBytes), ctx)
		if got == "" || got[:6] != "error:" {
			t.Errorf("handleCalculator(%q) = %q, want an error — %q is a reserved name", expression, got, name)
		}
	}
}

func TestHandleCalculator_LetCannotRedeclareSameNameTwice(t *testing.T) {
	ctx := &Context{Emit: func(string, map[string]interface{}) {}}
	got := handleCalculator(`{"expression":"let a = 1; let a = 2; a"}`, ctx)
	if got == "" || got[:6] != "error:" {
		t.Errorf("expected an error for redeclaring the same let-bound name twice, got %q", got)
	}
}

// TestHandleCalculator_ClosedSandbox is the regression coverage the
// package doc comment promises: expr-lang's own standard library (string/
// array/encoding/time-of-day builtins, available in every expression
// regardless of the custom Env unless disabled) and its separate
// predicate-builtin table (filter/map/reduce/..., which
// expr.DisableAllBuiltins() structurally cannot reach — see
// calculatorBlockedPredicate's doc comment) must both stay closed. Every
// one of these compiled and ran successfully before this test existed —
// found by actually stress-testing the evaluator against expr-lang's real
// builtin list, not by reading the (previously incorrect) doc comment
// claiming the environment "contains nothing but the handful of
// registered math functions/constants." `float(now().Unix())` in
// particular read the server's real wall clock through a tool documented
// to the model as pure arithmetic.
func TestHandleCalculator_ClosedSandbox(t *testing.T) {
	ctx := &Context{Emit: func(string, map[string]interface{}) {}}
	blocked := []string{
		// time/date info — the concrete leak found live.
		"now()", "float(now().Unix())", `date("2026-01-01")`, `duration("1h")`, `timezone("UTC")`,
		// string operations ("no string ... operations" per the tool
		// description) — each wrapped in something that would otherwise
		// coerce to a valid float64 result if the inner call succeeded.
		`len("hello")`, `upper("hi")`, `len(upper("hello"))`, `string(5)`, `len(string(12345))`,
		`int("5")`, `float("5.5")`, `toBase64("hi")`, `fromBase64("aGk=")`, `type(5)`,
		`get({"a":1}, "a")`, `len(fromJSON("[1,2,3]"))`,
		// predicate-family builtins — the separate gap
		// DisableAllBuiltins() alone doesn't close.
		"reduce(1..5, #acc + #, 0)", "filter(1..10, # > 5)", "all(1..5, # > 0)",
		"any(1..5, # > 4)", "none(1..5, # > 10)", "one(1..5, # == 3)", "map(1..5, # * 2)",
		"count(1..5, # > 2)", "find(1..5, # > 2)", "findIndex(1..5, # > 2)",
		"findLast(1..5, # > 2)", "findLastIndex(1..5, # > 2)", "groupBy(1..5, # % 2)", "sortBy(1..5, #)",
	}
	for _, expression := range blocked {
		argsBytes, _ := json.Marshal(map[string]string{"expression": expression})
		got := handleCalculator(string(argsBytes), ctx)
		if got == "" || got[:6] != "error:" {
			t.Errorf("handleCalculator(%q) = %q, want an error — this must stay outside calculator's sandbox", expression, got)
		}
	}
}

// TestHandleCalculator_SumStillOverridesPredicateSum confirms "sum"
// itself — one of expr-lang's predicate names too, per
// calculatorBlockedPredicate's doc comment — keeps resolving to our own
// plain variadic calculatorSum rather than accidentally falling through
// to expr-lang's predicate-style `sum(array, predicate)` form once other
// predicate names started getting explicit blocking entries.
func TestHandleCalculator_SumStillOverridesPredicateSum(t *testing.T) {
	ctx := &Context{Emit: func(string, map[string]interface{}) {}}
	got := handleCalculator(`{"expression":"sum(1,2,3)"}`, ctx)
	want := "sum(1,2,3) = 6"
	if got != want {
		t.Errorf("handleCalculator(%q) = %q, want %q", "sum(1,2,3)", got, want)
	}
}
