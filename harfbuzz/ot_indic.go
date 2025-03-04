package harfbuzz

import (
	"fmt"
	"sort"

	ot "github.com/boxesandglue/typesetting/font/opentype"
	"github.com/boxesandglue/typesetting/font/opentype/tables"
	"github.com/boxesandglue/typesetting/language"
)

// ported from harfbuzz/src/hb-ot-shape-complex-indic.cc, .hh Copyright © 2011,2012  Google, Inc.  Behdad Esfahbod

// UniscribeBugCompatible alters shaping of indic and khmer scripts:
//   - when `false`, it applies the recommended shaping choices
//   - when `true`, Uniscribe behavior is reproduced
var UniscribeBugCompatible = false

// Keep in sync with the code generator.
const (
	posStart = iota

	posRaToBecomeReph
	posPreM
	posPreC

	posBaseC
	posAfterMain

	posAboveC

	posBeforeSub
	posBelowC
	posAfterSub

	posBeforePost
	posPostC
	posAfterPost

	posSmvd

	posEnd
)

var _ otComplexShaper = (*complexShaperIndic)(nil)

// Indic shaper.
type complexShaperIndic struct {
	complexShaperNil

	plan indicShapePlan
}

/* Note:
 *
 * We treat Vowels and placeholders as if they were consonants.  This is safe because Vowels
 * cannot happen in a consonant syllable.  The plus side however is, we can call the
 * consonant syllable logic from the vowel syllable function and get it all right! */
const (
	consonantFlags = 1<<indSM_ex_C | 1<<indSM_ex_CS |
		1<<indSM_ex_Ra | 1<<indSM_ex_CM | 1<<indSM_ex_V |
		1<<indSM_ex_PLACEHOLDER | 1<<indSM_ex_DOTTEDCIRCLE
	joinerFlags = 1<<indSM_ex_ZWJ | 1<<indSM_ex_ZWNJ
)

func isOneOf(info *GlyphInfo, flags uint32) bool {
	/* If it ligated, all bets are off. */
	if info.ligated() {
		return false
	}
	return 1<<info.complexCategory&flags != 0
}

func isJoiner(info *GlyphInfo) bool {
	return isOneOf(info, joinerFlags)
}

func isConsonant(info *GlyphInfo) bool {
	return isOneOf(info, consonantFlags)
}

func isHalant(info *GlyphInfo) bool {
	return isOneOf(info, 1<<indSM_ex_H)
}

func (info *GlyphInfo) setIndicProperties() {
	u := info.codepoint
	type_ := indicGetCategories(u)
	info.complexCategory, info.complexAux = uint8(type_&0xFF), uint8(type_>>8)
}

type indicWouldSubstituteFeature struct {
	lookups []lookupMap
	//   count int
	zeroContext bool
}

func newIndicWouldSubstituteFeature(map_ *otMap, featureTag tables.Tag, zeroContext bool) indicWouldSubstituteFeature {
	var out indicWouldSubstituteFeature
	out.zeroContext = zeroContext
	out.lookups = map_.getStageLookups(0 /*GSUB*/, map_.getFeatureStage(0 /*GSUB*/, featureTag))
	return out
}

func (ws indicWouldSubstituteFeature) wouldSubstitute(glyphs []GID, font *Font) bool {
	for _, lk := range ws.lookups {
		if otLayoutLookupWouldSubstitute(font, lk.index, glyphs, ws.zeroContext) {
			return true
		}
	}
	return false
}

/*
 * Indic configurations.  Note that we do not want to keep every single script-specific
 * behavior in these tables necessarily.  This should mainly be used for per-script
 * properties that are cheaper keeping here, than in the code.  Ie. if, say, one and
 * only one script has an exception, that one script can be if'ed directly in the code,
 * instead of adding a new flag in these structs.
 */

// reph_position_t
const (
	rephPosAfterMain  = posAfterMain
	rephPosBeforeSub  = posBeforeSub
	rephPosAfterSub   = posAfterSub
	rephPosBeforePost = posBeforePost
	rephPosAfterPost  = posAfterPost
)

// reph_mode_t
const (
	rephModeImplicit = iota /* Reph formed out of initial Ra,H sequence. */
	rephModeExplicit        /* Reph formed out of initial Ra,H,ZWJ sequence. */
	rephModeLogRepha        /* Encoded Repha character, needs reordering. */
)

// blwf_mode_t
const (
	blwfModePreAndPost = iota /* Below-forms feature applied to pre-base and post-base. */
	blwfModePostOnly          /* Below-forms feature applied to post-base only. */
)

type indicConfig struct {
	script     language.Script
	hasOldSpec bool
	virama     rune
	rephPos    uint8
	rephMode   uint8
	blwfMode   uint8
}

var indicConfigs = [...]indicConfig{
	/* Default.  Should be first. */
	{0, false, 0, rephPosBeforePost, rephModeImplicit, blwfModePreAndPost},
	{language.Devanagari, true, 0x094D, rephPosBeforePost, rephModeImplicit, blwfModePreAndPost},
	{language.Bengali, true, 0x09CD, rephPosAfterSub, rephModeImplicit, blwfModePreAndPost},
	{language.Gurmukhi, true, 0x0A4D, rephPosBeforeSub, rephModeImplicit, blwfModePreAndPost},
	{language.Gujarati, true, 0x0ACD, rephPosBeforePost, rephModeImplicit, blwfModePreAndPost},
	{language.Oriya, true, 0x0B4D, rephPosAfterMain, rephModeImplicit, blwfModePreAndPost},
	{language.Tamil, true, 0x0BCD, rephPosAfterPost, rephModeImplicit, blwfModePreAndPost},
	{language.Telugu, true, 0x0C4D, rephPosAfterPost, rephModeExplicit, blwfModePostOnly},
	{language.Kannada, true, 0x0CCD, rephPosAfterPost, rephModeImplicit, blwfModePostOnly},
	{language.Malayalam, true, 0x0D4D, rephPosAfterMain, rephModeLogRepha, blwfModePreAndPost},
}

