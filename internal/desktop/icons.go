//go:build windows

package desktop

import (
	"fmt"
	"math"
	"sync"
	"unsafe"
)

// The embedded Fluent System Icons are MIT licensed; see THIRD_PARTY_NOTICES.md.
// All paths are rasterized at the current DPI with grayscale anti-aliasing.

type iconDef struct {
	ViewBox float64
	D       string
}

var iconTable = map[string]iconDef{
	// DeskPulse-drawn icons, sharing the same 24-unit grid.
	"moon":          {24, "M20.8 15.1C17.7 15.7 14.6 14.9 12.4 12.6C10.1 10.4 9.3 7.3 9.9 4.2C5.7 5.2 3 8.8 3 12.9C3 17.9 7 22 12.1 22C16.2 22 19.8 19.3 20.8 15.1ZM18.4 17C16.9 19.4 14.7 20.4 12.1 20.4C8 20.4 4.6 17 4.6 12.9C4.6 10.3 5.6 8.1 8 6.6C7.9 9.3 9.1 11.8 11.2 13.8C13.2 15.9 15.7 17.1 18.4 17Z"},
	"sun":           {24, "M12 6.6C15 6.6 17.4 9 17.4 12C17.4 15 15 17.4 12 17.4C9 17.4 6.6 15 6.6 12C6.6 9 9 6.6 12 6.6ZM12 8.2C9.9 8.2 8.2 9.9 8.2 12C8.2 14.1 9.9 15.8 12 15.8C14.1 15.8 15.8 14.1 15.8 12C15.8 9.9 14.1 8.2 12 8.2ZM11.2 1.8H12.8V5H11.2ZM11.2 19H12.8V22.2H11.2ZM1.8 11.2H5V12.8H1.8ZM19 11.2H22.2V12.8H19ZM4.2 5.3L5.3 4.2L7.6 6.4L6.4 7.6ZM16.4 17.6L17.6 16.4L19.8 18.7L18.7 19.8ZM16.4 6.4L18.7 4.2L19.8 5.3L17.6 7.6ZM4.2 18.7L6.4 16.4L7.6 17.6L5.3 19.8Z"},
	"copy":          {24, "M10 3H18C19.7 3 21 4.3 21 6V14C21 15.6 19.8 16.9 18.2 17V15.4C18.9 15.3 19.4 14.8 19.4 14V6C19.4 5.2 18.8 4.6 18 4.6H10C9.2 4.6 8.7 5.1 8.6 5.8H7C7.1 4.2 8.4 3 10 3ZM6 7H14C15.7 7 17 8.3 17 10V18C17 19.7 15.7 21 14 21H6C4.3 21 3 19.7 3 18V10C3 8.3 4.3 7 6 7ZM6 8.6C5.2 8.6 4.6 9.2 4.6 10V18C4.6 18.8 5.2 19.4 6 19.4H14C14.8 19.4 15.4 18.8 15.4 18V10C15.4 9.2 14.8 8.6 14 8.6Z"},
	"refresh":       {24, "M19 3.5H20.6V10.5H13.6V8.9H17.9C16.6 6.8 14.4 5.6 12 5.6C8.4 5.6 5.4 8.5 5.4 12.2C5.4 15.8 8.4 18.8 12 18.8C15 18.8 17.6 16.7 18.4 14H20C19.2 17.7 15.9 20.4 12 20.4C7.5 20.4 3.8 16.7 3.8 12.2C3.8 7.6 7.5 4 12 4C14.8 4 17.4 5.3 19 7.5Z"},
	"network":       {24, "M2.6 8.3C7.9 3.4 16.1 3.4 21.4 8.3L20.3 9.5C15.6 5.2 8.4 5.2 3.7 9.5ZM5.5 11.7C9.2 8.3 14.8 8.3 18.5 11.7L17.4 12.9C14.3 10.1 9.7 10.1 6.6 12.9ZM8.4 15.2C10.4 13.4 13.6 13.4 15.6 15.2L14.5 16.4C13.1 15.1 10.9 15.1 9.5 16.4ZM12 17.9C12.8 17.9 13.4 18.5 13.4 19.3C13.4 20.1 12.8 20.7 12 20.7C11.2 20.7 10.6 20.1 10.6 19.3C10.6 18.5 11.2 17.9 12 17.9Z"},
	"pulse":         {24, "M8.46238 6.80905L11.746 20.426C11.9236 21.1626 12.957 21.2011 13.1891 20.4798L16.4456 10.3575L17.0318 12.4532C17.1224 12.7772 17.4176 13.0012 17.7541 13.0012H21.2477C21.6619 13.0012 21.9977 12.6654 21.9977 12.2512C21.9977 11.837 21.6619 11.5012 21.2477 11.5012H18.3231L17.2181 7.55053C17.0179 6.83439 16.0096 6.81496 15.7819 7.52284L12.5785 17.4797L9.22531 3.57419C9.04279 2.81728 7.97039 2.80542 7.77117 3.5581L5.66883 11.5012H2.75C2.33579 11.5012 2 11.837 2 12.2512C2 12.6654 2.33579 13.0012 2.75 13.0012H6.24614C6.58645 13.0012 6.88411 12.7721 6.97118 12.4431L8.46238 6.80905Z"},
	"settings":      {24, "M12.0122 2.25C12.7462 2.25846 13.4773 2.34326 14.1937 2.50304C14.5064 2.57279 14.7403 2.83351 14.7758 3.15196L14.946 4.67881C15.0231 5.37986 15.615 5.91084 16.3206 5.91158C16.5103 5.91188 16.6979 5.87238 16.8732 5.79483L18.2738 5.17956C18.5651 5.05159 18.9055 5.12136 19.1229 5.35362C20.1351 6.43464 20.8889 7.73115 21.3277 9.14558C21.4223 9.45058 21.3134 9.78203 21.0564 9.9715L19.8149 10.8866C19.4607 11.1468 19.2516 11.56 19.2516 11.9995C19.2516 12.4389 19.4607 12.8521 19.8157 13.1129L21.0582 14.0283C21.3153 14.2177 21.4243 14.5492 21.3297 14.8543C20.8911 16.2685 20.1377 17.5649 19.1261 18.6461C18.9089 18.8783 18.5688 18.9483 18.2775 18.8206L16.8712 18.2045C16.4688 18.0284 16.0068 18.0542 15.6265 18.274C15.2463 18.4937 14.9933 18.8812 14.945 19.3177L14.7759 20.8444C14.741 21.1592 14.5122 21.4182 14.204 21.4915C12.7556 21.8361 11.2465 21.8361 9.79803 21.4915C9.48991 21.4182 9.26105 21.1592 9.22618 20.8444L9.05736 19.32C9.00777 18.8843 8.75434 18.498 8.37442 18.279C7.99451 18.06 7.5332 18.0343 7.1322 18.2094L5.72557 18.8256C5.43422 18.9533 5.09403 18.8833 4.87678 18.6509C3.86462 17.5685 3.11119 16.2705 2.6732 14.8548C2.57886 14.5499 2.68786 14.2186 2.94485 14.0293L4.18818 13.1133C4.54232 12.8531 4.75147 12.4399 4.75147 12.0005C4.75147 11.561 4.54232 11.1478 4.18771 10.8873L2.94516 9.97285C2.6878 9.78345 2.5787 9.45178 2.67337 9.14658C3.11212 7.73215 3.86594 6.43564 4.87813 5.35462C5.09559 5.12236 5.43594 5.05259 5.72724 5.18056L7.12762 5.79572C7.53056 5.97256 7.9938 5.94585 8.37577 5.72269C8.75609 5.50209 9.00929 5.11422 9.05817 4.67764L9.22824 3.15196C9.26376 2.83335 9.49786 2.57254 9.8108 2.50294C10.5281 2.34342 11.26 2.25865 12.0122 2.25ZM12.0124 3.7499C11.5583 3.75524 11.1056 3.79443 10.6578 3.86702L10.5489 4.84418C10.4471 5.75368 9.92003 6.56102 9.13042 7.01903C8.33597 7.48317 7.36736 7.53903 6.52458 7.16917L5.62629 6.77456C5.05436 7.46873 4.59914 8.25135 4.27852 9.09168L5.07632 9.67879C5.81513 10.2216 6.25147 11.0837 6.25147 12.0005C6.25147 12.9172 5.81513 13.7793 5.0771 14.3215L4.27805 14.9102C4.59839 15.752 5.05368 16.5361 5.626 17.2316L6.53113 16.8351C7.36923 16.4692 8.33124 16.5227 9.12353 16.9794C9.91581 17.4361 10.4443 18.2417 10.548 19.1526L10.657 20.1365C11.5466 20.2878 12.4555 20.2878 13.3451 20.1365L13.4541 19.1527C13.5549 18.2421 14.0828 17.4337 14.876 16.9753C15.6692 16.5168 16.6332 16.463 17.4728 16.8305L18.3772 17.2267C18.949 16.5323 19.4041 15.7495 19.7247 14.909L18.9267 14.3211C18.1879 13.7783 17.7516 12.9162 17.7516 11.9995C17.7516 11.0827 18.1879 10.2206 18.9258 9.67847L19.7227 9.09109C19.4021 8.25061 18.9468 7.46784 18.3748 6.77356L17.4783 7.16737C17.113 7.32901 16.7178 7.4122 16.3187 7.41158C14.849 7.41004 13.6155 6.30355 13.4551 4.84383L13.3462 3.8667C12.9007 3.7942 12.4526 3.75512 12.0124 3.7499ZM11.9997 8.24995C14.0708 8.24995 15.7497 9.92888 15.7497 12C15.7497 14.071 14.0708 15.75 11.9997 15.75C9.92863 15.75 8.2497 14.071 8.2497 12C8.2497 9.92888 9.92863 8.24995 11.9997 8.24995ZM11.9997 9.74995C10.7571 9.74995 9.7497 10.7573 9.7497 12C9.7497 13.2426 10.7571 14.25 11.9997 14.25C13.2423 14.25 14.2497 13.2426 14.2497 12C14.2497 10.7573 13.2423 9.74995 11.9997 9.74995Z"},
	"chevron-up":    {20, "M4 12 L10 6 L16 12 L15 13 L10 8 L5 13 Z"},
	"chevron-right": {20, "M7.64582 4.14708C7.84073 3.95147 8.15731 3.9509 8.35292 4.14582L13.8374 9.6108C14.0531 9.82574 14.0531 10.1751 13.8374 10.39L8.35292 15.855C8.15731 16.0499 7.84073 16.0493 7.64582 15.8537C7.4509 15.6581 7.45147 15.3415 7.64708 15.1466L12.8117 10.0004L7.64708 4.85418C7.45147 4.65927 7.4509 4.34269 7.64582 4.14708Z"},
	"chevron-down":  {20, "M15.8537 7.64582C16.0493 7.84073 16.0499 8.15731 15.855 8.35292L10.39 13.8374C10.1751 14.0531 9.82574 14.0531 9.6108 13.8374L4.14582 8.35292C3.9509 8.15731 3.95147 7.84073 4.14708 7.64582C4.34269 7.4509 4.65927 7.45147 4.85418 7.64708L10.0004 12.8117L15.1466 7.64708C15.3415 7.45147 15.6581 7.4509 15.8537 7.64582Z"},
	"dismiss":       {20, "M4.08859 4.21569L4.14645 4.14645C4.32001 3.97288 4.58944 3.9536 4.78431 4.08859L4.85355 4.14645L10 9.293L15.1464 4.14645C15.32 3.97288 15.5894 3.9536 15.7843 4.08859L15.8536 4.14645C16.0271 4.32001 16.0464 4.58944 15.9114 4.78431L15.8536 4.85355L10.707 10L15.8536 15.1464C16.0271 15.32 16.0464 15.5894 15.9114 15.7843L15.8536 15.8536C15.68 16.0271 15.4106 16.0464 15.2157 15.9114L15.1464 15.8536L10 10.707L4.85355 15.8536C4.67999 16.0271 4.41056 16.0464 4.21569 15.9114L4.14645 15.8536C3.97288 15.68 3.9536 15.4106 4.08859 15.2157L4.14645 15.1464L9.293 10L4.14645 4.85355C3.97288 4.67999 3.9536 4.41056 4.08859 4.21569L4.14645 4.14645L4.08859 4.21569Z"},
	"more":          {20, "M6.25 10C6.25 10.6904 5.69036 11.25 5 11.25C4.30964 11.25 3.75 10.6904 3.75 10C3.75 9.30964 4.30964 8.75 5 8.75C5.69036 8.75 6.25 9.30964 6.25 10ZM11.25 10C11.25 10.6904 10.6904 11.25 10 11.25C9.30964 11.25 8.75 10.6904 8.75 10C8.75 9.30964 9.30964 8.75 10 8.75C10.6904 8.75 11.25 9.30964 11.25 10ZM15 11.25C15.6904 11.25 16.25 10.6904 16.25 10C16.25 9.30964 15.6904 8.75 15 8.75C14.3096 8.75 13.75 9.30964 13.75 10C13.75 10.6904 14.3096 11.25 15 11.25Z"},
	"sparkle":       {20, "M7.39804 12.8088C7.57428 12.9331 7.78476 12.9997 8.00043 12.9993C8.21633 12.9995 8.42686 12.932 8.60243 12.8063C8.77993 12.6758 8.91464 12.4955 8.98943 12.2883L9.43643 10.9153C9.55086 10.571 9.74391 10.2581 10.0003 10.0014C10.2566 9.74467 10.5693 9.5512 10.9134 9.4363L12.3044 8.9853C12.4564 8.93095 12.5936 8.84214 12.7055 8.72586C12.8174 8.60957 12.9008 8.46896 12.9492 8.31503C12.9977 8.16111 13.0098 7.99806 12.9847 7.83867C12.9596 7.67927 12.8979 7.52786 12.8044 7.3963C12.6703 7.21038 12.4794 7.07314 12.2604 7.0053L10.8854 6.5583C10.5409 6.44407 10.2278 6.25111 9.97087 5.99472C9.71396 5.73833 9.52035 5.42558 9.40543 5.0813L8.95343 3.6933C8.88113 3.491 8.74761 3.31624 8.57143 3.1933C8.43877 3.09957 8.28607 3.03809 8.12548 3.01375C7.96489 2.9894 7.80083 3.00286 7.64636 3.05306C7.49188 3.10326 7.35125 3.18881 7.23564 3.3029C7.12004 3.417 7.03265 3.5565 6.98043 3.7103L6.52343 5.1103C6.40884 5.44513 6.21967 5.74954 5.97022 6.00055C5.72076 6.25157 5.41753 6.44263 5.08343 6.5593L3.69243 7.0073C3.54065 7.0617 3.40352 7.15048 3.29177 7.26669C3.18001 7.3829 3.09666 7.5234 3.04824 7.67719C2.99982 7.83097 2.98764 7.99388 3.01265 8.15315C3.03767 8.31243 3.0992 8.46376 3.19243 8.5953C3.32027 8.77475 3.50105 8.90972 3.70943 8.9813L5.08343 9.4263C5.52354 9.57278 5.90999 9.84713 6.19343 10.2143C6.35585 10.4249 6.4813 10.6616 6.56443 10.9143L7.01643 12.3053C7.08846 12.5086 7.22179 12.6845 7.39804 12.8088ZM7.48343 5.3943L8.01043 4.0173L8.44943 5.3943C8.61312 5.88745 8.88991 6.33546 9.25767 6.70253C9.62544 7.0696 10.074 7.34554 10.5674 7.5083L11.9734 8.0373L10.5914 8.4853C10.0987 8.6496 9.65095 8.92646 9.28382 9.29393C8.9167 9.66141 8.64026 10.1094 8.47643 10.6023L7.95343 11.9803L7.50443 10.6013C7.34335 10.108 7.06943 9.65913 6.70443 9.2903C6.3356 8.92226 5.88653 8.64467 5.39243 8.4793L4.01443 7.9573L5.40043 7.5073C5.88672 7.33867 6.32775 7.06051 6.68943 6.6943C7.04901 6.3266 7.32049 5.88211 7.48343 5.3943ZM13.5353 16.8513C13.6713 16.9475 13.8337 16.9992 14.0003 16.9993C14.1654 16.9994 14.3264 16.9484 14.4613 16.8533C14.6008 16.7548 14.7058 16.6149 14.7613 16.4533L15.0093 15.6913C15.0625 15.5329 15.1515 15.3888 15.2693 15.2703C15.3867 15.1518 15.5307 15.063 15.6893 15.0113L16.4613 14.7593C16.619 14.7048 16.7557 14.6024 16.8523 14.4663C16.9257 14.3633 16.9736 14.2444 16.9921 14.1193C17.0106 13.9942 16.9992 13.8665 16.9588 13.7467C16.9184 13.6268 16.8501 13.5183 16.7597 13.4299C16.6692 13.3415 16.5591 13.2759 16.4383 13.2383L15.6743 12.9893C15.5162 12.9368 15.3724 12.8481 15.2544 12.7305C15.1364 12.6128 15.0473 12.4693 14.9943 12.3113L14.7423 11.5383C14.6886 11.3805 14.586 11.2439 14.4493 11.1483C14.3473 11.0754 14.2295 11.0274 14.1056 11.0084C13.9816 10.9893 13.8549 10.9997 13.7357 11.0386C13.6164 11.0775 13.508 11.1439 13.4192 11.2325C13.3304 11.321 13.2636 11.4292 13.2243 11.5483L12.9773 12.3103C12.925 12.468 12.8375 12.6116 12.7213 12.7303C12.6066 12.8468 12.4667 12.9354 12.3123 12.9893L11.5393 13.2413C11.3803 13.2952 11.2422 13.3978 11.1447 13.5346C11.0472 13.6713 10.9952 13.8353 10.9961 14.0032C10.997 14.1712 11.0507 14.3346 11.1496 14.4703C11.2486 14.606 11.3877 14.7071 11.5473 14.7593L12.3103 15.0063C12.4692 15.0597 12.6136 15.149 12.7323 15.2673C12.8505 15.3856 12.939 15.5302 12.9903 15.6893L13.2433 16.4633C13.2981 16.6198 13.4001 16.7554 13.5353 16.8513Z"},
}

