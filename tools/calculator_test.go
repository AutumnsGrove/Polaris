package tools

import (
	"encoding/json"
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