var indicFeatures = [...]otMapFeature{
	/*
	* Basic features.
	* These features are applied in order, one at a time, after initial_reordering.
	 */
	{ot.NewTag('n', 'u', 'k', 't'), ffGlobalManualJoiners | ffPerSyllable},
	{ot.NewTag('a', 'k', 'h', 'n'), ffGlobalManualJoiners | ffPerSyllable},
	{ot.NewTag('r', 'p', 'h', 'f'), ffManualJoiners | ffPerSyllable},
	{ot.NewTag('r', 'k', 'r', 'f'), ffGlobalManualJoiners | ffPerSyllable},
	{ot.NewTag('p', 'r', 'e', 'f'), ffManualJoiners | ffPerSyllable},
	{ot.NewTag('b', 'l', 'w', 'f'), ffManualJoiners | ffPerSyllable},
	{ot.NewTag('a', 'b', 'v', 'f'), ffManualJoiners | ffPerSyllable},
	{ot.NewTag('h', 'a', 'l', 'f'), ffManualJoiners | ffPerSyllable},
	{ot.NewTag('p', 's', 't', 'f'), ffManualJoiners | ffPerSyllable},
	{ot.NewTag('v', 'a', 't', 'u'), ffGlobalManualJoiners | ffPerSyllable},
	{ot.NewTag('c', 'j', 'c', 't'), ffGlobalManualJoiners | ffPerSyllable},
	/*
	* Other features.
	* These features are applied all at once, after final_reordering
	* but before clearing syllables.
	* Default Bengali font in Windows for example has intermixed
	* lookups for init,pres,abvs,blws features.
	 */
	{ot.NewTag('i', 'n', 'i', 't'), ffManualJoiners | ffPerSyllable},
	{ot.NewTag('p', 'r', 'e', 's'), ffGlobalManualJoiners | ffPerSyllable},
	{ot.NewTag('a', 'b', 'v', 's'), ffGlobalManualJoiners | ffPerSyllable},
	{ot.NewTag('b', 'l', 'w', 's'), ffGlobalManualJoiners | ffPerSyllable},
	{ot.NewTag('p', 's', 't', 's'), ffGlobalManualJoiners | ffPerSyllable},
	{ot.NewTag('h', 'a', 'l', 'n'), ffGlobalManualJoiners | ffPerSyllable},
}

// in the same order as the indicFeatures array
const (
	indicNukt = iota
	indicAkhn
	indicRphf
	indicRkrf
	indicPref
	indicBlwf
	indicAbvf
	indicHalf
	indicPstf
	indicVatu
	indicCjct

	indicInit
	indicPres
	indicAbvs
	indicBlws
	indicPsts
	indicHaln

	indicNumFeatures
	indicBasicFeatures = indicInit /* Don't forget to update this! */
)

func (cs *complexShaperIndic) collectFeatures(plan *otShapePlanner) {
	map_ := &plan.map_

	/* Do this before any lookups have been applied. */
	map_.addGSUBPause(setupSyllablesIndic)

	map_.enableFeatureExt(ot.NewTag('l', 'o', 'c', 'l'), ffPerSyllable, 1)
	/* The Indic specs do not require ccmp, but we apply it here since if
	* there is a use of it, it's typically at the beginning. */
	map_.enableFeatureExt(ot.NewTag('c', 'c', 'm', 'p'), ffPerSyllable, 1)

	i := 0
	map_.addGSUBPause(cs.initialReorderingIndic)

	for ; i < indicBasicFeatures; i++ {
		map_.addFeatureExt(indicFeatures[i].tag, indicFeatures[i].flags, 1)
		map_.addGSUBPause(nil)
	}

	map_.addGSUBPause(cs.plan.finalReorderingIndic)

	for ; i < indicNumFeatures; i++ {
		map_.addFeatureExt(indicFeatures[i].tag, indicFeatures[i].flags, 1)
	}
}

func (complexShaperIndic) overrideFeatures(plan *otShapePlanner) {
	plan.map_.disableFeature(ot.NewTag('l', 'i', 'g', 'a'))
	plan.map_.addGSUBPause(nil)
}

type indicShapePlan struct {
	blwf indicWouldSubstituteFeature
	pstf indicWouldSubstituteFeature
	vatu indicWouldSubstituteFeature
	rphf indicWouldSubstituteFeature
	pref indicWouldSubstituteFeature

	maskArray   [indicNumFeatures]GlyphMask
	config      indicConfig
	viramaGlyph GID // cached value

	isOldSpec              bool
	uniscribeBugCompatible bool
}

func (indicPlan *indicShapePlan) loadViramaGlyph(font *Font) GID {
	if indicPlan.viramaGlyph == ^GID(0) {
		glyph, ok := font.face.NominalGlyph(indicPlan.config.virama)
		if indicPlan.config.virama == 0 || !ok {
			glyph = 0
		}
		/* Technically speaking, the spec says we should apply 'locl' to virama too.
		* Maybe one day... */

		/* Our get_nominal_glyph() function needs a font, so we can't get the virama glyph
		* during shape planning...  Instead, overwrite it here. */
		indicPlan.viramaGlyph = glyph
	}

	return indicPlan.viramaGlyph
}

func (cs *complexShaperIndic) dataCreate(plan *otShapePlan) {
	var indicPlan indicShapePlan

	indicPlan.config = indicConfigs[0]
	for i := 1; i < len(indicConfigs); i++ {
		if plan.props.Script == indicConfigs[i].script {
			indicPlan.config = indicConfigs[i]
			break
		}
	}

	indicPlan.isOldSpec = indicPlan.config.hasOldSpec && ((plan.map_.chosenScript[0] & 0x000000FF) != '2')
	indicPlan.uniscribeBugCompatible = UniscribeBugCompatible
	indicPlan.viramaGlyph = ^GID(0)

	/* Use zero-context wouldSubstitute() matching for new-spec of the main
	* Indic scripts, and scripts with one spec only, but not for old-specs.
	* The new-spec for all dual-spec scripts says zero-context matching happens.
	*
	* However, testing with Malayalam shows that old and new spec both allow
	* context.  Testing with Bengali new-spec however shows that it doesn't.
	* So, the heuristic here is the way it is.  It should *only* be changed,
	* as we discover more cases of what Windows does.  DON'T TOUCH OTHERWISE. */
	zeroContext := !indicPlan.isOldSpec && plan.props.Script != language.Malayalam
	indicPlan.rphf = newIndicWouldSubstituteFeature(&plan.map_, ot.NewTag('r', 'p', 'h', 'f'), zeroContext)
	indicPlan.pref = newIndicWouldSubstituteFeature(&plan.map_, ot.NewTag('p', 'r', 'e', 'f'), zeroContext)
	indicPlan.blwf = newIndicWouldSubstituteFeature(&plan.map_, ot.NewTag('b', 'l', 'w', 'f'), zeroContext)
	indicPlan.pstf = newIndicWouldSubstituteFeature(&plan.map_, ot.NewTag('p', 's', 't', 'f'), zeroContext)
	indicPlan.vatu = newIndicWouldSubstituteFeature(&plan.map_, ot.NewTag('v', 'a', 't', 'u'), zeroContext)

	for i := range indicPlan.maskArray {
		if indicFeatures[i].flags&ffGLOBAL != 0 {
			indicPlan.maskArray[i] = 0
		} else {
			indicPlan.maskArray[i] = plan.map_.getMask1(indicFeatures[i].tag)
		}
	}

	cs.plan = indicPlan
}