type pathPoint struct{ X, Y float64 }

// subpath is a flattened, fill-ready contour.
type subpath struct {
	Points []pathPoint
}

// parsePathData parses the SVG path grammar subset used by the embedded icons
// (M L H V C S Q T Z and their lowercase relatives; arcs fall back to a line).
func parsePathData(d string) []subpath {
	tokens := tokenizePath(d)
	var out []subpath
	var cur []pathPoint
	var cx, cy, lastCX, lastCY float64
	var startX, startY float64
	var haveStart bool
	addCur := func() {
		if len(cur) >= 3 {
			out = append(out, subpath{Points: cur})
		}
		cur = nil
	}
	lineTo := func(x, y float64) { cur = append(cur, pathPoint{X: x, Y: y}) }
	numPos := 0
	num := func() float64 {
		if numPos < len(tokens) && tokens[numPos].kind == 'n' {
			v := tokens[numPos].num
			numPos++
			return v
		}
		return 0
	}
	nextCmd := byte(0)
	for numPos < len(tokens) {
		t := tokens[numPos]
		var cmd byte
		if t.kind == 'c' {
			cmd = t.cmd
			numPos++
		} else {
			// Implicit repetition of the previous command (M becomes L).
			cmd = nextCmd
			if cmd == 0 {
				numPos++
				continue
			}
		}
		switch cmd {
		case 'M', 'm':
			x, y := num(), num()
			if cmd == 'm' {
				x += cx
				y += cy
			}
			addCur()
			cx, cy, startX, startY = x, y, x, y
			haveStart = true
			lineTo(x, y)
			nextCmd = 'L'
			if cmd == 'm' {
				nextCmd = 'l'
			}
		case 'L', 'l':
			x, y := num(), num()
			if cmd == 'l' {
				x += cx
				y += cy
			}
			cx, cy = x, y
			lineTo(x, y)
			nextCmd = cmd
		case 'H', 'h':
			x := num()
			if cmd == 'h' {
				x += cx
			}
			cx = x
			lineTo(cx, cy)
			nextCmd = cmd
		case 'V', 'v':
			y := num()
			if cmd == 'v' {
				y += cy
			}
			cy = y
			lineTo(cx, cy)
			nextCmd = cmd
		case 'C', 'c':
			x1, y1, x2, y2, x, y := num(), num(), num(), num(), num(), num()
			if cmd == 'c' {
				x1 += cx
				y1 += cy
				x2 += cx
				y2 += cy
				x += cx
				y += cy
			}
			cur = flattenCubic(cur, cx, cy, x1, y1, x2, y2, x, y, 24)
			lastCX, lastCY = x2, y2
			cx, cy = x, y
			nextCmd = cmd
		case 'S', 's':
			x2, y2, x, y := num(), num(), num(), num()
			if cmd == 's' {
				x2 += cx
				y2 += cy
				x += cx
				y += cy
			}
			x1, y1 := 2*cx-lastCX, 2*cy-lastCY
			cur = flattenCubic(cur, cx, cy, x1, y1, x2, y2, x, y, 24)
			lastCX, lastCY = x2, y2
			cx, cy = x, y
			nextCmd = cmd
		case 'Q', 'q':
			x1, y1, x, y := num(), num(), num(), num()
			if cmd == 'q' {
				x1 += cx
				y1 += cy
				x += cx
				y += cy
			}
			cur = flattenQuad(cur, cx, cy, x1, y1, x, y, 16)
			lastCX, lastCY = x1, y1
			cx, cy = x, y
			nextCmd = cmd
		case 'T', 't':
			x, y := num(), num()
			if cmd == 't' {
				x += cx
				y += cy
			}
			x1, y1 := 2*cx-lastCX, 2*cy-lastCY
			cur = flattenQuad(cur, cx, cy, x1, y1, x, y, 16)
			lastCX, lastCY = x1, y1
			cx, cy = x, y
			nextCmd = cmd
		case 'A', 'a':
			// The embedded icons contain no arcs; approximate by consuming the
			// parameters and drawing a straight line to the endpoint.
			_, _, _, _, _, _ = num(), num(), num(), num(), num(), num()
			x, y := num(), num()
			if cmd == 'a' {
				x += cx
				y += cy
			}
			cx, cy = x, y
			lineTo(cx, cy)
			nextCmd = cmd
		case 'Z', 'z':
			if haveStart {
				cur = append(cur, pathPoint{X: startX, Y: startY})
			}
			cx, cy = startX, startY
			nextCmd = cmd
		default:
			// Unknown command: skip it; numbers until the next command token
			// are consumed implicitly by the loop.
			nextCmd = 0
		}
	}
	addCur()
	return out
}

