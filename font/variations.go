// SPDX-License-Identifier: Unlicense OR BSD-3-Clause

package font

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"

	"github.com/boxesandglue/typesetting/font/opentype/tables"
)

// axis records
type fvar []tables.VariationAxisRecord

func newFvar(table tables.Fvar) fvar { return table.FvarRecords.Axis }

type mvar struct {
	store  tables.ItemVarStore
	values []tables.VarValueRecord
}

func newMvar(mv tables.MVAR, axisCount int) (mvar, error) {
	if got := mv.ItemVariationStore.AxisCount(); got != axisCount {
		return mvar{}, fmt.Errorf("mvar: invalid number of axis (%d != %d)", got, axisCount)
	}
	return mvar{mv.ItemVariationStore, mv.ValueRecords}, nil
}

// return 0 if `tag` is not found
func (mv mvar) getVar(tag Tag, coords []VarCoord) float32 {
	// binary search
	for i, j := 0, len(mv.values); i < j; {
		h := i + (j-i)/2
		entry := mv.values[h]
		if tag < entry.ValueTag {
			j = h
		} else if entry.ValueTag < tag {
			i = h + 1
		} else {
			return mv.store.GetDelta(entry.Index, coords)
		}
	}
	return 0
}

// ---------------------------------- gvar ----------------------------------

type gvar struct {
	sharedTuples         [][]VarCoord       // with size tupleCount x axisCount
	variations           [][]tupleVariation // with length glyphCount
	sharedTupleActiveIdx []int              // with length tupleCount
}

func newGvar(table tables.Gvar, glyf tables.Glyf) (gvar, error) {
	if len(table.GlyphVariationDatas) != len(glyf) {
		return gvar{}, fmt.Errorf("invalid 'gvar' table: mismatch in glyphs count")
	}

	out := gvar{
		sharedTuples:         make([][]VarCoord, len(table.SharedTuples.SharedTuples)),
		variations:           make([][]tupleVariation, len(table.GlyphVariationDatas)),
		sharedTupleActiveIdx: make([]int, len(table.SharedTuples.SharedTuples)),
	}
	for i, ts := range table.SharedTuples.SharedTuples {
		out.sharedTuples[i] = ts.Values
	}
	for i, vs := range table.GlyphVariationDatas {
		tvs := make([]tupleVariation, len(vs.TupleVariationHeaders))
		for j, header := range vs.TupleVariationHeaders {
			tvs[j].TupleVariationHeader = header
		}

		pointsNumberCountAll := pointNumbersCount(glyf[i]) + phantomCount
		err := parseGlyphVariationSerializedData(vs.SerializedData,
			vs.HasSharedPointNumbers(), pointsNumberCountAll, false, tvs)
		if err != nil {
			return out, err
		}
		out.variations[i] = tvs
	}

	// For shared tuples that only have one axis active, share the index of
	// that axis as a cache. This will speed up caclulateScalar() a lot
	// for fonts with lots of axes and many "monovar" tuples.
	for i, tuple := range out.sharedTuples {
		idx := -1
		for j, peak := range tuple {
			if peak != 0 {
				if idx != -1 { // two peaks or more, do not cache
					idx = -1
					break
				}
				idx = j
			}
		}
		out.sharedTupleActiveIdx[i] = idx
	}

	return out, nil
}

type tupleVariation struct {
	tables.TupleVariationHeader

	pointNumbers []uint16 // nil means allPointsNumbers
	// length 2*len(pointNumbers) for gvar table or 2*allPointsNumbers if zero
	deltas []int16
}