func (indicPlan *indicShapePlan) consonantPositionFromFace(consonant, virama GID, font *Font) uint8 {
	/* For old-spec, the order of glyphs is Consonant,Virama,
	* whereas for new-spec, it's Virama,Consonant.  However,
	* some broken fonts (like Free Sans) simply copied lookups
	* from old-spec to new-spec without modification.
	* And oddly enough, Uniscribe seems to respect those lookups.
	* Eg. in the sequence U+0924,U+094D,U+0930, Uniscribe finds
	* base at 0.  The font however, only has lookups matching
	* 930,94D in 'blwf', not the expected 94D,930 (with new-spec
	* table).  As such, we simply match both sequences.  Seems
	* to work.
	*
	* Vatu is done as well, for:
	* https://github.com/harfbuzz/harfbuzz/issues/1587
	 */
	glyphs := [3]GID{virama, consonant, virama}
	if indicPlan.blwf.wouldSubstitute(glyphs[0:2], font) ||
		indicPlan.blwf.wouldSubstitute(glyphs[1:3], font) ||
		indicPlan.vatu.wouldSubstitute(glyphs[0:2], font) ||
		indicPlan.vatu.wouldSubstitute(glyphs[1:3], font) {
		return posBelowC
	}
	if indicPlan.pstf.wouldSubstitute(glyphs[0:2], font) ||
		indicPlan.pstf.wouldSubstitute(glyphs[1:3], font) {
		return posPostC
	}
	if indicPlan.pref.wouldSubstitute(glyphs[0:2], font) ||
		indicPlan.pref.wouldSubstitute(glyphs[1:3], font) {
		return posPostC
	}
	return posBaseC
}

func (cs *complexShaperIndic) setupMasks(plan *otShapePlan, buffer *Buffer, _ *Font) {
	/* We cannot setup masks here.  We save information about characters
	* and setup masks later on in a pause-callback. */

	info := buffer.Info
	for i := range info {
		info[i].setIndicProperties()
	}
}

func setupSyllablesIndic(_ *otShapePlan, _ *Font, buffer *Buffer) bool {
	findSyllablesIndic(buffer)
	iter, count := buffer.syllableIterator()
	for start, end := iter.next(); start < count; start, end = iter.next() {
		buffer.unsafeToBreak(start, end)
	}
	return false
}

func foundSyllableIndic(syllableType uint8, ts, te int, info []GlyphInfo, syllableSerial *uint8) {
	for i := ts; i < te; i++ {
		info[i].syllable = (*syllableSerial << 4) | syllableType
	}
	*syllableSerial++
	if *syllableSerial == 16 {
		*syllableSerial = 1
	}
}

func (indicPlan *indicShapePlan) updateConsonantPositionsIndic(font *Font, buffer *Buffer) {
	virama := indicPlan.loadViramaGlyph(font)
	if virama != 0 {
		info := buffer.Info
		for i := range info {
			if info[i].complexAux == posBaseC {
				consonant := info[i].Glyph
				info[i].complexAux = indicPlan.consonantPositionFromFace(consonant, virama, font)
			}
		}
	}
}

/* Rules from:
 * https://docs.microsqoft.com/en-us/typography/script-development/devanagari */