type pathToken struct {
	kind byte // 'n' number, 'c' command
	num  float64
	cmd  byte
}

func tokenizePath(d string) []pathToken {
	out := make([]pathToken, 0, len(d)/3)
	i := 0
	for i < len(d) {
		c := d[i]
		switch {
		case c == ' ' || c == ',' || c == '\t' || c == '\n' || c == '\r':
			i++
		case (c >= '0' && c <= '9') || c == '-' || c == '+' || c == '.':
			j := i
			if d[j] == '-' || d[j] == '+' {
				j++
			}
			for j < len(d) && (d[j] >= '0' && d[j] <= '9' || d[j] == '.') {
				j++
			}
			if j < len(d) && (d[j] == 'e' || d[j] == 'E') {
				j++
				if j < len(d) && (d[j] == '-' || d[j] == '+') {
					j++
				}
				for j < len(d) && d[j] >= '0' && d[j] <= '9' {
					j++
				}
			}
			var v float64
			fmt.Sscanf(d[i:j], "%g", &v)
			out = append(out, pathToken{kind: 'n', num: v})
			i = j
		default:
			out = append(out, pathToken{kind: 'c', cmd: c})
			i++
		}
	}
	return out
}

// flattenCubic appends the flattened curve points and returns the grown
// slice, following Go's append semantics.
func flattenCubic(out []pathPoint, x0, y0, x1, y1, x2, y2, x3, y3 float64, steps int) []pathPoint {
	for s := 1; s <= steps; s++ {
		t := float64(s) / float64(steps)
		u := 1 - t
		x := u*u*u*x0 + 3*u*u*t*x1 + 3*u*t*t*x2 + t*t*t*x3
		y := u*u*u*y0 + 3*u*u*t*y1 + 3*u*t*t*y2 + t*t*t*y3
		out = append(out, pathPoint{X: x, Y: y})
	}
	return out
}

