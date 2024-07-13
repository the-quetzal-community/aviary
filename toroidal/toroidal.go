package toroidal

import (
	"math"
)

type Space struct {
	Seed int64

	Size float64

	Octaves     int64
	Scale       float64
	Persistence float64
	Lacunarity  float64
}

// LineBetween returns the line between the given points.
func (space Space) LineBetween(x1, y1, x2, y2 int64) (x, y, tx, ty int64) {
	abs := func(x int64) int64 {
		if x < 0 {
			return -x
		}
		return x
	}
	var dx = abs(x2 - x1)
	var dy = abs(y2 - y1)
	var size = int64(space.Size)
	var vx, vy int64 = 0, 0
	if x2-x1 > 0 {
		vx = 1
	} else {
		vx = -1
	}
	if y2-y1 > 0 {
		vy = 1
	} else {
		vy = -1
	}
	if dx > size/2 {
		dx = size - dx
		vx = -vx
	}
	if dy > size/2 {
		dy = size - dy
		vy = -vy
	}
	return x1, y1, x1 + dx*vx, y1 + dy*vy
}

// LineBetweenF32 returns the line between the given points.
func (space Space) LineBetweenF32(x1, y1, x2, y2 float64) (x, y, tx, ty float64) {
	var (
		dx = math.Abs(x2 - x1)
		dy = math.Abs(y2 - y1)
	)
	var vx, vy float64 = 0, 0
	if x2-x1 > 0 {
		vx = 1
	} else {
		vx = -1
	}
	if y2-y1 > 0 {
		vy = 1
	} else {
		vy = -1
	}
	if dx > space.Size/2 {
		dx = space.Size - dx
		vx = -vx
	}
	if dy > space.Size/2 {
		dy = space.Size - dy
		vy = -vy
	}
	return x1, y1, x1 + dx*vx, y1 + dy*vy
}

// AngleBetween returns the angle between the two points.
func (space Space) AngleBetween(a, b, c, d float64) float64 {
	a, b, c, d = space.LineBetweenF32(a, b, c, d)
	b = -b
	d = -d
	return math.Atan2(d-b, c-a) + math.Pi/2
}

// Distance returns the distance between the two given points.
func (space Space) Distance(x1, y1, x2, y2 float64) float64 {
	var (
		dx = math.Abs(x2 - x1)
		dy = math.Abs(y2 - y1)
	)
	if dx > space.Size/2 {
		dx = space.Size - dx
	}
	if dy > space.Size/2 {
		dy = space.Size - dy
	}
	return math.Sqrt(dx*dx + dy*dy)
}

// Lerp returns a linear interpolation between the first and second point based on the given 'dt' delta between 0 an 1.
func (space Space) Lerp(x, y, tx, ty, dt float64) (float64, float64) {
	var (
		dx = math.Abs(tx - x)
		dy = math.Abs(ty - y)
	)
	var vx, vy float64 = 0, 0
	if tx-x > 0 {
		vx = 1
	} else {
		vx = -1
	}
	if ty-y > 0 {
		vy = 1
	} else {
		vy = -1
	}
	if dx > space.Size/2 {
		dx = space.Size - dx
		vx = -vx
	}
	if dy > space.Size/2 {
		dy = space.Size - dy
		vy = -vy
	}
	fx := x*(1-dt) + (x+dx*vx)*dt
	fy := y*(1-dt) + (y+dy*vy)*dt
	if fx < 0 {
		fx += space.Size
	} else if fx > space.Size {
		fx -= space.Size
	}
	if fy < 0 {
		fy += space.Size
	} else if fy > space.Size {
		fy -= space.Size
	}
	return fx, fy
}

