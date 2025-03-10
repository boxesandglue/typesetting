package harfbuzz

import (
	"fmt"

	"github.com/boxesandglue/typesetting/font"
	ot "github.com/boxesandglue/typesetting/font/opentype"
	"github.com/boxesandglue/typesetting/font/opentype/tables"
)

// Support functions for OpenType shaping related queries.
// ported from src/hb-ot-shape.cc Copyright © 2009,2010  Red Hat, Inc. 2010,2011,2012  Google, Inc. Behdad Esfahbod

/*
 * GSUB/GPOS feature query and enumeration interface
 */

const (
	// Special value for script index indicating unsupported script.
	NoScriptIndex = 0xFFFF
	// Special value for feature index indicating unsupported feature.
	NoFeatureIndex = 0xFFFF
	// Special value for language index indicating default or unsupported language.
	DefaultLanguageIndex = 0xFFFF
	// Special value for variations index indicating unsupported variation.
	noVariationsIndex = -1
)

type otShapePlanner struct {
	shaper                        otComplexShaper
	props                         SegmentProperties
	tables                        *font.Font // also used by the map builders
	map_                          otMapBuilder
	applyMorx                     bool
	scriptZeroMarks               bool
	scriptFallbackMarkPositioning bool
}

func newOtShapePlanner(tables *font.Font, props SegmentProperties) *otShapePlanner {
	var out otShapePlanner
	out.props = props
	out.tables = tables
	out.map_ = newOtMapBuilder(tables, props)

	/* https://github.com/harfbuzz/harfbuzz/issues/2124 */
	out.applyMorx = len(tables.Morx) != 0 && (props.Direction.isHorizontal() || len(tables.GSUB.Lookups) == 0)

	out.shaper = out.categorizeComplex()

	zwm, fb := out.shaper.marksBehavior()
	out.scriptZeroMarks = zwm != zeroWidthMarksNone
	out.scriptFallbackMarkPositioning = fb

	/* https://github.com/harfbuzz/harfbuzz/issues/1528 */
	if _, isDefault := out.shaper.(complexShaperDefault); out.applyMorx && !isDefault {
		out.shaper = complexShaperDefault{dumb: true}
	}
	return &out
}

func (planner *otShapePlanner) compile(plan *otShapePlan, key otShapePlanKey) {
	plan.props = planner.props
	plan.shaper = planner.shaper
	planner.map_.compile(&plan.map_, key)

	plan.fracMask = plan.map_.getMask1(ot.NewTag('f', 'r', 'a', 'c'))
	plan.numrMask = plan.map_.getMask1(ot.NewTag('n', 'u', 'm', 'r'))
	plan.dnomMask = plan.map_.getMask1(ot.NewTag('d', 'n', 'o', 'm'))
	plan.hasFrac = plan.fracMask != 0 || (plan.numrMask != 0 && plan.dnomMask != 0)

	plan.rtlmMask = plan.map_.getMask1(ot.NewTag('r', 't', 'l', 'm'))
	plan.hasVert = plan.map_.getMask1(ot.NewTag('v', 'e', 'r', 't')) != 0

	kernTag := ot.NewTag('v', 'k', 'r', 'n')
	if planner.props.Direction.isHorizontal() {
		kernTag = ot.NewTag('k', 'e', 'r', 'n')
	}

	plan.kernMask, _ = plan.map_.getMask(kernTag)
	plan.requestedKerning = plan.kernMask != 0
	plan.trakMask, _ = plan.map_.getMask(ot.NewTag('t', 'r', 'a', 'k'))
	plan.requestedTracking = plan.trakMask != 0

	hasGposKern := plan.map_.getFeatureIndex(1, kernTag) != NoFeatureIndex
	disableGpos := plan.shaper.gposTag() != 0 && plan.shaper.gposTag() != plan.map_.chosenScript[1]

	// Decide who provides glyph classes. GDEF or Unicode.
	if planner.tables.GDEF.GlyphClassDef == nil {
		plan.fallbackGlyphClasses = true
	}

	// Decide who does substitutions. GSUB, morx, or fallback.
	plan.applyMorx = planner.applyMorx

	//  Decide who does positioning. GPOS, kerx, kern, or fallback.
	hasKerx := planner.tables.Kerx != nil
	hasGSUB := !plan.applyMorx && planner.tables.GSUB.Lookups != nil
	hasGPOS := !disableGpos && planner.tables.GPOS.Lookups != nil

	if hasKerx && !(hasGSUB && hasGPOS) {
		plan.applyKerx = true
	} else if hasGPOS {
		plan.applyGpos = true
	}

	if !plan.applyKerx && (!hasGposKern || !plan.applyGpos) {
		// apparently Apple applies kerx if GPOS kern was not applied.
		if hasKerx {
			plan.applyKerx = true
		} else if planner.tables.Kern != nil {
			plan.applyKern = true
		}
	}

	plan.applyFallbackKern = !(plan.applyGpos || plan.applyKerx || plan.applyKern)

	plan.zeroMarks = planner.scriptZeroMarks && !plan.applyKerx &&
		(!plan.applyKern || !hasMachineKerning(planner.tables.Kern))
	plan.hasGposMark = plan.map_.getMask1(ot.NewTag('m', 'a', 'r', 'k')) != 0

	plan.adjustMarkPositioningWhenZeroing = !plan.applyGpos && !plan.applyKerx &&
		(!plan.applyKern || !hasCrossKerning(planner.tables.Kern))

	plan.fallbackMarkPositioning = plan.adjustMarkPositioningWhenZeroing && planner.scriptFallbackMarkPositioning

	// If we're using morx shaping, we cancel mark position adjustment because
	// Apple Color Emoji assumes this will NOT be done when forming emoji sequences;
	// https://github.com/harfbuzz/harfbuzz/issues/2967.
	if plan.applyMorx {
		plan.adjustMarkPositioningWhenZeroing = false
	}

	// currently we always apply trak.
	plan.applyTrak = plan.requestedTracking && !planner.tables.Trak.IsEmpty()
}

