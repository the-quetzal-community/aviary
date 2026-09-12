package internal

import (
	"testing"

	"graphics.gd/variant/Float"
	"graphics.gd/variant/Vector2"
)

// TestPadStickShape pins the stick conditioning: drift inside the
// deadzone reads as nothing, motion starts from zero just outside it
// (not at a fifth of full speed), full deflection is full speed, and a
// stick that reads past 1 in the corners is clamped so a pad can never
// move faster than the keyboard.
func TestPadStickShape(t *testing.T) {
	near := func(got, want Float.X) bool { return got-want < 1e-4 && want-got < 1e-4 }

	if v := padStickShape(Vector2.XY{X: 0.1, Y: -0.1}); v != (Vector2.XY{}) {
		t.Fatalf("drift inside the deadzone should read zero, got %v", v)
	}
	// Just outside the deadzone the output is near zero, not a jump.
	if v := padStickShape(Vector2.XY{X: padStickDeadzone + 0.001}); !near(Vector2.Length(v), 0.00125) {
		t.Fatalf("just outside the deadzone should creep, got length %v", Vector2.Length(v))
	}
	if v := padStickShape(Vector2.XY{X: 1}); !near(v.X, 1) || !near(v.Y, 0) {
		t.Fatalf("full deflection should be full speed, got %v", v)
	}
	// A diagonal at full deflection on both axes reads past 1; clamped.
	if v := padStickShape(Vector2.XY{X: 1, Y: 1}); !near(Vector2.Length(v), 1) {
		t.Fatalf("corner deflection should clamp to 1, got length %v", Vector2.Length(v))
	}
	// Halfway out: the deadzone is removed, not merely ignored.
	if v := padStickShape(Vector2.XY{X: 0.6}); !near(v.X, (0.6-padStickDeadzone)/(1-padStickDeadzone)) {
		t.Fatalf("deadzone should be subtracted, got %v", v)
	}
}