func (indicPlan *indicShapePlan) initialReorderingConsonantSyllable(font *Font, buffer *Buffer, start, end int) {
	info := buffer.Info

	/* https://github.com/harfbuzz/harfbuzz/issues/435#issuecomment-335560167
	* For compatibility with legacy usage in Kannada,
	* Ra+h+ZWJ must behave like Ra+ZWJ+h... */
	if buffer.Props.Script == language.Kannada &&
		start+3 <= end &&
		isOneOf(&info[start], 1<<indSM_ex_Ra) &&
		isOneOf(&info[start+1], 1<<indSM_ex_H) &&
		isOneOf(&info[start+2], 1<<indSM_ex_ZWJ) {
		buffer.mergeClusters(start+1, start+3)
		info[start+1], info[start+2] = info[start+2], info[start+1]
	}

	/* 1. Find base consonant:
	*
	* The shaping engine finds the base consonant of the syllable, using the
	* following algorithm: starting from the end of the syllable, move backwards
	* until a consonant is found that does not have a below-base or post-base
	* form (post-base forms have to follow below-base forms), or that is not a
	* pre-base-reordering Ra, or arrive at the first consonant. The consonant
	* stopped at will be the base.
	*
	*   o If the syllable starts with Ra + Halant (in a script that has Reph)
	*     and has more than one consonant, Ra is excluded from candidates for
	*     base consonants.
	 */

	base := end
	hasReph := false

	{
		/* . If the syllable starts with Ra + Halant (in a script that has Reph)
		 *    and has more than one consonant, Ra is excluded from candidates for
		 *    base consonants. */
		limit := start
		if indicPlan.maskArray[indicRphf] != 0 && start+3 <= end &&
			((indicPlan.config.rephMode == rephModeImplicit && !isJoiner(&info[start+2])) ||
				(indicPlan.config.rephMode == rephModeExplicit && info[start+2].complexCategory == indSM_ex_ZWJ)) {
			/* See if it matches the 'rphf' feature. */
			glyphs := [3]GID{info[start].Glyph, info[start+1].Glyph, 0}
			if indicPlan.config.rephMode == rephModeExplicit {
				glyphs[2] = info[start+2].Glyph
			}
			if indicPlan.rphf.wouldSubstitute(glyphs[:2], font) ||
				(indicPlan.config.rephMode == rephModeExplicit &&
					indicPlan.rphf.wouldSubstitute(glyphs[:3], font)) {
				limit += 2
				for limit < end && isJoiner(&info[limit]) {
					limit++
				}
				base = start
				hasReph = true
			}
		} else if indicPlan.config.rephMode == rephModeLogRepha && info[start].complexCategory == indSM_ex_Repha {
			limit += 1
			for limit < end && isJoiner(&info[limit]) {
				limit++
			}
			base = start
			hasReph = true
		}

		{
			/* . starting from the end of the syllable, move backwards */
			i := end
			seenBelow := false
			for do := true; do; do = i > limit {
				i--
				/* . until a consonant is found */
				if isConsonant(&info[i]) {
					/* . that does not have a below-base or post-base form
					 * (post-base forms have to follow below-base forms), */
					if info[i].complexAux != posBelowC &&
						(info[i].complexAux != posPostC || seenBelow) {
						base = i
						break
					}
					if info[i].complexAux == posBelowC {
						seenBelow = true
					}

					/* . or that is not a pre-base-reordering Ra,
					 *
					 * IMPLEMENTATION NOTES:
					 *
					 * Our pre-base-reordering Ra's are marked posPostC, so will be skipped
					 * by the logic above already.
					 */

					/* . or arrive at the first consonant. The consonant stopped at will
					 * be the base. */
					base = i
				} else {
					/* A ZWJ after a Halant stops the base search, and requests an explicit
					 * half form.
					 * A ZWJ before a Halant, requests a subjoined form instead, and hence
					 * search continues.  This is particularly important for Bengali
					 * sequence Ra,H,Ya that should form Ya-Phalaa by subjoining Ya. */
					if start < i &&
						info[i].complexCategory == indSM_ex_ZWJ &&
						info[i-1].complexCategory == indSM_ex_H {
						break
					}
				}
			}
		}

		/* . If the syllable starts with Ra + Halant (in a script that has Reph)
		 *    and has more than one consonant, Ra is excluded from candidates for
		 *    base consonants.
		 *
		 *  Only do this for unforced Reph. (ie. not for Ra,H,ZWJ. */
		if hasReph && base == start && limit-base <= 2 {
			/* Have no other consonant, so Reph is not formed and Ra becomes base. */
			hasReph = false
		}
	}

	/* 2. Decompose and reorder Matras:
	*
	* Each matra and any syllable modifier sign in the syllable are moved to the
	* appropriate position relative to the consonant(s) in the syllable. The
	* shaping engine decomposes two- or three-part matras into their constituent
	* parts before any repositioning. Matra characters are classified by which
	* consonant in a conjunct they have affinity for and are reordered to the
	* following positions:
	*
	*   o Before first half form in the syllable
	*   o After subjoined consonants
	*   o After post-form consonant
	*   o After main consonant (for above marks)
	*
	* IMPLEMENTATION NOTES:
	*
	* The normalize() routine has already decomposed matras for us, so we don't
	* need to worry about that.
	 */

	/* 3.  Reorder marks to canonical order:
	*
	* Adjacent nukta and halant or nukta and vedic sign are always repositioned
	* if necessary, so that the nukta is first.
	*
	* IMPLEMENTATION NOTES:
	*
	* We don't need to do this: the normalize() routine already did this for us.
	 */

	/* Reorder characters */

	for i := start; i < base; i++ {
		info[i].complexAux = min8(posPreC, info[i].complexAux)
	}

	if base < end {
		info[base].complexAux = posBaseC
	}

	/* Handle beginning Ra */
	if hasReph {
		info[start].complexAux = posRaToBecomeReph
	}

	/* For old-style Indic script tags, move the first post-base Halant after
	* last consonant.
	*
	* Reports suggest that in some scripts Uniscribe does this only if there
	* is *not* a Halant after last consonant already.  We know that is the
	* case for Kannada, while it reorders unconditionally in other scripts,
	* eg. Malayalam, Bengali, and Devanagari.  We don't currently know about
	* other scripts, so we block Kannada.
	*
	* Kannada test case:
	* U+0C9A,U+0CCD,U+0C9A,U+0CCD
	* With some versions of Lohit Kannada.
	* https://bugs.freedesktop.org/show_bug.cgi?id=59118
	*
	* Malayalam test case:
	* U+0D38,U+0D4D,U+0D31,U+0D4D,U+0D31,U+0D4D
	* With lohit-ttf-20121122/Lohit-Malayalam.ttf
	*
	* Bengali test case:
	* U+0998,U+09CD,U+09AF,U+09CD
	* With Windows XP vrinda.ttf
	* https://github.com/harfbuzz/harfbuzz/issues/1073
	*
	* Devanagari test case:
	* U+091F,U+094D,U+0930,U+094D
	* With chandas.ttf
	* https://github.com/harfbuzz/harfbuzz/issues/1071
	 */
	if indicPlan.isOldSpec {
		disallowDoubleHalants := buffer.Props.Script == language.Kannada
		for i := base + 1; i < end; i++ {
			if info[i].complexCategory == indSM_ex_H {
				var j int
				for j = end - 1; j > i; j-- {
					if isConsonant(&info[j]) ||
						(disallowDoubleHalants && info[j].complexCategory == indSM_ex_H) {
						break
					}
				}
				if info[j].complexCategory != indSM_ex_H && j > i {
					/* Move Halant to after last consonant. */
					if debugMode {
						fmt.Printf("INDIC - halant: switching glyph %d to %d (and shifting between)", i, j)
					}
					t := info[i]
					copy(info[i:j], info[i+1:])
					info[j] = t
				}
				break
			}
		}
	}

	/* Attach misc marks to previous char to move with them. */
	{
		var lastPos uint8 = posStart
		for i := start; i < end; i++ {
			if 1<<info[i].complexCategory&(joinerFlags|1<<indSM_ex_N|1<<indSM_ex_RS|1<<indSM_ex_CM|1<<indSM_ex_H) != 0 {
				info[i].complexAux = lastPos
				if info[i].complexCategory == indSM_ex_H && info[i].complexAux == posPreM {
					/*
					* Uniscribe doesn't move the Halant with Left Matra.
					* TEST: U+092B,U+093F,U+094D
					* We follow.
					 */
					for j := i; j > start; j-- {
						if info[j-1].complexAux != posPreM {
							info[i].complexAux = info[j-1].complexAux
							break
						}
					}
				}
			} else if info[i].complexAux != posSmvd {
				if info[i].complexCategory == indSM_ex_MPst &&
					i > start && info[i-1].complexCategory == indSM_ex_SM {
					info[i-1].complexAux = info[i].complexAux
				}

				lastPos = info[i].complexAux
			}
		}
	}

	/* For post-base consonants let them own anything before them
	* since the last consonant or matra. */
	{
		last := base
		for i := base + 1; i < end; i++ {
			if isConsonant(&info[i]) {
				for j := last + 1; j < i; j++ {
					if info[j].complexAux < posSmvd {
						info[j].complexAux = info[i].complexAux
					}
				}
				last = i
			} else if ic := info[i].complexCategory; ic == indSM_ex_M || ic == indSM_ex_MPst {
				last = i
			}
		}
	}

	{
		/* Use Syllable for sort accounting temporarily. */
		syllable := info[start].syllable
		for i := start; i < end; i++ {
			info[i].syllable = uint8(i - start)
		}

		/* Sit tight, rock 'n roll! */

		if debugMode {
			fmt.Printf("INDIC - post-base: sorting between glyph %d and %d\n", start, end)
		}

		subSlice := info[start:end]
		sort.SliceStable(subSlice, func(i, j int) bool { return subSlice[i].complexAux < subSlice[j].complexAux })

		// Find base again; also flip left-matra sequence.
		firstLeftMatra := end
		lastLeftMatra := end
		base = end
		for i := start; i < end; i++ {
			if info[i].complexAux == posBaseC {
				base = i
				break
			} else if info[i].complexAux == posPreM {
				if firstLeftMatra == end {
					firstLeftMatra = i
				}
				lastLeftMatra = i
			}
		}

		/* https://github.com/harfbuzz/harfbuzz/issues/3863 */
		if firstLeftMatra < lastLeftMatra {
			/* No need to merge clusters, handled later. */
			buffer.reverseRange(firstLeftMatra, lastLeftMatra+1)
			/* Reverse back nuktas, etc. */
			i := firstLeftMatra
			for j := i; j <= lastLeftMatra; j++ {
				if ic := info[j].complexCategory; ic == indSM_ex_M || ic == indSM_ex_MPst {
					buffer.reverseRange(i, j+1)
					i = j + 1
				}
			}
		}

		// Things are out-of-control for post base positions, they may shuffle
		// around like crazy.  In old-spec mode, we move halants around, so in
		// that case merge all clusters after base.  Otherwise, check the sort
		// order and merge as needed.
		// For pre-base stuff, we handle cluster issues in final reordering.
		//
		// We could use buffer.sort() for this, if there was no special
		// reordering of pre-base stuff happening later...
		// We don't want to mergeClusters all of that, which buffer.sort()
		// would.  Here's a concrete example:
		//
		// Assume there's a pre-base consonant and explicit Halant before base,
		// followed by a prebase-reordering (left) Matra:
		//
		//   C,H,ZWNJ,B,M
		//
		// At this point in reordering we would have:
		//
		//   M,C,H,ZWNJ,B
		//
		// whereas in final reordering we will bring the Matra closer to Base:
		//
		//   C,H,ZWNJ,M,B
		//
		// That's why we don't want to merge-clusters anything before the Base
		// at this point.  But if something moved from after Base to before it,
		// we should merge clusters from base to them.  In final-reordering, we
		// only move things around before base, and merge-clusters up to base.
		// These two merge-clusters from the two sides of base will interlock
		// to merge things correctly. See:
		// https://github.com/harfbuzz/harfbuzz/issues/2272
		if indicPlan.isOldSpec || end-start > 127 {
			buffer.mergeClusters(base, end)
		} else {
			/* Note!  Syllable is a one-byte field. */
			for i := base; i < end; i++ {
				if info[i].syllable != 255 {
					ma, mi := i, i
					j := start + int(info[i].syllable)
					for j != i {
						mi = min(mi, j)
						ma = max(ma, j)
						next := start + int(info[j].syllable)
						info[j].syllable = 255 /* So we don't process j later again. */
						j = next
					}
					buffer.mergeClusters(max(base, mi), ma+1)
				}
			}
		}

		/* Put syllable back in. */
		for i := start; i < end; i++ {
			info[i].syllable = syllable
		}
	}

	/* Setup masks now */

	{
		var mask GlyphMask

		/* Reph */
		for i := start; i < end && info[i].complexAux == posRaToBecomeReph; i++ {
			info[i].Mask |= indicPlan.maskArray[indicRphf]
		}

		/* Pre-base */
		mask = indicPlan.maskArray[indicHalf]
		if !indicPlan.isOldSpec &&
			indicPlan.config.blwfMode == blwfModePreAndPost {
			mask |= indicPlan.maskArray[indicBlwf]
		}
		for i := start; i < base; i++ {
			info[i].Mask |= mask
		}
		/* Base */
		mask = 0
		if base < end {
			info[base].Mask |= mask
		}
		/* Post-base */
		mask = indicPlan.maskArray[indicBlwf] |
			indicPlan.maskArray[indicAbvf] |
			indicPlan.maskArray[indicPstf]
		for i := base + 1; i < end; i++ {
			info[i].Mask |= mask
		}
	}

	if indicPlan.isOldSpec &&
		buffer.Props.Script == language.Devanagari {
		/* Old-spec eye-lash Ra needs special handling.  From the
		 * spec:
		 *
		 * "The feature 'below-base form' is applied to consonants
		 * having below-base forms and following the base consonant.
		 * The exception is vattu, which may appear below half forms
		 * as well as below the base glyph. The feature 'below-base
		 * form' will be applied to all such occurrences of Ra as well."
		 *
		 * Test case: U+0924,U+094D,U+0930,U+094d,U+0915
		 * with Sanskrit 2003 font.
		 *
		 * However, note that Ra,Halant,ZWJ is the correct way to
		 * request eyelash form of Ra, so we wouldbn't inhibit it
		 * in that sequence.
		 *
		 * Test case: U+0924,U+094D,U+0930,U+094d,U+200D,U+0915
		 */
		for i := start; i+1 < base; i++ {
			if info[i].complexCategory == indSM_ex_Ra &&
				info[i+1].complexCategory == indSM_ex_H &&
				(i+2 == base ||
					info[i+2].complexCategory != indSM_ex_ZWJ) {
				info[i].Mask |= indicPlan.maskArray[indicBlwf]
				info[i+1].Mask |= indicPlan.maskArray[indicBlwf]
			}
		}
	}

	prefLen := 2
	if indicPlan.maskArray[indicPref] != 0 && base+prefLen < end {
		/* Find a Halant,Ra sequence and mark it for pre-base-reordering processing. */
		for i := base + 1; i+prefLen-1 < end; i++ {
			var glyphs [2]GID
			for j := 0; j < prefLen; j++ {
				glyphs[j] = info[i+j].Glyph
			}
			if indicPlan.pref.wouldSubstitute(glyphs[:prefLen], font) {
				for j := 0; j < prefLen; j++ {
					info[i].Mask |= indicPlan.maskArray[indicPref]
					i++
				}
				break
			}
		}
	}

	/* Apply ZWJ/ZWNJ effects */
	for i := start + 1; i < end; i++ {
		if isJoiner(&info[i]) {
			nonJoiner := info[i].complexCategory == indSM_ex_ZWNJ
			j := i

			for do := true; do; do = (j > start && !isConsonant(&info[j])) {
				j--

				/* ZWJ/ZWNJ should disable CJCT.  They do that by simply
				 * being there, since we don't skip them for the CJCT
				 * feature (ie. F_MANUAL_ZWJ) */

				/* A ZWNJ disables HALF. */
				if nonJoiner {
					info[j].Mask &= ^indicPlan.maskArray[indicHalf]
				}

			}
		}
	}
}