type otShapePlan struct {
	shaper otComplexShaper
	props  SegmentProperties

	map_ otMap

	fracMask GlyphMask
	numrMask GlyphMask
	dnomMask GlyphMask
	rtlmMask GlyphMask
	kernMask GlyphMask
	trakMask GlyphMask

	hasFrac                          bool
	requestedTracking                bool
	requestedKerning                 bool
	hasVert                          bool
	hasGposMark                      bool
	zeroMarks                        bool
	fallbackGlyphClasses             bool
	fallbackMarkPositioning          bool
	adjustMarkPositioningWhenZeroing bool

	applyGpos         bool
	applyFallbackKern bool
	applyKern         bool
	applyKerx         bool
	applyMorx         bool
	applyTrak         bool
}

func (sp *otShapePlan) init0(tables *font.Font, props SegmentProperties, userFeatures []Feature, otKey otShapePlanKey) {
	planner := newOtShapePlanner(tables, props)

	planner.collectFeatures(userFeatures)

	planner.compile(sp, otKey)

	sp.shaper.dataCreate(sp)
}

func (sp *otShapePlan) substitute(font *Font, buffer *Buffer) {
	sp.map_.substitute(sp, font, buffer)
}

func (sp *otShapePlan) position(font *Font, buffer *Buffer) {
	if sp.applyGpos {
		sp.map_.position(sp, font, buffer)
	} else if sp.applyKerx {
		sp.aatLayoutPosition(font, buffer)
	}

	if sp.applyKern {
		sp.otLayoutKern(font, buffer)
	} else if sp.applyFallbackKern {
		sp.otApplyFallbackKern(font, buffer)
	}

	if sp.applyTrak {
		sp.aatLayoutTrack(font, buffer)
	}
}

var (
	commonFeatures = [...]otMapFeature{
		{ot.NewTag('a', 'b', 'v', 'm'), ffGLOBAL},
		{ot.NewTag('b', 'l', 'w', 'm'), ffGLOBAL},
		{ot.NewTag('c', 'c', 'm', 'p'), ffGLOBAL},
		{ot.NewTag('l', 'o', 'c', 'l'), ffGLOBAL},
		{ot.NewTag('m', 'a', 'r', 'k'), ffGlobalManualJoiners},
		{ot.NewTag('m', 'k', 'm', 'k'), ffGlobalManualJoiners},
		{ot.NewTag('r', 'l', 'i', 'g'), ffGLOBAL},
	}

	horizontalFeatures = [...]otMapFeature{
		{ot.NewTag('c', 'a', 'l', 't'), ffGLOBAL},
		{ot.NewTag('c', 'l', 'i', 'g'), ffGLOBAL},
		{ot.NewTag('c', 'u', 'r', 's'), ffGLOBAL},
		{ot.NewTag('d', 'i', 's', 't'), ffGLOBAL},
		{ot.NewTag('k', 'e', 'r', 'n'), ffGlobalHasFallback},
		{ot.NewTag('l', 'i', 'g', 'a'), ffGLOBAL},
		{ot.NewTag('r', 'c', 'l', 't'), ffGLOBAL},
	}
)

