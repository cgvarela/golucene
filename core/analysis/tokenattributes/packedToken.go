package tokenattributes

import (
	"github.com/balzaczyy/golucene/core/util"
)

/*
Default implementation of the common attributes used by Lucene:
- CharTermAttribute
- TypeAttribute
- PositionIncrementAttribute
- PositionLengthAttribute
- OffsetAttribute
*/
type PackedTokenAttributeImpl struct {
	*CharTermAttributeImpl
	startOffset, endOffset int
	typ                    string
	positionIncrement      int
	positionLength         int
}

func NewPackedTokenAttribute() *util.AttributeImpl {
	return util.NewAttributeImpl(&PackedTokenAttributeImpl{
		CharTermAttributeImpl: newCharTermAttributeImpl().Value.(*CharTermAttributeImpl),
		typ:               DEFAULT_TYPE,
		positionIncrement: 1,
		positionLength:    1,
	})
}

func (a *PackedTokenAttributeImpl) Interfaces() []string {
	return []string{"CharTermAttribute", "TermToBytesRefAttribute",
		"TypeAttribute", "PositionIncrementAttribute",
		"PositionLengthAttribute", "OffsetAttribute"}
}

func (a *PackedTokenAttributeImpl) SetPositionIncrement(positionIncrement int) {
	assert2(positionIncrement >= 0, "Increment must be zero or greater: %v", positionIncrement)
	a.positionIncrement = positionIncrement
}

func (a *PackedTokenAttributeImpl) PositionIncrement() int {
	return a.positionIncrement
}

func (a *PackedTokenAttributeImpl) StartOffset() int {
	return a.startOffset
}

func (a *PackedTokenAttributeImpl) EndOffset() int {
	return a.endOffset
}

func (a *PackedTokenAttributeImpl) SetOffset(startOffset, endOffset int) {
	assert2(startOffset >= 0 && startOffset <= endOffset,
		"startOffset must be non-negative, and endOffset must be >= startOffset, "+
			"startOffset=%v,endOffset=%v", startOffset, endOffset)
	a.startOffset = startOffset
	a.endOffset = endOffset
}

func (a *PackedTokenAttributeImpl) SetType(typ string) {
	a.typ = typ
}

func (a *PackedTokenAttributeImpl) Clear() {
	a.CharTermAttributeImpl.Clear()
	a.positionIncrement, a.positionLength = 1, 1
	a.startOffset, a.endOffset = 0, 0
	a.typ = DEFAULT_TYPE
}
