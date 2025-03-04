package harfbuzz

import (
	"fmt"

	"github.com/boxesandglue/typesetting/font/opentype/tables"
)

// Position stores a position, scaled according to the `Font`
// scale parameters.
type Position = int32

// GlyphPosition holds the positions of the
// glyph in both horizontal and vertical directions.
// All positions are relative to the current point.
type GlyphPosition struct {
	// How much the line advances after drawing this glyph when setting
	// text in horizontal direction.
	XAdvance Position
	// How much the glyph moves on the X-axis before drawing it, this
	// should not affect how much the line advances.
	XOffset Position

	// How much the line advances after drawing this glyph when setting
	// text in vertical direction.
	YAdvance Position
	// How much the glyph moves on the Y-axis before drawing it, this
	// should not affect how much the line advances.
	YOffset Position

	// glyph to which this attaches to, relative to current glyphs;
	// negative for going back, positive for forward.
	attachChain int16
	attachType  uint8 // attachment type, irrelevant if attachChain is 0
}

// unicodeProp is a two-byte number. The low byte includes:
//   - General_Category: 5 bits
//   - A bit each for:
//     -> Is it Default_Ignorable(); we have a modified Default_Ignorable().
//     -> Whether it's one of the four Mongolian Free Variation Selectors,
//     CGJ, or other characters that are hidden but should not be ignored
//     like most other Default_Ignorable()s do during matching.
//     -> Whether it's a grapheme continuation.
//
// The high-byte has different meanings, switched by the General_Category:
//   - For Mn,Mc,Me: the modified Combining_Class.
//   - For Cf: whether it's ZWJ, ZWNJ, or something else.
//   - For Ws: index of which space character this is, if space fallback
//     is needed, ie. we don't set this by default, only if asked to.
type unicodeProp uint16

const (
	upropsMaskIgnorable unicodeProp = 1 << (5 + iota)
	upropsMaskHidden                // MONGOLIAN FREE VARIATION SELECTOR 1..4, or TAG characters
	upropsMaskContinuation

	// if GEN_CAT=FORMAT, top byte masks
	upropsMaskCfZwj
	upropsMaskCfZwnj

	upropsMaskGenCat unicodeProp = 1<<5 - 1 // 11111
)

const isLigBase = 0x10

// generalCategory extracts the general category.
func (prop unicodeProp) generalCategory() generalCategory {
	return generalCategory(prop & upropsMaskGenCat)
}

type GlyphMask = uint32

const (
	// Indicates that if input text is broken at the beginning of the cluster this glyph is part of,
	// then both sides need to be re-shaped, as the result might be different.
	// On the flip side, it means that when this flag is not present,
	// then it's safe to break the glyph-run at the beginning of this cluster,
	// and the two sides represent the exact same result one would get
	// if breaking input text at the beginning of this cluster and shaping the two sides
	// separately.
	// This can be used to optimize paragraph layout, by avoiding re-shaping
	// of each line after line-breaking.
	GlyphUnsafeToBreak GlyphMask = 1 << iota

	// Indicates that if input text is changed on one side of the beginning of the cluster this glyph
	// is part of, then the shaping results for the other side might change.
	// Note that the absence of this flag will NOT by itself mean that it IS safe to concat text.
	// Only two pieces of text both of which clear of this flag can be concatenated safely.
	// This can be used to optimize paragraph layout, by avoiding re-shaping of each line
	// after line-breaking, by limiting the reshaping to a small piece around the
	// breaking positin only, even if the breaking position carries the
	// [GlyphUnsafeToBreak] or when hyphenation or other text transformation
	// happens at line-break position, in the following way:
	// 	1. Iterate back from the line-break position until the first cluster start position that is
	// 		NOT unsafe-to-concat,
	// 	2. Shape the segment from there till the end of line,
	// 	3. Check whether the resulting glyph-run also is clear of the unsafe-to-concat at its start-of-text position;
	// 		if it is, just splice it into place and the line is shaped; If not, move on to a position further
	// 		back that is clear of unsafe-to-concat and retry from there, and repeat.
	// At the start of next line a similar algorithm can be implemented. That is:
	// 	1. Iterate forward from the line-break position until the first cluster start position that is NOT unsafe-to-concat,
	// 	2. Shape the segment from beginning of the line to that position,
	// 	3. Check whether the resulting glyph-run also is clear of the unsafe-to-concat at its end-of-text position;
	// 		if it is, just splice it into place and the beginning is shaped; If not, move on to a position further forward that is clear
	// 	 	of unsafe-to-concat and retry up to there, and repeat.
	// A slight complication will arise in the implementation of the algorithm above, because while our buffer API has a way to
	// return flags for position corresponding to start-of-text, there is currently no position
	// corresponding to end-of-text.  This limitation can be alleviated by shaping more text than needed
	// and looking for unsafe-to-concat flag within text clusters.
	// The [GlyphUnsafeToBreak] flag will always imply this flag.
	// To use this flag, you must enable the buffer flag [ProduceUnsafeToConcat] during
	// shaping, otherwise the buffer flag will not be reliably produced.
	GlyphUnsafeToConcat

	// In scripts that use elongation (Arabic, Mongolian, Syriac, etc.), this flag signifies
	// that it is safe to insert a U+0640 TATWEEL character before this cluster for elongation.
	// This flag does not determine the script-specific elongation places, but only
	// when it is safe to do the elongation without interrupting text shaping.
	GlyphSafeToInsertTatweel

	// OR of all defined flags
	glyphFlagDefined GlyphMask = GlyphUnsafeToBreak | GlyphUnsafeToConcat | GlyphSafeToInsertTatweel
)