func (planner *otShapePlanner) collectFeatures(userFeatures []Feature) {
	map_ := &planner.map_

	map_.enableFeature(ot.NewTag('r', 'v', 'r', 'n'))
	map_.addGSUBPause(nil)

	switch planner.props.Direction {
	case LeftToRight:
		map_.enableFeature(ot.NewTag('l', 't', 'r', 'a'))
		map_.enableFeature(ot.NewTag('l', 't', 'r', 'm'))
	case RightToLeft:
		map_.enableFeature(ot.NewTag('r', 't', 'l', 'a'))
		map_.addFeature(ot.NewTag('r', 't', 'l', 'm'))
	}

	/* Automatic fractions. */
	map_.addFeature(ot.NewTag('f', 'r', 'a', 'c'))
	map_.addFeature(ot.NewTag('n', 'u', 'm', 'r'))
	map_.addFeature(ot.NewTag('d', 'n', 'o', 'm'))

	/* Random! */
	map_.enableFeatureExt(ot.NewTag('r', 'a', 'n', 'd'), ffRandom, otMapMaxValue)

	/* Tracking.  We enable dummy feature here just to allow disabling
	* AAT 'trak' table using features.
	* https://github.com/harfbuzz/harfbuzz/issues/1303 */
	map_.enableFeatureExt(ot.NewTag('t', 'r', 'a', 'k'), ffHasFallback, 1)

	map_.enableFeature(ot.NewTag('H', 'a', 'r', 'f')) /* Considered required. */
	map_.enableFeature(ot.NewTag('H', 'A', 'R', 'F')) /* Considered discretionary. */

	planner.shaper.collectFeatures(planner)

	map_.enableFeature(ot.NewTag('B', 'u', 'z', 'z')) /* Considered required. */
	map_.enableFeature(ot.NewTag('B', 'U', 'Z', 'Z')) /* Considered discretionary. */

	for _, feat := range commonFeatures {
		map_.addFeatureExt(feat.tag, feat.flags, 1)
	}

	if planner.props.Direction.isHorizontal() {
		for _, feat := range horizontalFeatures {
			map_.addFeatureExt(feat.tag, feat.flags, 1)
		}
	} else {
		/* We really want to find a 'vert' feature if there's any in the font, no
		 * matter which script/langsys it is listed (or not) under.
		 * See various bugs referenced from:
		 * https://github.com/harfbuzz/harfbuzz/issues/63 */
		map_.enableFeatureExt(ot.NewTag('v', 'e', 'r', 't'), ffGlobalSearch, 1)
	}

	for _, f := range userFeatures {
		ftag := ffNone
		if f.Start == FeatureGlobalStart && f.End == FeatureGlobalEnd {
			ftag = ffGLOBAL
		}
		map_.addFeatureExt(f.Tag, ftag, f.Value)
	}

	planner.shaper.overrideFeatures(planner)
}

/*
 * shaper
 */

type otContext struct {
	plan         *otShapePlan
	font         *Font
	buffer       *Buffer
	userFeatures []Feature

	// transient stuff
	targetDirection Direction
}

/* Main shaper */

/*
 * Substitute
 */