// sharedTuples has length tupleCount x axisCount
// sharedTupleActiveIdx has length tupleCount
func (t tupleVariation) calculateScalar(coords []VarCoord, sharedTuples [][]VarCoord, sharedTupleActiveIdx []int) float32 {
	startIdx, endIdx := 0, len(coords)
	peakTuple := t.PeakTuple.Values
	if peakTuple == nil { // no peak specified -> use shared tuple
		index := t.Index()
		if int(index) >= len(sharedTuples) { // should not happend
			return 0.
		}
		peakTuple = sharedTuples[index]

		// use the cache to restrict the range
		if v := sharedTupleActiveIdx[index]; v != -1 {
			startIdx = v
			endIdx = startIdx + 1
		}
	}

	startTuple, endTuple := t.IntermediateTuples[0].Values, t.IntermediateTuples[1].Values
	hasIntermediate := startTuple != nil

	var scalar float32 = 1.
	for i := startIdx; i < endIdx; i++ {
		v, peak := coords[i], peakTuple[i]
		if peak == 0 || v == peak {
			continue
		}

		if hasIntermediate {
			start := startTuple[i]
			end := endTuple[i]
			if start > peak || peak > end || (start < 0 && end > 0 && peak != 0) {
				continue
			}
			if v < start || v > end {
				return 0.
			}
			if v < peak {
				if peak != start {
					scalar *= float32(v-start) / float32(peak-start)
				}
			} else {
				if peak != end {
					scalar *= float32(end-v) / float32(end-peak)
				}
			}
		} else if v == 0 || v < minC(0, peak) || v > maxC(0, peak) {
			return 0.
		} else {
			scalar *= float32(v) / float32(peak)
		}
	}
	return scalar
}

// complete `out`, which contains the parsed tuple headers.
// pointNumbersCountAll is used when the tuple variation data provides deltas for all glyph points
func parseGlyphVariationSerializedData(data []byte, hasSharedPoints bool, pointNumbersCountAll int, isCvar bool, out []tupleVariation) error {
	var (
		sharedPointNumbers []uint16
		err                error
	)
	if hasSharedPoints {
		sharedPointNumbers, data, err = parsePointNumbers(data)
		if err != nil {
			return err
		}
	}

	for i, h := range out {
		// adjust for the next iteration
		if len(data) < int(h.VariationDataSize) {
			return errors.New("invalid glyph variation serialized data (EOF)")
		}
		nextData := data[h.VariationDataSize:]

		// default to shared points
		privatePointNumbers := sharedPointNumbers
		if h.HasPrivatePointNumbers() {
			privatePointNumbers, data, err = parsePointNumbers(data)
			if err != nil {
				return err
			}
		}
		// the number of point is precised or defaut to all the points
		pointCount := pointNumbersCountAll
		if privatePointNumbers != nil {
			pointCount = len(privatePointNumbers)
		}

		out[i].pointNumbers = privatePointNumbers

		if !isCvar {
			pointCount *= 2 // for X and Y
		}

		out[i].deltas, err = unpackDeltas(data, pointCount)
		if err != nil {
			return err
		}

		data = nextData
	}
	return nil
}

// the returned slice is nil if all glyph points are used
func parsePointNumbers(data []byte) ([]uint16, []byte, error) {
	count, data, err := getPackedPointCount(data)
	if err != nil {
		return nil, nil, err
	}
	if count == 0 {
		return nil, data, nil
	}

	var lastPoint uint16
	points := make([]uint16, 0, count) // max value of count is 32767
	for len(points) < int(count) {     // loop through the runs
		if len(data) == 0 {
			return nil, nil, errors.New("invalid glyph variation points numbers (EOF)")
		}
		control := data[0]
		is16bit := control&0x80 != 0
		runLength := int(control&0x7F + 1)
		if is16bit {
			pts, err := tables.ParseUint16s(data[1:], runLength)
			if err != nil {
				return nil, nil, fmt.Errorf("invalid glyph variation points numbers: %s", err)
			}
			for _, pt := range pts {
				actualValue := pt + lastPoint
				points = append(points, actualValue)
				lastPoint = actualValue
			}
			data = data[1+2*runLength:]
		} else {
			if len(data) < 1+runLength {
				return nil, nil, errors.New("invalid glyph variation points numbers (EOF)")
			}
			for _, b := range data[1 : 1+runLength] {
				actualValue := uint16(b) + lastPoint
				points = append(points, actualValue)
				lastPoint = actualValue
			}
			data = data[1+runLength:]
		}
	}

	return points, data, nil
}