// GlyphInfo holds information about the
// glyphs and their relation to input text.
// They are internally created from user input,
// and the shapping sets the `Glyph` field.
type GlyphInfo struct {
	// Cluster is the index of the character in the original text that corresponds
	// to this `GlyphInfo`, or whatever the client passes to `Buffer.Add()`.
	// More than one glyph can have the same `Cluster` value,
	// if they resulted from the same character (e.g. one to many glyph substitution),
	// and when more than one character gets merged in the same glyph (e.g. many to one glyph substitution)
	// the glyph will have the smallest Cluster value of them.
	// By default some characters are merged into the same Cluster
	// (e.g. combining marks have the same Cluster as their bases)
	// even if they are separate glyphs.
	// See Buffer.ClusterLevel for more fine-grained Cluster handling.
	Cluster int

	// input value of the shapping
	codepoint rune

	// Glyph is the result of the selection of concrete glyph
	// after shaping, and refers to the font used.
	Glyph GID

	// Mask exposes glyph attributes (see the constants).
	// It is also used internally during the shaping.
	Mask GlyphMask

	// in C code: var1

	// GDEF glyph properties
	glyphProps uint16

	// GSUB/GPOS ligature tracking
	// When a ligature is formed:
	//
	//   - The ligature glyph and any marks in between all the same newly allocated
	//     lig_id,
	//   - The ligature glyph will get lig_num_comps set to the number of components
	//   - The marks get lig_comp > 0, reflecting which component of the ligature
	//     they were applied to.
	//   - This is used in GPOS to attach marks to the right component of a ligature
	//     in MarkLigPos,
	//   - Note that when marks are ligated together, much of the above is skipped
	//     and the current lig_id reused.
	//
	// When a multiple-substitution is done:
	//
	//   - All resulting glyphs will have lig_id = 0,
	//   - The resulting glyphs will have lig_comp = 0, 1, 2, ... respectively.
	//   - This is used in GPOS to attach marks to the first component of a
	//     multiple substitution in MarkBasePos.
	//
	// The numbers are also used in GPOS to do mark-to-mark positioning only
	// to marks that belong to the same component of the same ligature.
	ligProps uint8
	// GSUB/GPOS shaping boundaries
	syllable uint8

	// in C code: var2

	unicode unicodeProp

	complexCategory, complexAux uint8 // storage interpreted by complex shapers
}

// String returns a simple description of the glyph of the form Glyph=Cluster(mask)
func (info GlyphInfo) String() string {
	return fmt.Sprintf("%d=%d(0x%x)", info.Glyph, info.Cluster, info.Mask&glyphFlagDefined)
}

func (info *GlyphInfo) setUnicodeProps(buffer *Buffer) {
	u := info.codepoint
	var flags bufferScratchFlags
	info.unicode, flags = computeUnicodeProps(u)
	buffer.scratchFlags |= flags
}

func (info *GlyphInfo) setGeneralCategory(genCat generalCategory) {
	/* Clears top-byte. */
	info.unicode = unicodeProp(genCat) | (info.unicode & (0xFF & ^upropsMaskGenCat))
}