func vertCharFor(u rune) rune {
	switch u >> 8 {
	case 0x20:
		switch u {
		case 0x2013:
			return 0xfe32 // EN DASH
		case 0x2014:
			return 0xfe31 // EM DASH
		case 0x2025:
			return 0xfe30 // TWO DOT LEADER
		case 0x2026:
			return 0xfe19 // HORIZONTAL ELLIPSIS
		}
	case 0x30:
		switch u {
		case 0x3001:
			return 0xfe11 // IDEOGRAPHIC COMMA
		case 0x3002:
			return 0xfe12 // IDEOGRAPHIC FULL STOP
		case 0x3008:
			return 0xfe3f // LEFT ANGLE BRACKET
		case 0x3009:
			return 0xfe40 // RIGHT ANGLE BRACKET
		case 0x300a:
			return 0xfe3d // LEFT DOUBLE ANGLE BRACKET
		case 0x300b:
			return 0xfe3e // RIGHT DOUBLE ANGLE BRACKET
		case 0x300c:
			return 0xfe41 // LEFT CORNER BRACKET
		case 0x300d:
			return 0xfe42 // RIGHT CORNER BRACKET
		case 0x300e:
			return 0xfe43 // LEFT WHITE CORNER BRACKET
		case 0x300f:
			return 0xfe44 // RIGHT WHITE CORNER BRACKET
		case 0x3010:
			return 0xfe3b // LEFT BLACK LENTICULAR BRACKET
		case 0x3011:
			return 0xfe3c // RIGHT BLACK LENTICULAR BRACKET
		case 0x3014:
			return 0xfe39 // LEFT TORTOISE SHELL BRACKET
		case 0x3015:
			return 0xfe3a // RIGHT TORTOISE SHELL BRACKET
		case 0x3016:
			return 0xfe17 // LEFT WHITE LENTICULAR BRACKET
		case 0x3017:
			return 0xfe18 // RIGHT WHITE LENTICULAR BRACKET
		}
	case 0xfe:
		switch u {
		case 0xfe4f:
			return 0xfe34 // WAVY LOW LINE
		}
	case 0xff:
		switch u {
		case 0xff01:
			return 0xfe15 // FULLWIDTH EXCLAMATION MARK
		case 0xff08:
			return 0xfe35 // FULLWIDTH LEFT PARENTHESIS
		case 0xff09:
			return 0xfe36 // FULLWIDTH RIGHT PARENTHESIS
		case 0xff0c:
			return 0xfe10 // FULLWIDTH COMMA
		case 0xff1a:
			return 0xfe13 // FULLWIDTH COLON
		case 0xff1b:
			return 0xfe14 // FULLWIDTH SEMICOLON
		case 0xff1f:
			return 0xfe16 // FULLWIDTH QUESTION MARK
		case 0xff3b:
			return 0xfe47 // FULLWIDTH LEFT SQUARE BRACKET
		case 0xff3d:
			return 0xfe48 // FULLWIDTH RIGHT SQUARE BRACKET
		case 0xff3f:
			return 0xfe33 // FULLWIDTH LOW LINE
		case 0xff5b:
			return 0xfe37 // FULLWIDTH LEFT CURLY BRACKET
		case 0xff5d:
			return 0xfe38 // FULLWIDTH RIGHT CURLY BRACKET
		}
	}

	return u
}

func (c *otContext) otRotateChars() {
	info := c.buffer.Info

	if c.targetDirection.isBackward() {
		rtlmMask := c.plan.rtlmMask

		for i := range info {
			codepoint := uni.mirroring(info[i].codepoint)
			if codepoint != info[i].codepoint && c.font.hasGlyph(codepoint) {
				info[i].codepoint = codepoint
			} else {
				info[i].Mask |= rtlmMask
			}
		}
	}

	if c.targetDirection.isVertical() && !c.plan.hasVert {
		for i := range info {
			codepoint := vertCharFor(info[i].codepoint)
			if codepoint != info[i].codepoint && c.font.hasGlyph(codepoint) {
				info[i].codepoint = codepoint
			}
		}
	}
}

func (c *otContext) setupMasksFraction() {
	if c.buffer.scratchFlags&bsfHasNonASCII == 0 || !c.plan.hasFrac {
		return
	}

	buffer := c.buffer

	var preMask, postMask GlyphMask
	if buffer.Props.Direction.isForward() {
		preMask = c.plan.numrMask | c.plan.fracMask
		postMask = c.plan.fracMask | c.plan.dnomMask
	} else {
		preMask = c.plan.fracMask | c.plan.dnomMask
		postMask = c.plan.numrMask | c.plan.fracMask
	}

	count := len(buffer.Info)
	info := buffer.Info
	for i := 0; i < count; i++ {
		if info[i].codepoint == 0x2044 /* FRACTION SLASH */ {
			start, end := i, i+1
			for start != 0 && info[start-1].unicode.generalCategory() == decimalNumber {
				start--
			}
			for end < count && info[end].unicode.generalCategory() == decimalNumber {
				end++
			}

			buffer.unsafeToBreak(start, end)

			for j := start; j < i; j++ {
				info[j].Mask |= preMask
			}
			info[i].Mask |= c.plan.fracMask
			for j := i + 1; j < end; j++ {
				info[j].Mask |= postMask
			}

			i = end - 1
		}
	}
}

