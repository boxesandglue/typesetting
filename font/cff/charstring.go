// SPDX-License-Identifier: Unlicense OR BSD-3-Clause

package cff

import (
	"errors"
	"fmt"

	ps "github.com/boxesandglue/typesetting/font/cff/interpreter"
	ot "github.com/boxesandglue/typesetting/font/opentype"
	"github.com/boxesandglue/typesetting/font/opentype/tables"
)

// LoadGlyph parses the glyph charstring to compute segments and path bounds.
// It returns an error if the glyph is invalid or if decoding the charstring fails.
func (f *CFF) LoadGlyph(glyph tables.GlyphID) ([]ot.Segment, ps.PathBounds, error) {
	if int(glyph) >= len(f.Charstrings) {
		return nil, ps.PathBounds{}, errGlyph
	}

	var (
		psi    ps.Machine
		loader type2CharstringHandler
		index  byte = 0
		err    error
	)
	if f.fdSelect != nil {
		index, err = f.fdSelect.fontDictIndex(glyph)
		if err != nil {
			return nil, ps.PathBounds{}, err
		}
	}

	subrs := f.localSubrs[index]
	err = psi.Run(f.Charstrings[glyph], subrs, f.globalSubrs, &loader)
	return loader.cs.Segments, loader.cs.Bounds, err
}

// type2CharstringHandler implements operators needed to fetch Type2 charstring metrics
type type2CharstringHandler struct {
	cs ps.CharstringReader

	// found in private DICT, needed since we can't differenciate
	// no width set from 0 width
	// `width` must be initialized to default width
	nominalWidthX float64
	width         float64
}

func (type2CharstringHandler) Context() ps.Context { return ps.Type2Charstring }

func (met *type2CharstringHandler) Apply(state *ps.Machine, op ps.Operator) error {
	var err error
	if !op.IsEscaped {
		switch op.Operator {
		case 11: // return
			return state.Return() // do not clear the arg stack
		case 14: // endchar
			if state.ArgStack.Top > 0 { // width is optional
				met.width = met.nominalWidthX + state.ArgStack.Vals[0]
			}
			met.cs.ClosePath()
			return ps.ErrInterrupt
		case 10: // callsubr
			return ps.LocalSubr(state) // do not clear the arg stack
		case 29: // callgsubr
			return ps.GlobalSubr(state) // do not clear the arg stack
		case 21: // rmoveto
			if state.ArgStack.Top > 2 { // width is optional
				met.width = met.nominalWidthX + state.ArgStack.Vals[0]
			}
			err = met.cs.Rmoveto(state)
		case 22: // hmoveto
			if state.ArgStack.Top > 1 { // width is optional
				met.width = met.nominalWidthX + state.ArgStack.Vals[0]
			}
			err = met.cs.Hmoveto(state)
		case 4: // vmoveto
			if state.ArgStack.Top > 1 { // width is optional
				met.width = met.nominalWidthX + state.ArgStack.Vals[0]
			}
			err = met.cs.Vmoveto(state)
		case 1, 18: // hstem, hstemhm
			met.cs.Hstem(state)
		case 3, 23: // vstem, vstemhm
			met.cs.Vstem(state)
		case 19, 20: // hintmask, cntrmask
			// variable number of arguments, but always even
			// for xxxmask, if there are arguments on the stack, then this is an impliied stem
			if state.ArgStack.Top&1 != 0 {
				met.width = met.nominalWidthX + state.ArgStack.Vals[0]
			}
			met.cs.Hintmask(state)
			// the stack is managed by the previous call
			return nil

		case 5: // rlineto
			met.cs.Rlineto(state)
		case 6: // hlineto
			met.cs.Hlineto(state)
		case 7: // vlineto
			met.cs.Vlineto(state)
		case 8: // rrcurveto
			met.cs.Rrcurveto(state)
		case 24: // rcurveline
			err = met.cs.Rcurveline(state)
		case 25: // rlinecurve
			err = met.cs.Rlinecurve(state)
		case 26: // vvcurveto
			met.cs.Vvcurveto(state)
		case 27: // hhcurveto
			met.cs.Hhcurveto(state)
		case 30: // vhcurveto
			met.cs.Vhcurveto(state)
		case 31: // hvcurveto
			met.cs.Hvcurveto(state)
		default:
			// no other operands are allowed before the ones handled above
			err = fmt.Errorf("invalid operator %s in charstring", op)
		}
	} else {
		switch op.Operator {
		case 34: // hflex
			err = met.cs.Hflex(state)
		case 35: // flex
			err = met.cs.Flex(state)
		case 36: // hflex1
			err = met.cs.Hflex1(state)
		case 37: // flex1
			err = met.cs.Flex1(state)
		default:
			// no other operands are allowed before the ones handled above
			err = fmt.Errorf("invalid operator %s in charstring", op)
		}
	}
	state.ArgStack.Clear()
	return err
}

// ---------------------------- CFF2 format ----------------------------

// LoadGlyph parses the glyph charstring to compute segments and path bounds.
// It returns an error if the glyph is invalid or if decoding the charstring fails.
//
// [coords] must either have the same length as the variations axis, or be empty,
// and be normalized
func (f *CFF2) LoadGlyph(glyph tables.GlyphID, coords []tables.Coord) ([]ot.Segment, ps.PathBounds, error) {
	if int(glyph) >= len(f.Charstrings) {
		return nil, ps.PathBounds{}, errGlyph
	}

	var (
		psi    ps.Machine
		loader cff2CharstringHandler
		index  byte = 0
		err    error
	)
	if f.fdSelect != nil {
		index, err = f.fdSelect.fontDictIndex(glyph)
		if err != nil {
			return nil, ps.PathBounds{}, err
		}
	}

	font := f.fonts[index]

	loader.coords = coords
	loader.vars = f.VarStore
	loader.setVSIndex(int(font.defaultVSIndex))

	err = psi.Run(f.Charstrings[glyph], font.localSubrs, f.globalSubrs, &loader)

	return loader.cs.Segments, loader.cs.Bounds, err
}