func (indicPlan *indicShapePlan) initialReorderingStandaloneCluster(font *Font, buffer *Buffer, start, end int) {
	/* We treat placeholder/dotted-circle as if they are consonants, so we
	* should just chain.  Only if not in compatibility mode that is... */

	if indicPlan.uniscribeBugCompatible {
		/* For dotted-circle, this is what Uniscribe does:
		 * If dotted-circle is the last glyph, it just does nothing.
		 * Ie. It doesn't form Reph. */
		if buffer.Info[end-1].complexCategory == indSM_ex_DOTTEDCIRCLE {
			return
		}
	}

	indicPlan.initialReorderingConsonantSyllable(font, buffer, start, end)
}

func (indicPlan *indicShapePlan) initialReorderingSyllableIndic(font *Font, buffer *Buffer, start, end int) {
	syllableType := (buffer.Info[start].syllable & 0x0F)
	switch syllableType {
	case indicVowelSyllable, indicConsonantSyllable: /* We made the vowels look like consonants.  So let's call the consonant logic! */
		indicPlan.initialReorderingConsonantSyllable(font, buffer, start, end)
	case indicBrokenCluster, indicStandaloneCluster: /* We already inserted dotted-circles, so just call the standalone_cluster. */
		indicPlan.initialReorderingStandaloneCluster(font, buffer, start, end)
	}
}

