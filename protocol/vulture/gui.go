package vulture

// ElementShaped represents a primitive shape.
type ElementShaped struct {
	Type Shape  // transparency
	Cell Cell   // within the area where the shape is located.
	Face Angle  // is the direction the view is facing (z axiz).
	Flip Angle  // around the x-axis.
	Spin Angle  // around the z-axis.
	Jump Height // up by the specified height.
	Bump uint8  // offsets the view within the cell by this amount.
	Next Offset // shape tuner.
	Tint Upload // texture
	Time Ticks  // Next frame.
}

type Shape uint8

const (
	Rectangle Shape = iota
	Triangle
	Circle
	CircleHalf
	Hexagon
	Box
	Bowl
	Torus
	Sphere
	SphereHalf
	Cylinder
	Pyramid
	Cone
)

type ElementBinary struct {
	Cell Cell   // within the area where the metric is located.
	Bump uint8  // offsets the metric within the cell by this amount.
	Time Ticks  // Next metric.
	View Upload // renderer for this metric.
	Type MetricType
	Data uint64 // metric data.
}

type MetricType uint8