func (c *otContext) initializeMasks() {
	c.buffer.resetMasks(c.plan.map_.globalMask)
}

func (c *otContext) setupMasks() {
	map_ := &c.plan.map_
	buffer := c.buffer

	c.setupMasksFraction()

	c.plan.shaper.setupMasks(c.plan, buffer, c.font)

	for _, feature := range c.userFeatures {
		if !(feature.Start == FeatureGlobalStart && feature.End == FeatureGlobalEnd) {
			mask, shift := map_.getMask(feature.Tag)
			buffer.setMasks(feature.Value<<shift, mask, feature.Start, feature.End)
		}
	}
}

func zeroWidthDefaultIgnorables(buffer *Buffer) {
	if buffer.scratchFlags&bsfHasDefaultIgnorables == 0 ||
		buffer.Flags&PreserveDefaultIgnorables != 0 ||
		buffer.Flags&RemoveDefaultIgnorables != 0 {
		return
	}

	pos := buffer.Pos
	for i, info := range buffer.Info {
		if info.isDefaultIgnorable() {
			pos[i].XAdvance, pos[i].YAdvance, pos[i].XOffset, pos[i].YOffset = 0, 0, 0, 0
		}
	}
}

func hideDefaultIgnorables(buffer *Buffer, font *Font) {
	if buffer.scratchFlags&bsfHasDefaultIgnorables == 0 ||
		buffer.Flags&PreserveDefaultIgnorables != 0 {
		return
	}

	info := buffer.Info

	var (
		invisible = buffer.Invisible
		ok        bool
	)
	if invisible == 0 {
		invisible, ok = font.face.NominalGlyph(' ')
	}
	if buffer.Flags&RemoveDefaultIgnorables == 0 && ok {
		// replace default-ignorables with a zero-advance invisible glyph.
		for i := range info {
			if info[i].isDefaultIgnorable() {
				info[i].Glyph = invisible
			}
		}
	} else {
		otLayoutDeleteGlyphsInplace(buffer, (*GlyphInfo).isDefaultIgnorable)
	}
}

// use unicodeProp to assign a class
func synthesizeGlyphClasses(buffer *Buffer) {
	info := buffer.Info
	for i := range info {
		/* Never mark default-ignorables as marks.
		 * They won't get in the way of lookups anyway,
		 * but having them as mark will cause them to be skipped
		 * over if the lookup-flag says so, but at least for the
		 * Mongolian variation selectors, looks like Uniscribe
		 * marks them as non-mark.  Some Mongolian fonts without
		 * GDEF rely on this.  Another notable character that
		 * this applies to is COMBINING GRAPHEME JOINER. */
		class := tables.GPMark
		if info[i].unicode.generalCategory() != nonSpacingMark || info[i].isDefaultIgnorable() {
			class = tables.GPBaseGlyph
		}

		info[i].glyphProps = class
	}
}

func (c *otContext) substituteBeforePosition() {
	buffer := c.buffer

	// substituteDefault : normalize and sets Glyph
	c.otRotateChars()

	otShapeNormalize(c.plan, buffer, c.font)

	c.setupMasks()

	// this is unfortunate to go here, but necessary...
	if c.plan.fallbackMarkPositioning {
		fallbackMarkPositionRecategorizeMarks(buffer)
	}

	if debugMode {
		fmt.Println("BEFORE SUBSTITUTE:", c.buffer.Info)
	}

	// substitutePan : glyph fields are now set up ...
	// ... apply complex substitution from font

	layoutSubstituteStart(c.font, buffer)

	if c.plan.fallbackGlyphClasses {
		synthesizeGlyphClasses(c.buffer)
	}

	if c.plan.applyMorx {
		c.plan.aatLayoutSubstitute(c.font, c.buffer, c.userFeatures)
	}

	c.plan.substitute(c.font, buffer)

	//
	if c.plan.applyMorx && c.plan.applyGpos {
		aatLayoutRemoveDeletedGlyphs(buffer)
	}
}