func (cs *complexShaperIndic) initialReorderingIndic(_ *otShapePlan, font *Font, buffer *Buffer) bool {
	if debugMode {
		fmt.Println("INDIC - start reordering indic initial")
	}

	cs.plan.updateConsonantPositionsIndic(font, buffer)
	ret := syllabicInsertDottedCircles(font, buffer, indicBrokenCluster,
		indSM_ex_DOTTEDCIRCLE, indSM_ex_Repha, posEnd)

	iter, count := buffer.syllableIterator()
	for start, end := iter.next(); start < count; start, end = iter.next() {
		cs.plan.initialReorderingSyllableIndic(font, buffer, start, end)
	}

	if debugMode {
		fmt.Println("INDIC - end reordering indic initial")
	}

	return ret
}

func (indicPlan *indicShapePlan) finalReorderingSyllableIndic(plan *otShapePlan, buffer *Buffer, start, end int) {
	info := buffer.Info

	/* This function relies heavily on halant glyphs.  Lots of ligation
	* and possibly multiple substitutions happened prior to this
	* phase, and that might have messed up our properties.  Recover
	* from a particular case of that where we're fairly sure that a
	* class of otH is desired but has been lost. */
	/* We don't call loadViramaGlyph(), since we know it's already
	* loaded. */
	viramaGlyph := indicPlan.viramaGlyph
	if viramaGlyph != 0 {
		for i := start; i < end; i++ {
			if info[i].Glyph == viramaGlyph &&
				info[i].ligated() && info[i].multiplied() {
				/* This will make sure that this glyph passes isHalant() test. */
				info[i].complexCategory = indSM_ex_H
				info[i].clearLigatedAndMultiplied()
			}
		}
	}

	/* 4. Final reordering:
	*
	* After the localized forms and basic shaping forms GSUB features have been
	* applied (see below), the shaping engine performs some final glyph
	* reordering before applying all the remaining font features to the entire
	* syllable.
	 */

	tryPref := indicPlan.maskArray[indicPref] != 0

	/* Find base again */
	var base int
	for base = start; base < end; base++ {
		if info[base].complexAux >= posBaseC {
			if tryPref && base+1 < end {
				for i := base + 1; i < end; i++ {
					if (info[i].Mask & indicPlan.maskArray[indicPref]) != 0 {
						if !(info[i].substituted() && info[i].ligatedAndDidntMultiply()) {
							/* Ok, this was a 'pref' candidate but didn't form any.
							* Base is around here... */
							base = i
							for base < end && isHalant(&info[base]) {
								base++
							}
							if base < end {
								info[base].complexAux = posBaseC
							}

							tryPref = false
						}
						break
					}
					if base == end {
						break
					}
				}
			}
			/* For Malayalam, skip over unformed below- (but NOT post-) forms. */
			if buffer.Props.Script == language.Malayalam {
				for i := base + 1; i < end; i++ {
					for i < end && isJoiner(&info[i]) {
						i++
					}
					if i == end || !isHalant(&info[i]) {
						break
					}
					i++ /* Skip halant. */
					for i < end && isJoiner(&info[i]) {
						i++
					}
					if i < end && isConsonant(&info[i]) && info[i].complexAux == posBelowC {
						base = i
						info[base].complexAux = posBaseC
					}
				}
			}

			if start < base && info[base].complexAux > posBaseC {
				base--
			}
			break
		}
	}
	if base == end && start < base && isOneOf(&info[base-1], 1<<indSM_ex_ZWJ) {
		base--
	}
	if base < end {
		for start < base && isOneOf(&info[base], 1<<indSM_ex_N|1<<indSM_ex_H) {
			base--
		}
	}

	/*   o Reorder matras:
	*
	*     If a pre-base matra character had been reordered before applying basic
	*     features, the glyph can be moved closer to the main consonant based on
	*     whether half-forms had been formed. Actual position for the matra is
	*     defined as “after last standalone halant glyph, after initial matra
	*     position and before the main consonant”. If ZWJ or ZWNJ follow this
	*     halant, position is moved after it.
	*
	* IMPLEMENTATION NOTES:
	*
	* It looks like the last sentence is wrong.  Testing, with Windows 7 Uniscribe
	* and Devanagari shows that the behavior is best described as:
	*
	* "If ZWJ follows this halant, matra is NOT repositioned after this halant.
	*  If ZWNJ follows this halant, position is moved after it."
	*
	* Test case, with Adobe Devanagari or Nirmala UI:
	*
	*   U+091F,U+094D,U+200C,U+092F,U+093F
	*   (Matra moves to the middle, after ZWNJ.)
	*
	*   U+091F,U+094D,U+200D,U+092F,U+093F
	*   (Matra does NOT move, stays to the left.)
	*
	* https://github.com/harfbuzz/harfbuzz/issues/1070
	 */

	if start+1 < end && start < base /* Otherwise there can't be any pre-base matra characters. */ {
		/* If we lost track of base, alas, position before last thingy. */
		newPos := base - 1
		if base == end {
			newPos = base - 2
		}

		/* Malayalam / Tamil do not have "half" forms or explicit virama forms.
		 * The glyphs formed by 'half' are Chillus or ligated explicit viramas.
		 * We want to position matra after them.
		 */
		if buffer.Props.Script != language.Malayalam && buffer.Props.Script != language.Tamil {
		search:
			for newPos > start && !isOneOf(&info[newPos], 1<<indSM_ex_M|1<<indSM_ex_MPst|1<<indSM_ex_H) {
				newPos--
			}
			/* If we found no Halant we are done.
			* Otherwise only proceed if the Halant does
			* not belong to the Matra itself! */
			if isHalant(&info[newPos]) && info[newPos].complexAux != posPreM {
				if newPos+1 < end {
					/* . If ZWJ follows this halant, matra is NOT repositioned after this halant. */
					if info[newPos+1].complexCategory == indSM_ex_ZWJ {
						/* Keep searching. */
						if newPos > start {
							newPos--
							goto search
						}
					}
					/* . If ZWNJ follows this halant, position is moved after it.
					*
					* IMPLEMENTATION NOTES:
					*
					* This is taken care of by the state-machine. A Halant,ZWNJ is a terminating
					* sequence for a consonant syllable; any pre-base matras occurring after it
					* will belong to the subsequent syllable.
					 */
				}
			} else {
				newPos = start /* No move. */
			}
		}

		if start < newPos && info[newPos].complexAux != posPreM {
			/* Now go see if there's actually any matras... */
			for i := newPos; i > start; i-- {
				if info[i-1].complexAux == posPreM {
					oldPos := i - 1
					if oldPos < base && base <= newPos { /* Shouldn't actually happen. */
						base--
					}

					if debugMode {
						fmt.Printf("INDIC - matras: switching glyph %d to %d (and shifting between)", oldPos, newPos)
					}

					tmp := info[oldPos]
					copy(info[oldPos:newPos], info[oldPos+1:])
					info[newPos] = tmp

					/* Note: this mergeClusters() is intentionally *after* the reordering.
					* Indic matra reordering is special and tricky... */
					buffer.mergeClusters(newPos, min(end, base+1))

					newPos--
				}
			}
		} else {
			for i := start; i < base; i++ {
				if info[i].complexAux == posPreM {
					buffer.mergeClusters(i, min(end, base+1))
					break
				}
			}
		}
	}

	/*   o Reorder reph:
	*
	*     Reph’s original position is always at the beginning of the syllable,
	*     (i.e. it is not reordered at the character reordering stage). However,
	*     it will be reordered according to the basic-forms shaping results.
	*     Possible positions for reph, depending on the script, are; after main,
	*     before post-base consonant forms, and after post-base consonant forms.
	 */

	/* Two cases:
	*
	* - If repha is encoded as a sequence of characters (Ra,H or Ra,H,ZWJ), then
	*   we should only move it if the sequence ligated to the repha form.
	*
	* - If repha is encoded separately and in the logical position, we should only
	*   move it if it did NOT ligate.  If it ligated, it's probably the font trying
	*   to make it work without the reordering.
	 */
	if start+1 < end && info[start].complexAux == posRaToBecomeReph &&
		(info[start].complexCategory == indSM_ex_Repha) != info[start].ligatedAndDidntMultiply() {
		var newRephPos int
		rephPos := indicPlan.config.rephPos

		/*       1. If reph should be positioned after post-base consonant forms,
		 *          proceed to step 5.
		 */
		if rephPos == rephPosAfterPost {
			goto reph_step_5
		}

		/*       2. If the reph repositioning class is not after post-base: target
		 *          position is after the first explicit halant glyph between the
		 *          first post-reph consonant and last main consonant. If ZWJ or ZWNJ
		 *          are following this halant, position is moved after it. If such
		 *          position is found, this is the target position. Otherwise,
		 *          proceed to the next step.
		 *
		 *          Note: in old-implementation fonts, where classifications were
		 *          fixed in shaping engine, there was no case where reph position
		 *          will be found on this step.
		 */
		{
			newRephPos = start + 1
			for newRephPos < base && !isHalant(&info[newRephPos]) {
				newRephPos++
			}

			if newRephPos < base && isHalant(&info[newRephPos]) {
				/* .If ZWJ or ZWNJ are following this halant, position is moved after it. */
				if newRephPos+1 < base && isJoiner(&info[newRephPos+1]) {
					newRephPos++
				}
				goto reph_move
			}
		}

		/*       3. If reph should be repositioned after the main consonant: find the
		 *          first consonant not ligated with main, or find the first
		 *          consonant that is not a potential pre-base-reordering Ra.
		 */
		if rephPos == rephPosAfterMain {
			newRephPos = base
			for newRephPos+1 < end && info[newRephPos+1].complexAux <= posAfterMain {
				newRephPos++
			}
			if newRephPos < end {
				goto reph_move
			}
		}

		/*       4. If reph should be positioned before post-base consonant, find
		 *          first post-base classified consonant not ligated with main. If no
		 *          consonant is found, the target position should be before the
		 *          first matra, syllable modifier sign or vedic sign.
		 */
		/* This is our take on what step 4 is trying to say (and failing, BADLY). */
		if rephPos == rephPosAfterSub {
			newRephPos = base
			for newRephPos+1 < end &&
				(1<<info[newRephPos+1].complexAux)&(1<<posPostC|1<<posAfterPost|1<<posSmvd) == 0 {
				newRephPos++
			}
			if newRephPos < end {
				goto reph_move
			}
		}

		/*       5. If no consonant is found in steps 3 or 4, move reph to a position
		 *          immediately before the first post-base matra, syllable modifier
		 *          sign or vedic sign that has a reordering class after the intended
		 *          reph position. For example, if the reordering position for reph
		 *          is post-main, it will skip above-base matras that also have a
		 *          post-main position.
		 */
	reph_step_5:
		{
			/* Copied from step 2. */
			newRephPos = start + 1
			for newRephPos < base && !isHalant(&info[newRephPos]) {
				newRephPos++
			}

			if newRephPos < base && isHalant(&info[newRephPos]) {
				/* .If ZWJ or ZWNJ are following this halant, position is moved after it. */
				if newRephPos+1 < base && isJoiner(&info[newRephPos+1]) {
					newRephPos++
				}
				goto reph_move
			}
		}
		/* See https://github.com/harfbuzz/harfbuzz/issues/2298#issuecomment-615318654 */

		/*       6. Otherwise, reorder reph to the end of the syllable.
		 */
		{
			newRephPos = end - 1
			for newRephPos > start && info[newRephPos].complexAux == posSmvd {
				newRephPos--
			}

			/*
			* If the Reph is to be ending up after a Matra,Halant sequence,
			* position it before that Halant so it can interact with the Matra.
			* However, if it's a plain Consonant,Halant we shouldn't do that.
			* Uniscribe doesn't do this.
			* TEST: U+0930,U+094D,U+0915,U+094B,U+094D
			 */
			if !indicPlan.uniscribeBugCompatible && isHalant(&info[newRephPos]) {
				for i := base + 1; i < newRephPos; i++ {
					if ic := info[i].complexCategory; ic == indSM_ex_M || ic == indSM_ex_MPst {
						/* Ok, got it. */
						newRephPos--
					}
				}
			}

			goto reph_move
		}

	reph_move:
		{

			if debugMode {
				fmt.Printf("INDIC - reph: switching glyph %d to %d (and shifting between)", start, newRephPos)
			}

			/* Move */
			buffer.mergeClusters(start, newRephPos+1)
			reph := info[start]
			copy(info[start:newRephPos], info[start+1:])
			info[newRephPos] = reph

			if start < base && base <= newRephPos {
				base--
			}
		}
	}

	/*   o Reorder pre-base-reordering consonants:
	*
	*     If a pre-base-reordering consonant is found, reorder it according to
	*     the following rules:
	 */

	if tryPref && base+1 < end /* Otherwise there can't be any pre-base-reordering Ra. */ {
		for i := base + 1; i < end; i++ {
			if (info[i].Mask & indicPlan.maskArray[indicPref]) != 0 {
				/*       1. Only reorder a glyph produced by substitution during application
				 *          of the <pref> feature. (Note that a font may shape a Ra consonant with
				 *          the feature generally but block it in certain contexts.)
				 */
				/* Note: We just check that something got substituted.  We don't check that
				 * the <pref> feature actually did it...
				 *
				 * Reorder pref only if it ligated. */
				if info[i].ligatedAndDidntMultiply() {
					/*
					*       2. Try to find a target position the same way as for pre-base matra.
					*          If it is found, reorder pre-base consonant glyph.
					*
					*       3. If position is not found, reorder immediately before main
					*          consonant.
					 */

					newPos := base
					/* Malayalam / Tamil do not have "half" forms or explicit virama forms.
					* The glyphs formed by 'half' are Chillus or ligated explicit viramas.
					* We want to position matra after them.
					 */
					if buffer.Props.Script != language.Malayalam && buffer.Props.Script != language.Tamil {
						for newPos > start && !isOneOf(&info[newPos-1], 1<<indSM_ex_M|1<<indSM_ex_MPst|1<<indSM_ex_H) {
							newPos--
						}
					}

					if newPos > start && isHalant(&info[newPos-1]) {
						/* . If ZWJ or ZWNJ follow this halant, position is moved after it. */
						if newPos < end && isJoiner(&info[newPos]) {
							newPos++
						}
					}

					{

						oldPos := i
						buffer.mergeClusters(newPos, oldPos+1)

						if debugMode {
							fmt.Printf("INDIC - pre-base: switching glyph %d to %d (and shifting between)", oldPos, newPos)
						}

						tmp := info[oldPos]
						copy(info[newPos+1:], info[newPos:oldPos])
						info[newPos] = tmp

						if newPos <= base && base < oldPos {
							base++
						}
					}
				}

				break
			}
		}
	}

	/* Apply 'init' to the Left Matra if it's a word start. */
	if info[start].complexAux == posPreM {
		const flagRange = 1<<(nonSpacingMark+1) - 1<<format
		if start == 0 || 1<<info[start-1].unicode.generalCategory()&flagRange == 0 {
			info[start].Mask |= indicPlan.maskArray[indicInit]
		} else {
			buffer.unsafeToBreak(start-1, start+1)
		}
	}

	// Finish off the clusters and go home!
	if indicPlan.uniscribeBugCompatible {
		/* Uniscribe merges the entire syllable into a single cluster... Except for Tamil.
		 * This means, half forms are submerged into the main consonant's cluster.
		 * This is unnecessary, and makes cursor positioning harder, but that's what
		 * Uniscribe does. */
		switch plan.props.Script {
		case language.Tamil:
		default:
			buffer.mergeClusters(start, end)
		}
	}
}