// flattenQuad appends the flattened quadratic points and returns the slice.
func flattenQuad(out []pathPoint, x0, y0, x1, y1, x2, y2 float64, steps int) []pathPoint {
	for s := 1; s <= steps; s++ {
		t := float64(s) / float64(steps)
		u := 1 - t
		x := u*u*x0 + 2*u*t*x1 + t*t*x2
		y := u*u*y0 + 2*u*t*y1 + t*t*y2
		out = append(out, pathPoint{X: x, Y: y})
	}
	return out
}

// rasterizePath renders flattened contours into an RGBA coverage map with
// 4× vertical supersampling and exact horizontal span coverage (nonzero rule).
func rasterizePath(subs []subpath, w, h int32, sx, sy, scale float64) []uint8 {
	type edge struct {
		x1, y1, x2, y2 float64
		dir            int
	}
	var edges []edge
	for _, sp := range subs {
		if len(sp.Points) < 3 {
			continue
		}
		pts := make([]pathPoint, len(sp.Points))
		for i, p := range sp.Points {
			pts[i] = pathPoint{X: p.X*scale + sx, Y: p.Y*scale + sy}
		}
		for i := 0; i < len(pts)-1; i++ {
			a, b := pts[i], pts[i+1]
			if a.Y == b.Y {
				continue
			}
			dir := 1
			if b.Y < a.Y {
				dir = -1
			}
			edges = append(edges, edge{a.X, a.Y, b.X, b.Y, dir})
		}
	}
	if len(edges) == 0 {
		return nil
	}
	cov := make([]float32, int(w)*int(h))
	crossings := make([]float64, 0, 64)
	winds := make([]int, 0, 64)
	const subsamples = 4
	for py := int32(0); py < h; py++ {
		base := float64(py)
		for s := 0; s < subsamples; s++ {
			y := base + (float64(s)+0.5)/subsamples
			crossings = crossings[:0]
			winds = winds[:0]
			for _, e := range edges {
				lo, hi := e.y1, e.y2
				if lo > hi {
					lo, hi = hi, lo
				}
				if y >= lo && y < hi {
					t := (y - e.y1) / (e.y2 - e.y1)
					crossings = append(crossings, e.x1+t*(e.x2-e.x1))
					winds = append(winds, e.dir)
				}
			}
			if len(crossings) == 0 {
				continue
			}
			// Sort crossings together with windings by x.
			for i := 1; i < len(crossings); i++ {
				x, wd := crossings[i], winds[i]
				j := i - 1
				for j >= 0 && crossings[j] > x {
					crossings[j+1] = crossings[j]
					winds[j+1] = winds[j]
					j--
				}
				crossings[j+1] = x
				winds[j+1] = wd
			}
			winding := 0
			spanStart := 0.0
			for i := 0; i < len(crossings); i++ {
				prev := winding
				winding += winds[i]
				if prev == 0 && winding != 0 {
					spanStart = crossings[i]
				} else if prev != 0 && winding == 0 {
					// Fill [spanStart, crossings[i]].
					a, b := spanStart, crossings[i]
					if b > a {
						px := int32(a)
						if px < 0 {
							px = 0
						}
						endPx := int32(b + 0.999999)
						if endPx > w {
							endPx = w
						}
						for x := px; x < endPx; x++ {
							l := float64(x)
							ov := b - maxFloat(a, l)
							ov = minFloat(ov, l+1-a)
							if ov > 0 {
								cov[int(py)*int(w)+int(x)] += float32(ov / subsamples)
							}
						}
					}
				}
			}
		}
	}
	out := make([]uint8, int(w)*int(h))
	for i, v := range cov {
		if v > 1 {
			v = 1
		}
		out[i] = uint8(v*255 + 0.5)
	}
	return out
}