// return the remaining data and special case of 00
func getPackedPointCount(data []byte) (uint16, []byte, error) {
	const highOrderBit byte = 1 << 7
	if len(data) < 1 {
		return 0, nil, errors.New("invalid glyph variation points numbers (EOF)")
	}
	if data[0] == 0 {
		return 0, data[1:], nil
	} else if data[0]&highOrderBit == 0 {
		count := uint16(data[0])
		return count, data[1:], nil
	} else {
		if len(data) < 2 {
			return 0, nil, errors.New("invalid glyph variation points numbers (EOF)")
		}
		count := uint16(data[0]&^highOrderBit)<<8 | uint16(data[1])
		return count, data[2:], nil
	}
}

func unpackDeltas(data []byte, pointNumbersCount int) ([]int16, error) {
	const (
		deltasAreZero     = 0x80
		deltasAreWords    = 0x40
		deltaRunCountMask = 0x3F
	)
	out := make([]int16, pointNumbersCount)
	nbRead := 0 // number of point read : out[:nbRead] is valid
	// The data is read until the expected logic count of deltas is obtained.
	for nbRead < pointNumbersCount {
		if len(data) == 0 {
			return nil, errors.New("invalid packed deltas (EOF)")
		}
		control := data[0]
		count := control&deltaRunCountMask + 1
		if isZero := control&deltasAreZero != 0; isZero {
			//  no additional value to read, just fill with zeros
			nbRead += int(count)
			data = data[1:]
		} else {
			// we want to fill out[nbRead:nbRead+count-1], that is we must have
			// nbRead+count-1 < pointNumbersCount, ie
			// nbRead+count <= pointNumbersCount
			if got := nbRead + int(count); got > pointNumbersCount {
				return nil, fmt.Errorf("invalid packed deltas (expected %d point numbers, got %d)", pointNumbersCount, got)
			}
			isInt16 := control&deltasAreWords != 0
			if isInt16 {
				if len(data) < 1+2*int(count) {
					return nil, errors.New("invalid packed deltas (EOF)")
				}
				for i := byte(0); i < count; i++ { // count < 64 -> no overflow
					out[nbRead] = int16(binary.BigEndian.Uint16(data[1+2*i:]))
					nbRead++
				}
				data = data[1+2*count:]
			} else {
				if len(data) < 1+int(count) {
					return nil, errors.New("invalid packed deltas (EOF)")
				}
				for i := byte(0); i < count; i++ { // count < 64 -> no overflow
					out[nbRead] = int16(int8(data[1+i]))
					nbRead++
				}
				data = data[1+count:]
			}
		}
	}
	return out, nil
}