func (indicPlan *indicShapePlan) finalReorderingIndic(plan *otShapePlan, font *Font, buffer *Buffer) bool {
	if len(buffer.Info) == 0 {
		return false
	}

	if debugMode {
		fmt.Println("INDIC - start reordering indic final")
	}

	iter, count := buffer.syllableIterator()
	for start, end := iter.next(); start < count; start, end = iter.next() {
		indicPlan.finalReorderingSyllableIndic(plan, buffer, start, end)
	}

	if debugMode {
		fmt.Println("INDIC - end reordering indic final")
	}

	return false
}

func (ci complexShaperIndic) preprocessText(_ *otShapePlan, buffer *Buffer, _ *Font) {
	if !ci.plan.uniscribeBugCompatible {
		preprocessTextVowelConstraints(buffer)
	}
}

func (cs *complexShaperIndic) decompose(c *otNormalizeContext, ab rune) (rune, rune, bool) {
	switch ab {
	/* Don't decompose these. */
	case 0x0931, /* DEVANAGARI LETTER RRA */
		// https://github.com/harfbuzz/harfbuzz/issues/779
		0x09DC, /* BENGALI LETTER RRA */
		0x09DD, /* BENGALI LETTER RHA */
		0x0B94:
		return 0, 0, false /* TAMIL LETTER AU */

		/*
		 * Decompose split matras that don't have Unicode decompositions.
		 */
	}

	return uni.decompose(ab)
}

func (cs *complexShaperIndic) compose(c *otNormalizeContext, a, b rune) (rune, bool) {
	/* Avoid recomposing split matras. */
	if uni.generalCategory(a).isMark() {
		return 0, false
	}

	/* Composition-exclusion exceptions that we want to recompose. */
	if a == 0x09AF && b == 0x09BC {
		return 0x09DF, true
	}

	return uni.compose(a, b)
}

func (complexShaperIndic) marksBehavior() (zeroWidthMarks, bool) {
	return zeroWidthMarksNone, false
}

func (complexShaperIndic) normalizationPreference() normalizationMode {
	return nmComposedDiacriticsNoShortCircuit
}