// cff2CharstringHandler implements operators needed to fetch CFF2 charstring metrics
type cff2CharstringHandler struct {
	cs ps.CharstringReader

	coords []tables.Coord // normalized variation coordinates
	vars   tables.ItemVarStore

	// the currently active ItemVariationData subtable (default to 0)
	scalars []float32 // computed from the currently active ItemVariationData subtable
}

func (cff2CharstringHandler) Context() ps.Context { return ps.Type2Charstring }

func (met *cff2CharstringHandler) setVSIndex(index int) error {
	// if the font has variations, always build the scalar
	// slice, even if no variations are activated by the user:
	// the blend operator needs to know how many args to skip.
	if len(met.vars.ItemVariationDatas) == 0 {
		return nil
	}

	if index >= len(met.vars.ItemVariationDatas) {
		return fmt.Errorf("invalid 'vsindex' %d", index)
	}

	vars := met.vars.ItemVariationDatas[index]
	k := int32(len(vars.RegionIndexes)) // number of regions
	met.scalars = append(met.scalars[:0], make([]float32, k)...)
	for i, regionIndex := range vars.RegionIndexes {
		region := met.vars.VariationRegionList.VariationRegions[regionIndex]
		met.scalars[i] = region.Evaluate(met.coords)
	}
	return nil
}

func (met *cff2CharstringHandler) blend(state *ps.Machine) error {
	// blend requires n*(k+1) + 1 arguments
	if state.ArgStack.Top < 1 {
		return errors.New("missing n argument for blend operator")
	}
	n := int32(state.ArgStack.Pop())
	k := int32(len(met.scalars))
	if state.ArgStack.Top < n*(k+1) {
		return errors.New("missing arguments for blend operator")
	}

	// actually apply the deltas only if the user has activated variations
	if len(met.coords) != 0 {
		args := state.ArgStack.Vals[state.ArgStack.Top-n*(k+1) : state.ArgStack.Top]
		// the first n values are the 'default' arguments
		for i := int32(0); i < n; i++ {
			baseValue := args[i]
			deltas := args[n+i*k : n+(i+1)*k] // all the regions, for one operand
			v := 0.
			for ik, delta := range deltas {
				v += float64(met.scalars[ik]) * delta
			}
			args[i] = baseValue + v // update the stack with the blended value
		}
	}

	// clear the stack, keeping only n arguments
	state.ArgStack.Top -= n * k

	return nil
}

func (met *cff2CharstringHandler) Apply(state *ps.Machine, op ps.Operator) error {
	var err error
	if !op.IsEscaped {
		switch op.Operator {
		case 1, 18: // hstem, hstemhm
			met.cs.Hstem(state)
		case 3, 23: // vstem, vstemhm
			met.cs.Vstem(state)
		case 4: // vmoveto
			err = met.cs.Vmoveto(state)
		case 5: // rlineto
			met.cs.Rlineto(state)
		case 6: // hlineto
			met.cs.Hlineto(state)
		case 7: // vlineto
			met.cs.Vlineto(state)
		case 8: // rrcurveto
			met.cs.Rrcurveto(state)
		case 10: // callsubr
			return ps.LocalSubr(state) // do not clear the arg stack
		case 15: // vsindex
			if state.ArgStack.Top < 1 {
				return errors.New("missing argument for vsindex operator")
			}
			err = met.setVSIndex(int(state.ArgStack.Pop()))
		case 16: // blend
			return met.blend(state) // do not clear the arg stack
		case 19, 20: // hintmask, cntrmask
			// variable number of arguments, but always even
			// for xxxmask, if there are arguments on the stack, then this is an impliied stem
			met.cs.Hintmask(state)
			// the stack is managed by the previous call
			return nil
		case 21: // rmoveto
			err = met.cs.Rmoveto(state)
		case 22: // hmoveto
			err = met.cs.Hmoveto(state)
		case 24: // rcurveline
			err = met.cs.Rcurveline(state)
		case 25: // rlinecurve
			err = met.cs.Rlinecurve(state)
		case 26: // vvcurveto
			met.cs.Vvcurveto(state)
		case 27: // hhcurveto
			met.cs.Hhcurveto(state)
		case 29: // callgsubr
			return ps.GlobalSubr(state) // do not clear the arg stack
		case 30: // vhcurveto
			met.cs.Vhcurveto(state)
		case 31: // hvcurveto
			met.cs.Hvcurveto(state)
		default:
			// no other operands are allowed before the ones handled above
			err = fmt.Errorf("invalid operator %s in charstring", op)
		}
	} else {
		switch op.Operator {
		case 34: // hflex
			err = met.cs.Hflex(state)
		case 35: // flex
			err = met.cs.Flex(state)
		case 36: // hflex1
			err = met.cs.Hflex1(state)
		case 37: // flex1
			err = met.cs.Flex1(state)
		default:
			// no other operands are allowed before the ones handled above
			err = fmt.Errorf("invalid operator %s in charstring", op)
		}
	}
	state.ArgStack.Clear()
	return err
}