func (c *otContext) substituteAfterPosition() {
	if c.plan.applyMorx && !c.plan.applyGpos {
		aatLayoutRemoveDeletedGlyphs(c.buffer)
	}

	hideDefaultIgnorables(c.buffer, c.font)

	if debugMode {
		fmt.Println("POSTPROCESS glyphs start")
	}
	c.plan.shaper.postprocessGlyphs(c.plan, c.buffer, c.font)
	if debugMode {
		fmt.Println("POSTPROCESS glyphs end ")
	}
}

/*
 * Position
 */

func zeroMarkWidthsByGdef(buffer *Buffer, adjustOffsets bool) {
	for i, inf := range buffer.Info {
		if inf.isMark() {
			pos := &buffer.Pos[i]
			if adjustOffsets { // adjustMarkOffsets
				pos.XOffset -= pos.XAdvance
				pos.YOffset -= pos.YAdvance
			}
			// zeroMarkWidth
			pos.XAdvance = 0
			pos.YAdvance = 0
		}
	}
}

// override Pos array with default values
func (c *otContext) positionDefault() {
	direction := c.buffer.Props.Direction
	info := c.buffer.Info
	pos := c.buffer.Pos

	if direction.isHorizontal() {
		for i, inf := range info {
			pos[i].XAdvance, pos[i].YAdvance = c.font.GlyphHAdvance(inf.Glyph), 0
			pos[i].XOffset, pos[i].YOffset = c.font.subtractGlyphHOrigin(inf.Glyph, 0, 0)
		}
	} else {
		for i, inf := range info {
			pos[i].XAdvance, pos[i].YAdvance = 0, c.font.getGlyphVAdvance(inf.Glyph)
			pos[i].XOffset, pos[i].YOffset = c.font.subtractGlyphVOrigin(inf.Glyph, 0, 0)
		}
	}
	if c.buffer.scratchFlags&bsfHasSpaceFallback != 0 {
		fallbackSpaces(c.font, c.buffer)
	}
}

func (c *otContext) positionComplex() {
	info := c.buffer.Info
	pos := c.buffer.Pos

	/* If the font has no GPOS and direction is forward, then when
	* zeroing mark widths, we shift the mark with it, such that the
	* mark is positioned hanging over the previous glyph.  When
	* direction is backward we don't shift and it will end up
	* hanging over the next glyph after the final reordering.
	*
	* Note: If fallback positioning happens, we don't care about
	* this as it will be overridden. */
	adjustOffsetsWhenZeroing := c.plan.adjustMarkPositioningWhenZeroing && c.buffer.Props.Direction.isForward()

	// we change glyph origin to what GPOS expects (horizontal), apply GPOS, change it back.

	for i, inf := range info {
		pos[i].XOffset, pos[i].YOffset = c.font.addGlyphHOrigin(inf.Glyph, pos[i].XOffset, pos[i].YOffset)
	}

	otLayoutPositionStart(c.font, c.buffer)
	markBehavior, _ := c.plan.shaper.marksBehavior()

	if c.plan.zeroMarks {
		if markBehavior == zeroWidthMarksByGdefEarly {
			zeroMarkWidthsByGdef(c.buffer, adjustOffsetsWhenZeroing)
		}
	}

	c.plan.position(c.font, c.buffer) // apply GPOS, AAT

	if c.plan.zeroMarks {
		if markBehavior == zeroWidthMarksByGdefLate {
			zeroMarkWidthsByGdef(c.buffer, adjustOffsetsWhenZeroing)
		}
	}

	// finish off. Has to follow a certain order.
	zeroWidthDefaultIgnorables(c.buffer)
	if c.plan.applyMorx {
		aatLayoutZeroWidthDeletedGlyphs(c.buffer)
	}
	otLayoutPositionFinishOffsets(c.font, c.buffer)

	for i, inf := range info {
		pos[i].XOffset, pos[i].YOffset = c.font.subtractGlyphHOrigin(inf.Glyph, pos[i].XOffset, pos[i].YOffset)
	}

	if c.plan.fallbackMarkPositioning {
		fallbackMarkPosition(c.plan, c.font, c.buffer, adjustOffsetsWhenZeroing)
	}
}

func (c *otContext) position() {
	c.buffer.clearPositions()

	c.positionDefault()

	if debugMode {
		fmt.Println("AFTER DEFAULT POSITION", c.buffer.Pos)
	}

	c.positionComplex()

	if c.buffer.Props.Direction.isBackward() {
		c.buffer.Reverse()
	}
}

