package streamxlsx

import "fmt"

type AxisGenerator struct {
	yAxis  int
	xAxis  rune
	xAxis2 rune
	Column int
}

func NewAxisGenerator(y int) AxisGenerator {
	return AxisGenerator{
		yAxis: y,
		xAxis: 'A',
	}
}

func (a *AxisGenerator) GetCurrent() string {
	x := string(a.xAxis)
	if a.xAxis2 != 0 {
		x = string(a.xAxis2) + x
	}

	return fmt.Sprintf("%s%d", x, a.yAxis)
}

func (a *AxisGenerator) NextColumn() {
	a.incrementX()
}

func (a *AxisGenerator) NextRow() {
	a.incrementY()
	a.resetX()
}

func (a *AxisGenerator) SetRowFrom(row int) {
	a.yAxis = row
}

func (a *AxisGenerator) resetX() {
	a.xAxis = 'A'
	a.xAxis2 = 0
}

func (a *AxisGenerator) incrementX() {
	if a.xAxis == 'Z' {
		a.xAxis = 'A'
		a.incrementX2()
	} else {
		a.xAxis++
	}
}

func (a *AxisGenerator) incrementX2() {
	if a.xAxis2 == 0 {
		a.xAxis2 = 'A'
	} else {
		a.xAxis++
	}
}

func (a *AxisGenerator) incrementY() {
	a.yAxis++
}