func (space Space) LinesIntersect(x1, y1, x2, y2, x3, y3, x4, y4 int64) (x, y int64, ok bool) {
	var (
		lineA = [2][2]int64{{x1, y1}, {x2, y2}}
		lineB = [2][2]int64{{x3, y3}, {x4, y4}}
	)
	var lineA1, lineA2 [2][2]int64
	lineA1[0][0], lineA1[0][1], lineA1[1][0], lineA1[1][1] = space.LineBetween(lineA[0][0], lineA[0][1], lineA[1][0], lineA[1][1])
	lineA2[0][0], lineA2[0][1], lineA2[1][0], lineA2[1][1] = space.LineBetween(lineA[1][0], lineA[1][1], lineA[0][0], lineA[0][1])
	var lineB1, lineB2 [2][2]int64
	lineB1[0][0], lineB1[0][1], lineB1[1][0], lineB1[1][1] = space.LineBetween(lineB[0][0], lineB[0][1], lineB[1][0], lineB[1][1])
	lineB2[0][0], lineB2[0][1], lineB2[1][0], lineB2[1][1] = space.LineBetween(lineB[1][0], lineB[1][1], lineB[0][0], lineB[0][1])
	Intersect := func(a, b [2][2]int64) (int64, int64, bool) {
		return linesIntersect(a[0][0], a[0][1], a[1][0], a[1][1], b[0][0], b[0][1], b[1][0], b[1][1])
	}
	if x, y, ok := Intersect(lineA1, lineB1); ok {
		return x, y, ok
	}
	if x, y, ok := Intersect(lineA1, lineB2); ok {
		return x, y, ok
	}
	if x, y, ok := Intersect(lineA2, lineB1); ok {
		return x, y, ok
	}
	if x, y, ok := Intersect(lineA2, lineB2); ok {
		return x, y, ok
	}
	return 0, 0, false
}

// linesIntersect computes a the line intersection betweeb the two provided line segments.
func linesIntersect(x1, y1, x2, y2, x3, y3, x4, y4 int64) (x, y int64, ok bool) {
	sameSigns := func(a, b int64) bool {
		return (a < 0 && b < 0) || (a > 0 && b > 0)
	}

	var a1, a2, b1, b2, c1, c2 int64 /* Coefficients of line eqns. */
	var r1, r2, r3, r4 int64         /* 'Sign' values */
	var denom, offset, num int64     /* Intermediate values */

	/* Compute a1, b1, c1, where line joining points 1 and 2
	 * is "a1 x  +  b1 y  +  c1  =  0".
	 */

	a1 = y2 - y1
	b1 = x1 - x2
	c1 = x2*y1 - x1*y2

	/* Compute r3 and r4.
	 */

	r3 = a1*x3 + b1*y3 + c1
	r4 = a1*x4 + b1*y4 + c1

	/* Check signs of r3 and r4.  If both point 3 and point 4 lie on
	 * same side of line 1, the line segments do not intersect.
	 */

	if r3 != 0 &&
		r4 != 0 &&
		sameSigns(r3, r4) {
		return 0, 0, false
	}

	/* Compute a2, b2, c2 */

	a2 = y4 - y3
	b2 = x3 - x4
	c2 = x4*y3 - x3*y4

	/* Compute r1 and r2 */

	r1 = a2*x1 + b2*y1 + c2
	r2 = a2*x2 + b2*y2 + c2

	/* Check signs of r1 and r2.  If both point 1 and point 2 lie
	 * on same side of second line segment, the line segments do
	 * not intersect.
	 */

	if r1 != 0 &&
		r2 != 0 &&
		sameSigns(r1, r2) {
		return 0, 0, false
	}

	/* Line segments intersect: compute intersection point.
	 */

	denom = a1*b2 - a2*b1
	if denom == 0 {
		return 0, 0, false
	}
	if denom < 0 {
		offset = -denom / 2
	} else {
		offset = denom / 2
	}

	/* The denom/2 is to get rounding instead of truncating.  It
	 * is added or subtracted to the numerator, depending upon the
	 * sign of the numerator.
	 */

	num = b1*c2 - b2*c1

	if num < 0 {
		x = num - offset
	} else {
		x = num + offset
	}

	num = a2*c1 - a1*c2

	if num < 0 {
		y = num - offset
	} else {
		y = num + offset
	}

	return x / denom, y / denom, true
}