// update `points` in place
func (gvar gvar) applyDeltasToPoints(glyph gID, coords []VarCoord, points []contourPoint) {
	// adapted from harfbuzz/src/hb-ot-var-gvar-table.hh

	if int(glyph) >= len(gvar.variations) { // should not happend
		return
	}

	// save original points for inferred delta calculation
	origPoints := append([]contourPoint(nil), points...)
	// flag is used to indicate referenced point
	deltas := make([]contourPoint, len(points))

	var endPoints []int // index into points
	for i, p := range points {
		if p.isEndPoint {
			endPoints = append(endPoints, i)
		}
	}

	varData := gvar.variations[glyph]
	for _, tuple := range varData {
		scalar := tuple.calculateScalar(coords, gvar.sharedTuples, gvar.sharedTupleActiveIdx)
		if scalar == 0 {
			continue
		}
		L := len(tuple.deltas)
		applyToAll := tuple.pointNumbers == nil
		xDeltas, yDeltas := tuple.deltas[:L/2], tuple.deltas[L/2:]

		// reset the current deltas
		for i := range deltas {
			deltas[i] = contourPoint{}
		}

		for i := range xDeltas {
			ptIndex := uint16(i)
			if !applyToAll {
				ptIndex = tuple.pointNumbers[i]
			}
			deltas[ptIndex].isExplicit = true
			deltas[ptIndex].X += float32(xDeltas[i]) * scalar
			deltas[ptIndex].Y += float32(yDeltas[i]) * scalar
		}

		/* infer deltas for unreferenced points */
		startPoint := 0
		for _, endPoint := range endPoints {
			// check the number of unreferenced points in a contour.
			// If no unref points or no ref points, nothing to do.
			unrefCount := 0
			for _, p := range deltas[startPoint : endPoint+1] {
				if !p.isExplicit {
					unrefCount++
				}
			}
			j := startPoint
			if unrefCount == 0 || unrefCount > endPoint-startPoint {
				goto noMoreGaps
			}

			for {
				/* Locate the next gap of unreferenced points between two referenced points prev and next.
				 * Note that a gap may wrap around at left (startPoint) and/or at right (endPoint).
				 */
				var prev, next, i int
				for {
					i = j
					j = nextIndex(i, startPoint, endPoint)
					if deltas[i].isExplicit && !deltas[j].isExplicit {
						break
					}
				}
				prev, j = i, i
				for {
					i = j
					j = nextIndex(i, startPoint, endPoint)
					if !deltas[i].isExplicit && deltas[j].isExplicit {
						break
					}
				}
				next = j
				/* Infer deltas for all unref points in the gap between prev and next */
				i = prev
				for {
					i = nextIndex(i, startPoint, endPoint)
					if i == next {
						break
					}
					deltas[i].X = inferDelta(origPoints[i].X, origPoints[prev].X, origPoints[next].X, deltas[prev].X, deltas[next].X)
					deltas[i].Y = inferDelta(origPoints[i].Y, origPoints[prev].Y, origPoints[next].Y, deltas[prev].Y, deltas[next].Y)
					unrefCount--
					if unrefCount == 0 {
						goto noMoreGaps
					}
				}
			}
		noMoreGaps:
			startPoint = endPoint + 1
		}

		// apply specified / inferred deltas to points
		for i, d := range deltas {
			points[i].translate(d.X, d.Y)
		}
	}
}

func nextIndex(i, start, end int) int {
	if i >= end {
		return start
	}
	return i + 1
}

func inferDelta(targetVal, prevVal, nextVal, prevDelta, nextDelta float32) float32 {
	if prevVal == nextVal {
		if prevDelta == nextDelta {
			return prevDelta
		}
		return 0
	} else if targetVal <= minF(prevVal, nextVal) {
		if prevVal < nextVal {
			return prevDelta
		}
		return nextDelta
	} else if targetVal >= maxF(prevVal, nextVal) {
		if prevVal > nextVal {
			return prevDelta
		}
		return nextDelta
	}

	// linear interpolation
	r := (targetVal - prevVal) / (nextVal - prevVal)
	return prevDelta + r*(nextDelta-prevDelta)
}

// ------------------------------ hvar/vvar ------------------------------

func getAdvanceDeltaUnscaled(t *tables.HVAR, glyph tables.GlyphID, coords []VarCoord) float32 {
	index := t.AdvanceWidthMapping.Index(glyph)
	return t.ItemVariationStore.GetDelta(index, coords)
}

func getLsbDeltaUnscaled(t *tables.HVAR, glyph tables.GlyphID, coords []VarCoord) float32 {
	if t.LsbMapping == nil {
		return 0
	}
	index := t.LsbMapping.Index(glyph)
	return t.ItemVariationStore.GetDelta(index, coords)
}

func sanitizeGDEF(table tables.GDEF, axisCount int) error {
	// check axis count
	if got := table.ItemVarStore.AxisCount(); got != -1 && got != axisCount {
		return fmt.Errorf("GDEF: invalid number of axis (%d != %d)", axisCount, got)
	}

	// check LigCarets length
	if table.LigCaretList.Coverage != nil {
		expected := table.LigCaretList.Coverage.Len()
		got := len(table.LigCaretList.LigGlyphs)
		if expected != got {
			return fmt.Errorf("GDEF: invalid number of lig gyphs (%d != %d)", expected, got)
		}
	}
	return nil
}