/* Propagate cluster-level glyph flags to be the same on all cluster glyphs.
 * Simplifies using them. */
func propagateFlags(buffer *Buffer) {
	if buffer.scratchFlags&bsfHasGlyphFlags == 0 {
		return
	}

	/* If we are producing SAFE_TO_INSERT_TATWEEL, then do two things:
	 *
	 * - If the places that the Arabic shaper marked as SAFE_TO_INSERT_TATWEEL,
	 *   are UNSAFE_TO_BREAK, then clear the SAFE_TO_INSERT_TATWEEL,
	 * - Any place that is SAFE_TO_INSERT_TATWEEL, is also now UNSAFE_TO_BREAK.
	 *
	 * We couldn't make this interaction earlier. It has to be done here.
	 */
	flipTatweel := buffer.Flags&ProduceSafeToInsertTatweel != 0

	clearConcat := (buffer.Flags & ProduceUnsafeToConcat) == 0

	info := buffer.Info

	iter, count := buffer.clusterIterator()
	for start, end := iter.next(); start < count; start, end = iter.next() {
		var mask uint32
		for i := start; i < end; i++ {
			mask |= info[i].Mask & glyphFlagDefined
		}

		if flipTatweel {
			if mask&GlyphUnsafeToBreak != 0 {
				mask &= ^GlyphSafeToInsertTatweel
			}
			if mask&GlyphSafeToInsertTatweel != 0 {
				mask |= GlyphUnsafeToBreak | GlyphUnsafeToConcat
			}

		}

		if clearConcat {
			mask &= ^GlyphUnsafeToConcat
		}

		for i := start; i < end; i++ {
			info[i].Mask = mask
		}
	}
}

// shaperOpentype is the main shaper of this library.
// It handles complex language and Opentype layout features found in fonts.
type shaperOpentype struct {
	tables *font.Font
	plan   otShapePlan
	key    otShapePlanKey
}

type otShapePlanKey = [2]int // -1 for not found

func (sp *shaperOpentype) init(tables *font.Font, coords []tables.Coord) {
	sp.plan = otShapePlan{}
	sp.key = otShapePlanKey{
		0: tables.GSUB.FindVariationIndex(coords),
		1: tables.GPOS.FindVariationIndex(coords),
	}
	sp.tables = tables
}

func (sp *shaperOpentype) compile(props SegmentProperties, userFeatures []Feature) {
	sp.plan.init0(sp.tables, props, userFeatures, sp.key)
}

// pull it all together!
func (sp *shaperOpentype) shape(font *Font, buffer *Buffer, features []Feature) {
	c := otContext{plan: &sp.plan, font: font, buffer: buffer, userFeatures: features}
	c.buffer.scratchFlags = bsfDefault

	const maxLenFactor = 64
	const maxLenMin = 16384
	const maxOpsFactor = 1024
	const maxOpsMin = 16384
	c.buffer.maxOps = max(len(c.buffer.Info)*maxOpsFactor, maxOpsMin)
	c.buffer.maxLen = max(len(c.buffer.Info)*maxLenFactor, maxLenMin)

	// save the original direction, we use it later.
	c.targetDirection = c.buffer.Props.Direction

	c.initializeMasks()
	c.buffer.setUnicodeProps()
	c.buffer.insertDottedCircle(c.font)

	c.buffer.formClusters()

	if debugMode {
		fmt.Println("FORMING CLUSTER :", c.buffer.Info)
	}

	c.buffer.ensureNativeDirection()

	if debugMode {
		fmt.Printf("PREPROCESS text start\n")
	}
	c.plan.shaper.preprocessText(c.plan, c.buffer, c.font)
	if debugMode {
		fmt.Println("PREPROCESS text end:", c.buffer.Info)
	}

	c.substituteBeforePosition() // apply GSUB

	if debugMode {
		fmt.Println("AFTER SUBSTITUTE", c.buffer.Info)
	}

	c.position()

	if debugMode {
		fmt.Println("AFTER POSITION", c.buffer.Pos)
	}

	c.substituteAfterPosition()

	propagateFlags(c.buffer)

	c.buffer.Props.Direction = c.targetDirection

	c.buffer.maxOps = maxOpsDefault
}
