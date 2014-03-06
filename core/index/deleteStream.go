package index

import (
	"github.com/balzaczyy/golucene/core/util"
)

// index/BufferedDeletesStream.java

/*
Tracks the stream of BufferedDeletes. When DocumentsWriterPerThread
flushes, its buffered deletes are appended to this stream. We later
apply these deletes (resolve them to the actual docIDs, per segment)
when a merge is started (only to the to-be-merged segments). We also
apply to all segments when NRT reader is pulled, commit/close is
called, or when too many deletes are buffered and must be flushed (by
RAM usage or by count).

Each packet is assigned a generation, and each flushed or merged
segment is also assigned a generation, so we can track when
BufferedDeletes packets to apply to any given segment.
*/
type BufferedDeletesStream struct {
	// TODO: maybe linked list?
	deletes []*FrozenBufferedDeletes

	// Starts at 1 so that SegmentInfos that have never had deletes
	// applied (whose bufferedDelGen defaults to 0) will be correct:
	nextGen int64

	// used only by assert
	lastDeleteTerm *Term

	infoStream util.InfoStream
	bytesUsed  int64 // atomic
	numTerms   int   // atomic
}

func newBufferedDeletesStream(infoStream util.InfoStream) *BufferedDeletesStream {
	return &BufferedDeletesStream{
		deletes:    make([]*FrozenBufferedDeletes, 0),
		nextGen:    1,
		infoStream: infoStream,
	}
}