// ------------------------------------- external API -------------------------------------

// Variation defines a value for a wanted variation axis.
type Variation struct {
	Tag   Tag     // Variation-axis identifier tag
	Value float32 // In design units
}

// SetVariations applies a list of font-variation settings to a font,
// defaulting to the values given in the `fvar` table.
// Note that passing an empty slice will instead remove the coordinates.
func (face *Face) SetVariations(variations []Variation) {
	if len(variations) == 0 {
		face.SetCoords(nil)
		return
	}

	fv := face.Font.fvar
	if len(fv) == 0 { // the font is not variable...
		face.SetCoords(nil)
		return
	}

	designCoords := fv.getDesignCoordsDefault(variations)

	face.SetCoords(face.NormalizeVariations(designCoords))
}

// getDesignCoordsDefault returns the design coordinates corresponding to the given pairs of axis/value.
// The default value of the axis is used when not specified in the variations.
func (fv fvar) getDesignCoordsDefault(variations []Variation) []float32 {
	designCoords := make([]float32, len(fv))
	// start with default values
	for i, axis := range fv {
		designCoords[i] = axis.Default
	}

	fv.getDesignCoords(variations, designCoords)

	return designCoords
}

// getDesignCoords updates the design coordinates, with the given pairs of axis/value.
// It will panic if `designCoords` has not the length expected by the table, that is the number of axis.
func (fv fvar) getDesignCoords(variations []Variation, designCoords []float32) {
	for _, variation := range variations {
		// allow for multiple axis with the same tag
		for index, axis := range fv {
			if axis.Tag == variation.Tag {
				designCoords[index] = variation.Value
			}
		}
	}
}

// normalize based on the [min,def,max] values for the axis to be [-1,0,1].
func (fv fvar) normalizeCoordinates(coords []float32) []VarCoord {
	normalized := make([]VarCoord, len(coords))
	for i, a := range fv {
		coord := coords[i]

		// out of range: clamping
		if coord > a.Maximum {
			coord = a.Maximum
		} else if coord < a.Minimum {
			coord = a.Minimum
		}

		if coord < a.Default {
			coord = -(coord - a.Default) / (a.Minimum - a.Default)
		} else if coord > a.Default {
			coord = (coord - a.Default) / (a.Maximum - a.Default)
		} else {
			coord = 0
		}

		normalized[i] = VarCoord(math.Round(float64(coord * 16384))) // 1 << 14
	}
	return normalized
}

// NormalizeVariations normalize the given design-space coordinates. The minimum and maximum
// values for the axis are mapped to the interval [-1,1], with the default
// axis value mapped to 0.
//
// Any additional scaling defined in the face's `avar` table is also
// applied, as described at https://docs.microsoft.com/en-us/typography/opentype/spec/avar.
//
// This method panics if `coords` has not the correct length, that is the number of axis inf 'fvar'.
func (f *Font) NormalizeVariations(coords []float32) []VarCoord {
	// ported from freetype2

	// Axis normalization is a two-stage process.  First we normalize
	// based on the [min,def,max] values for the axis to be [-1,0,1].
	// Then, if there's an `avar' table, we renormalize this range.
	normalized := f.fvar.normalizeCoordinates(coords)

	// now applying 'avar'
	for i, av := range f.avar.AxisSegmentMaps {
		l := av.AxisValueMaps
		for j := 1; j < len(l); j++ {
			previous, pair := l[j-1], l[j]
			if normalized[i] < pair.FromCoordinate {

				normalized[i] = previous.ToCoordinate + VarCoord(math.Round(float64(normalized[i]-previous.FromCoordinate)*
					float64(pair.ToCoordinate-previous.ToCoordinate)/float64(pair.FromCoordinate-previous.FromCoordinate)))
				break
			}
		}
	}

	return normalized
}
