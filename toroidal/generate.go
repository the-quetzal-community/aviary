package toroidal

import (
	"grow.graphics/xy/vector2"
)

func (space Space) NoiseAt(x, y float64) (height, slopex, slopey float64) {
	/*clamp := func(x, lowerlimit, upperlimit float32) float32 {
		if x < lowerlimit {
			x = lowerlimit
		}
		if x > upperlimit {
			x = upperlimit
		}
		return x
	}
	smoothstep := func(edge0, edge1, x float32) float32 {
		// Scale, bias and saturate x to 0..1 range
		x = clamp((x-edge0)/(edge1-edge0), 0.0, 1.0)
		// Evaluate polynomial
		return x * x * (3 - 2*x)
	}*/
	mix := func(from, to float64, amount float64) float64 {
		return from*(1-amount) + to*amount
	}
	fbmAt := func(x, y float64) (float64, float64, float64) {
		var (
			amplitude = 1.0
			frequency = 1.0
			noise     = 0.0
			slopex    = 0.0
			slopey    = 0.0
		)
		for range space.Octaves {
			sX := x / space.Scale * frequency
			sY := y / space.Scale * frequency
			n, dx, dy := dnoise(sX, sY)
			n = (n + 1) / 2
			//Uncomment for Ridged noise
			//n = 1 - math.Abs(n)
			//n = math.Abs(n)
			noise += n * amplitude
			slopex += dx * amplitude * frequency
			slopey += dy * amplitude * frequency
			amplitude *= space.Persistence
			frequency *= space.Lacunarity
		}
		return noise, slopex, slopey
	}
	noiseAt := func(x, y float64) (float64, float64, float64) {
		//Uncomment for Domain warping
		/*x2 := fbmAt(x, y)
		y2 := fbmAt(x+5.2, y+1.3)
		x3 := fbmAt(x+8*x2.noise+1.7, y+8*y2.noise+9.2)
		y3 := fbmAt(x+8*x2.noise+8.3, y+8*y2.noise+2.8)
		return fbmAt(x+8*x3.noise, y+8*y3.noise)*/
		return fbmAt(x, y)
	}
	/*linePoint := func(px, py, x1, y1, x2, y2 float32) *math32.Vector2 {
		var dx, dy = x2 - x1, y2 - y1
		var length = math32.Sqrt(dx*dx + dy*dy)
		dx, dy = dx/length, dy/length
		var posOnLine = math32.Min(length, math32.Max(0, dx*(px-x1)+dy*(py-y1)))
		return &math32.Vector2{x1 + posOnLine*dx, x2 + posOnLine*dy}
	}*/
	var (
		smooth = float64(space.Scale)
		limit  = float64(space.Size) * float64(space.Scale)
	)
	for x < 0 {
		x = limit - x
	}
	for y < 0 {
		y = limit - y
	}
	for x >= limit {
		x -= limit
	}
	for y >= limit {
		y -= limit
	}
	//Diagonal area needs to smooth bothways.
	if x > limit-smooth && y > limit-smooth {
		diffx := limit - x
		diffy := limit - y
		if diffx/smooth > diffy/smooth {
			var (
				LocalPoint  = vector2.New(diffy/smooth+diffx/smooth, 0)
				RemotePoint = vector2.New(0, diffy/smooth+diffx/smooth)
			)
			if 1-diffx/smooth < diffy/smooth {
				LocalPoint = vector2.New(1, diffy/smooth-(1-diffx/smooth))
				RemotePoint = vector2.New(diffy/smooth-(1-diffx/smooth), 1)
			}
			var (
				d = vector2.Distance(LocalPoint, vector2.New(diffx/smooth, diffy/smooth)) / LocalPoint.DistanceTo(RemotePoint)
			)
			d *= 2
			d = (1 - d)
			if d > 1 {
				d = 1
			}
			a, _, _ := space.NoiseAt(x, y-limit)
			b, _, _ := space.NoiseAt(x-limit, y)
			c, _, _ := noiseAt(x, y)
			return mix(
				mix(a, c, diffy/smooth),
				mix(b, c, diffx/smooth),
				1-d,
			), 0, 0
		}
		a, _, _ := space.NoiseAt(x, y-limit)
		b, _, _ := noiseAt(x, y)
		return mix(a, b, diffx/smooth), 0, 0
	}
	if x > limit-smooth {
		diff := limit - x
		a, _, _ := space.NoiseAt(x-limit, y)
		b, _, _ := space.NoiseAt(x, y)
		return mix(a, b, diff/smooth), 0, 0
	}
	if y > limit-smooth {
		diff := limit - y
		a, _, _ := space.NoiseAt(x, y-limit)
		b, _, _ := space.NoiseAt(x, y)
		return mix(a, b, diff/smooth), 0, 0
	}
	return noiseAt(x, y)
}