func (info *GlyphInfo) setCluster(cluster int, mask GlyphMask) {
	if info.Cluster != cluster {
		info.Mask = (info.Mask & ^glyphFlagDefined) | (mask & glyphFlagDefined)
	}
	info.Cluster = cluster
}

func (info *GlyphInfo) setContinuation() {
	info.unicode |= upropsMaskContinuation
}

func (info *GlyphInfo) isContinuation() bool {
	return info.unicode&upropsMaskContinuation != 0
}

func (info *GlyphInfo) resetContinutation() { info.unicode &= ^upropsMaskContinuation }

func (info *GlyphInfo) isUnicodeSpace() bool {
	return info.unicode.generalCategory() == spaceSeparator
}

func (info *GlyphInfo) isUnicodeFormat() bool {
	return info.unicode.generalCategory() == format
}

func (info *GlyphInfo) isZwnj() bool {
	return info.isUnicodeFormat() && (info.unicode&upropsMaskCfZwnj) != 0
}

func (info *GlyphInfo) isZwj() bool {
	return info.isUnicodeFormat() && (info.unicode&upropsMaskCfZwj) != 0
}

func (info *GlyphInfo) isUnicodeMark() bool {
	return (info.unicode & upropsMaskGenCat).generalCategory().isMark()
}

func (info *GlyphInfo) setUnicodeSpaceFallbackType(s uint8) {
	if !info.isUnicodeSpace() {
		return
	}
	info.unicode = unicodeProp(s)<<8 | info.unicode&0xFF
}

func (info *GlyphInfo) getModifiedCombiningClass() uint8 {
	if info.isUnicodeMark() {
		return uint8(info.unicode >> 8)
	}
	return 0
}

func (info *GlyphInfo) unhide() {
	info.unicode &= ^upropsMaskHidden
}

func (info *GlyphInfo) setModifiedCombiningClass(modifiedClass uint8) {
	if !info.isUnicodeMark() {
		return
	}
	info.unicode = (unicodeProp(modifiedClass) << 8) | (info.unicode & 0xFF)
}

func (info *GlyphInfo) ligated() bool {
	return info.glyphProps&ligated != 0
}

func (info *GlyphInfo) getLigID() uint8 {
	return info.ligProps >> 5
}

func (info *GlyphInfo) ligatedInternal() bool {
	return info.ligProps&isLigBase != 0
}

func (info *GlyphInfo) getLigComp() uint8 {
	if info.ligatedInternal() {
		return 0
	}
	return info.ligProps & 0x0F
}

func (info *GlyphInfo) getLigNumComps() uint8 {
	if (info.glyphProps&tables.GPLigature) != 0 && info.ligatedInternal() {
		return info.ligProps & 0x0F
	}
	return 1
}

func (info *GlyphInfo) setLigPropsForMark(ligID, ligComp uint8) {
	info.ligProps = (ligID << 5) | ligComp&0x0F
}

func (info *GlyphInfo) setLigPropsForLigature(ligID, ligNumComps uint8) {
	info.ligProps = (ligID << 5) | isLigBase | ligNumComps&0x0F
}

func (info *GlyphInfo) isDefaultIgnorable() bool {
	return (info.unicode&upropsMaskIgnorable) != 0 && !info.substituted()
}

func (info *GlyphInfo) isDefaultIgnorableAndNotHidden() bool {
	return (info.unicode&(upropsMaskIgnorable|upropsMaskHidden) == upropsMaskIgnorable) &&
		!info.substituted()
}

func (info *GlyphInfo) getUnicodeSpaceFallbackType() uint8 {
	if info.isUnicodeSpace() {
		return uint8(info.unicode >> 8)
	}
	return notSpace
}

func (info *GlyphInfo) isMark() bool {
	return info.glyphProps&tables.GPMark != 0
}

func (info *GlyphInfo) isBaseGlyph() bool {
	return info.glyphProps&tables.GPBaseGlyph != 0
}

func (info *GlyphInfo) isLigature() bool {
	return info.glyphProps&tables.GPLigature != 0
}

func (info *GlyphInfo) multiplied() bool {
	return info.glyphProps&multiplied != 0
}

func (info *GlyphInfo) clearLigatedAndMultiplied() {
	info.glyphProps &= ^(ligated | multiplied)
}

func (info *GlyphInfo) ligatedAndDidntMultiply() bool {
	return info.ligated() && !info.multiplied()
}

func (info *GlyphInfo) substituted() bool {
	return info.glyphProps&substituted != 0
}