func maxFloat(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

func minFloat(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

// iconBitmap is a cached premultiplied-ARGB DIB of one rasterized icon.
type iconBitmap struct {
	hdc, bmp, old uintptr
	w, h          int32
}

type iconKey struct {
	name  string
	color uint32
	w, h  int32
}

var (
	iconMu    sync.Mutex
	iconCache = map[iconKey]*iconBitmap{}
)

// buildIconBitmap rasterizes and uploads one icon. The returned bitmap owns a
// memory DC with the bitmap selected.
func buildIconBitmap(name string, color uint32, w, h int32) (*iconBitmap, bool) {
	def, ok := iconTable[name]
	if !ok || w <= 0 || h <= 0 {
		return nil, false
	}
	subs := parsePathData(def.D)
	// Fit the viewBox into the box, preserving aspect and centering.
	scale := float64(w) / def.ViewBox
	if vh := float64(h) / def.ViewBox; vh < scale {
		scale = vh
	}
	sx := (float64(w) - def.ViewBox*scale) / 2
	sy := (float64(h) - def.ViewBox*scale) / 2
	cov := rasterizePath(subs, w, h, sx, sy, scale)
	if cov == nil {
		return nil, false
	}
	hdc, bmp, old, bits := createBufferDC(w, h)
	if hdc == 0 {
		return nil, false
	}
	r := byte(color & 0xFF)
	g := byte((color >> 8) & 0xFF)
	b := byte((color >> 16) & 0xFF)
	pix := make([]byte, int(w)*int(h)*4)
	for i, a := range cov {
		// Premultiplied alpha.
		o := i * 4
		pix[o+0] = byte(uint16(b) * uint16(a) / 255)
		pix[o+1] = byte(uint16(g) * uint16(a) / 255)
		pix[o+2] = byte(uint16(r) * uint16(a) / 255)
		pix[o+3] = a
	}
	memCopy(bits, goPtr(&pix[0]), uintptr(len(pix)))
	return &iconBitmap{hdc: hdc, bmp: bmp, old: old, w: w, h: h}, true
}

func addPtr(p *byte, n int) unsafe.Pointer {
	return unsafe.Add(unsafe.Pointer(p), n)
}

// drawIcon renders a named icon into box on hdc at device resolution.
func drawIcon(hdc uintptr, name string, color uint32, box rect) {
	w := box.Right - box.Left
	h := box.Bottom - box.Top
	if w <= 0 || h <= 0 {
		return
	}
	key := iconKey{name: name, color: color, w: w, h: h}
	iconMu.Lock()
	bmp := iconCache[key]
	if bmp == nil {
		var ok bool
		bmp, ok = buildIconBitmap(name, color, w, h)
		if !ok {
			iconMu.Unlock()
			return
		}
		iconCache[key] = bmp
	}
	iconMu.Unlock()
	blendIcon(hdc, box.Left, box.Top, w, h, bmp.hdc)
}

// drawActionLabel centers the icon and caption as one unit. Narrow buttons
// give the caption priority when the localized text needs the full width.
func drawActionLabel(hdc uintptr, area rect, label, icon string, font uintptr, ink uint32, dpi uint32) {
	const center = dtCenter | dtVCenter | dtSingleLine | dtEndEllipsis | dtNoPrefix
	if icon == "" {
		drawText(hdc, label, area, font, ink, center)
		return
	}
	width, _ := measureText(hdc, label, font)
	size, gap := scale(15, dpi), scale(6, dpi)
	groupWidth := width + size + gap
	if groupWidth+scale(12, dpi) > area.Right-area.Left {
		drawText(hdc, label, area, font, ink, center)
		return
	}
	start := (area.Left + area.Right - groupWidth) / 2
	marker := rect{start, area.Top, start + size, area.Bottom}
	drawIcon(hdc, icon, ink, iconRect(marker, size))
	caption := rect{marker.Right + gap, area.Top, area.Right, area.Bottom}
	drawText(hdc, label, caption, font, ink, dtLeft|dtVCenter|dtSingleLine|dtNoPrefix)
}

// deleteIconCache releases every cached icon bitmap (called at shutdown and
// after a DPI change so icons are re-rasterized at the new resolution).
func deleteIconCache() {
	iconMu.Lock()
	defer iconMu.Unlock()
	for k, b := range iconCache {
		releaseBufferDC(b.hdc, b.bmp, b.old)
		delete(iconCache, k)
	}
}

// swCircle fills an analytically anti-aliased circle into a Go-side BGRA
// pixel buffer (top-down). Used for the ball face and ring.
func swCircle(pix []byte, w, h int32, cx, cy, r float64, color uint32) {
	if len(pix) == 0 || r <= 0 || w <= 0 || h <= 0 {
		return
	}
	rBytes := byte(color & 0xFF)
	gBytes := byte((color >> 8) & 0xFF)
	bBytes := byte((color >> 16) & 0xFF)
	left := int(cx - r - 2)
	if left < 0 {
		left = 0
	}
	right := int(cx + r + 2)
	if right > int(w) {
		right = int(w)
	}
	top := int(cy - r - 2)
	if top < 0 {
		top = 0
	}
	bottom := int(cy + r + 2)
	if bottom > int(h) {
		bottom = int(h)
	}
	for y := top; y < bottom; y++ {
		for x := left; x < right; x++ {
			dx := float64(x) + 0.5 - cx
			dy := float64(y) + 0.5 - cy
			// Coverage = r - dist + 0.5 gives a 1px analytic anti-aliased edge.
			a := r - math.Sqrt(dx*dx+dy*dy) + 0.5
			if a <= 0 {
				continue
			}
			if a > 1 {
				a = 1
			}
			o := (y*int(w) + x) * 4
			inv := 1 - a
			pix[o+0] = byte(float64(bBytes)*a + float64(pix[o+0])*inv)
			pix[o+1] = byte(float64(gBytes)*a + float64(pix[o+1])*inv)
			pix[o+2] = byte(float64(rBytes)*a + float64(pix[o+2])*inv)
			pix[o+3] = 255
		}
	}
}