var perm = [512]byte{151, 160, 137, 91, 90, 15,
	131, 13, 201, 95, 96, 53, 194, 233, 7, 225, 140, 36, 103, 30, 69, 142, 8, 99, 37, 240, 21, 10, 23,
	190, 6, 148, 247, 120, 234, 75, 0, 26, 197, 62, 94, 252, 219, 203, 117, 35, 11, 32, 57, 177, 33,
	88, 237, 149, 56, 87, 174, 20, 125, 136, 171, 168, 68, 175, 74, 165, 71, 134, 139, 48, 27, 166,
	77, 146, 158, 231, 83, 111, 229, 122, 60, 211, 133, 230, 220, 105, 92, 41, 55, 46, 245, 40, 244,
	102, 143, 54, 65, 25, 63, 161, 1, 216, 80, 73, 209, 76, 132, 187, 208, 89, 18, 169, 200, 196,
	135, 130, 116, 188, 159, 86, 164, 100, 109, 198, 173, 186, 3, 64, 52, 217, 226, 250, 124, 123,
	5, 202, 38, 147, 118, 126, 255, 82, 85, 212, 207, 206, 59, 227, 47, 16, 58, 17, 182, 189, 28, 42,
	223, 183, 170, 213, 119, 248, 152, 2, 44, 154, 163, 70, 221, 153, 101, 155, 167, 43, 172, 9,
	129, 22, 39, 253, 19, 98, 108, 110, 79, 113, 224, 232, 178, 185, 112, 104, 218, 246, 97, 228,
	251, 34, 242, 193, 238, 210, 144, 12, 191, 179, 162, 241, 81, 51, 145, 235, 249, 14, 239, 107,
	49, 192, 214, 31, 181, 199, 106, 157, 184, 84, 204, 176, 115, 121, 50, 45, 127, 4, 150, 254,
	138, 236, 205, 93, 222, 114, 67, 29, 24, 72, 243, 141, 128, 195, 78, 66, 215, 61, 156, 180,
	151, 160, 137, 91, 90, 15,
	131, 13, 201, 95, 96, 53, 194, 233, 7, 225, 140, 36, 103, 30, 69, 142, 8, 99, 37, 240, 21, 10, 23,
	190, 6, 148, 247, 120, 234, 75, 0, 26, 197, 62, 94, 252, 219, 203, 117, 35, 11, 32, 57, 177, 33,
	88, 237, 149, 56, 87, 174, 20, 125, 136, 171, 168, 68, 175, 74, 165, 71, 134, 139, 48, 27, 166,
	77, 146, 158, 231, 83, 111, 229, 122, 60, 211, 133, 230, 220, 105, 92, 41, 55, 46, 245, 40, 244,
	102, 143, 54, 65, 25, 63, 161, 1, 216, 80, 73, 209, 76, 132, 187, 208, 89, 18, 169, 200, 196,
	135, 130, 116, 188, 159, 86, 164, 100, 109, 198, 173, 186, 3, 64, 52, 217, 226, 250, 124, 123,
	5, 202, 38, 147, 118, 126, 255, 82, 85, 212, 207, 206, 59, 227, 47, 16, 58, 17, 182, 189, 28, 42,
	223, 183, 170, 213, 119, 248, 152, 2, 44, 154, 163, 70, 221, 153, 101, 155, 167, 43, 172, 9,
	129, 22, 39, 253, 19, 98, 108, 110, 79, 113, 224, 232, 178, 185, 112, 104, 218, 246, 97, 228,
	251, 34, 242, 193, 238, 210, 144, 12, 191, 179, 162, 241, 81, 51, 145, 235, 249, 14, 239, 107,
	49, 192, 214, 31, 181, 199, 106, 157, 184, 84, 204, 176, 115, 121, 50, 45, 127, 4, 150, 254,
	138, 236, 205, 93, 222, 114, 67, 29, 24, 72, 243, 141, 128, 195, 78, 66, 215, 61, 156, 180,
}

/*
Skewing factors for 2D simplex grid:

	F2 = 0.5*(sqrt(3.0)-1.0)
	G2 = (3.0-Math.sqrt(3.0))/6.0
*/
const f2 = 0.366025403
const g2 = 0.211324865

func fastFloor(x float64) int {
	if x > 0 {
		return int(x)
	}
	return int(x) - 1
}

var grad2lut = [8][2]float64{
	{-1.0, -1.0}, {1.0, 0.0}, {-1.0, 0.0}, {1.0, 1.0},
	{-1.0, 1.0}, {0.0, -1.0}, {0.0, 1.0}, {1.0, -1.0},
}

func grad2(hash int, gx, gy *float64) {
	var h = hash & 7
	*gx = grad2lut[h][0]
	*gy = grad2lut[h][1]
	return
}

func dnoise(x, y float64) (noise, dx, dy float64) {
	var n0, n1, n2 float64 // Noise contributions from the three corners

	// Skew the input space to determine which simplex cell we're in
	var s float64 = (x + y) * f2 // Hairy factor for 2D
	var xs float64 = x + s
	var ys float64 = y + s
	var i int = fastFloor(xs)
	var j int = fastFloor(ys)

	var t float64 = float64(i+j) * g2
	var X0 float64 = float64(i) - t // Unskew the cell origin back to (x,y) space
	var Y0 float64 = float64(j) - t
	var x0 float64 = x - X0 // The x,y distances from the cell origin
	var y0 float64 = y - Y0

	// For the 2D case, the simplex shape is an equilateral triangle.
	// Determine which simplex we are in.
	var i1, j1 int // Offsets for second (middle) corner of simplex in (i,j) coords
	if x0 > y0 {
		// lower triangle, XY order: (0,0)->(1,0)->(1,1)
		i1 = 1
		j1 = 0
	} else {
		// upper triangle, YX order: (0,0)->(0,1)->(1,1)
		i1 = 0
		j1 = 1
	}

	// A step of (1,0) in (i,j) means a step of (1-c,-c) in (x,y), and
	// a step of (0,1) in (i,j) means a step of (-c,1-c) in (x,y), where
	// c = (3-sqrt(3))/6

	var x1 float64 = x0 - float64(i1) + g2 // Offsets for middle corner in (x,y) unskewed coords
	var y1 float64 = y0 - float64(j1) + g2
	var x2 float64 = x0 - 1.0 + 2.0*g2 // Offsets for last corner in (x,y) unskewed coords
	var y2 float64 = y0 - 1.0 + 2.0*g2

	// Wrap the integer indices at 256, to avoid indexing details::perm[] out of bounds
	var ii int = i & 0xff
	var jj int = j & 0xff

	var gx0, gy0, gx1, gy1, gx2, gy2 float64 /* Gradients at simplex corners */

	/* Calculate the contribution from the three corners */
	var t0 float64 = 0.5 - x0*x0 - y0*y0
	var t20, t40 float64
	if t0 < 0.0 {
		/* No influence */
		t40 = 0
		t20 = 0
		t0 = 0
		n0 = 0
		gx0 = 0
		gy0 = 0
	} else {
		grad2(int(perm[ii+int(perm[jj])]), &gx0, &gy0)
		t20 = t0 * t0
		t40 = t20 * t20
		n0 = t40 * (gx0*x0 + gy0*y0)
	}

	var t1 = 0.5 - x1*x1 - y1*y1
	var t21, t41 float64
	if t1 < 0.0 {
		/* No influence */
		t21 = 0
		t41 = 0
		t1 = 0
		n1 = 0
		gx1 = 0
		gy1 = 0
	} else {
		grad2(int(perm[ii+i1+int(perm[jj+j1])]), &gx1, &gy1)
		t21 = t1 * t1
		t41 = t21 * t21
		n1 = t41 * (gx1*x1 + gy1*y1)
	}

	var t2 = 0.5 - x2*x2 - y2*y2
	var t22, t42 float64
	if t2 < 0 {
		/* No influence */
		t42 = 0
		t22 = 0
		t2 = 0
		n2 = 0
		gx2 = 0
		gy2 = 0
	} else {
		grad2(int(perm[ii+1+int(perm[jj+1])]), &gx2, &gy2)
		t22 = t2 * t2
		t42 = t22 * t22
		n2 = t42 * (gx2*x2 + gy2*y2)
	}

	/* Compute derivative, if requested by supplying non-null pointers
	 * for the last two arguments */
	/*  A straight, unoptimised calculation would be like:
	 *    *dnoise_dx = -8.0f * t20 * t0 * x0 * ( gx0 * x0 + gy0 * y0 ) + t40 * gx0;
	 *    *dnoise_dy = -8.0f * t20 * t0 * y0 * ( gx0 * x0 + gy0 * y0 ) + t40 * gy0;
	 *    *dnoise_dx += -8.0f * t21 * t1 * x1 * ( gx1 * x1 + gy1 * y1 ) + t41 * gx1;
	 *    *dnoise_dy += -8.0f * t21 * t1 * y1 * ( gx1 * x1 + gy1 * y1 ) + t41 * gy1;
	 *    *dnoise_dx += -8.0f * t22 * t2 * x2 * ( gx2 * x2 + gy2 * y2 ) + t42 * gx2;
	 *    *dnoise_dy += -8.0f * t22 * t2 * y2 * ( gx2 * x2 + gy2 * y2 ) + t42 * gy2;
	 */
	var temp0 = t20 * t0 * (gx0*x0 + gy0*y0)
	var dnoise_dx = temp0 * x0
	var dnoise_dy = temp0 * y0
	var temp1 = t21 * t1 * (gx1*x1 + gy1*y1)
	dnoise_dx += temp1 * x1
	dnoise_dy += temp1 * y1
	var temp2 = t22 * t2 * (gx2*x2 + gy2*y2)
	dnoise_dx += temp2 * x2
	dnoise_dy += temp2 * y2
	dnoise_dx *= -8.0
	dnoise_dy *= -8.0
	dnoise_dx += t40*gx0 + t41*gx1 + t42*gx2
	dnoise_dy += t40*gy0 + t41*gy1 + t42*gy2
	dnoise_dx *= 40.0 /* Scale derivative to match the noise scaling */
	dnoise_dy *= 40.0

	return 40 * (n0 + n1 + n2), dnoise_dx, dnoise_dy
	// Add contributions from each corner to get the final noise value.
	// The result is scaled to return values in the interval [-1,1].
	/*#ifdef SIMPLEX_DERIVATIVES_RESCALE
	  	return glm::vec3( 70.175438596f * (n0 + n1 + n2), dnoise_dx, dnoise_dy ); // TODO: The scale factor is preliminary!
	  #else
	  	return glm::vec3( 40.0f * (n0 + n1 + n2), dnoise_dx, dnoise_dy ); // TODO: The scale factor is preliminary!
	  #endif*/
}
