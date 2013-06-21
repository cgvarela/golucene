package index

import (
	"bytes"
	"errors"
	"fmt"
	"log"
	"lucene/store"
	"lucene/util"
	"regexp"
	"strconv"
	"strings"
)

type FindSegmentsFile struct {
	directory                *store.Directory
	doBody                   func(segmentFileName string) (obj interface{}, err error)
	defaultGenLookaheadCount int
}

func NewFindSegmentsFile(directory *store.Directory,
	doBody func(segmentFileName string) (obj interface{}, err error)) *FindSegmentsFile {
	return &FindSegmentsFile{directory, doBody, 10}
}

// TODO support IndexCommit
func (fsf *FindSegmentsFile) run() (obj interface{}, err error) {
	// if commit != nil {
	// 	if fsf.directory != commit.Directory {
	// 		return nil, errors.New("the specified commit does not match the specified Directory")
	// 	}
	// 	return fsf.doBody(commit.SegmentsFileName)
	// }

	lastGen := int64(-1)
	gen := int64(0)
	genLookaheadCount := 0
	var exc error
	retryCount := 0

	useFirstMethod := true

	// Loop until we succeed in calling doBody() without
	// hitting an IOException.  An IOException most likely
	// means a commit was in process and has finished, in
	// the time it took us to load the now-old infos files
	// (and segments files).  It's also possible it's a
	// true error (corrupt index).  To distinguish these,
	// on each retry we must see "forward progress" on
	// which generation we are trying to load.  If we
	// don't, then the original error is real and we throw
	// it.

	// We have three methods for determining the current
	// generation.  We try the first two in parallel (when
	// useFirstMethod is true), and fall back to the third
	// when necessary.

	for {
		if useFirstMethod {
			// List the directory and use the highest
			// segments_N file.  This method works well as long
			// as there is no stale caching on the directory
			// contents (NOTE: NFS clients often have such stale
			// caching):
			genA := int64(-1)

			files, err := fsf.directory.ListAll()
			if err != nil {
				return nil, err
			}
			if files != nil {
				genA = LastCommitGeneration(files)
			}

			// TODO support info stream
			// if fsf.infoStream != nil {
			// 	message("directory listing genA=" + genA)
			// }
			log.Printf("directory listing genA=%v", genA)

			// Also open segments.gen and read its
			// contents.  Then we take the larger of the two
			// gens.  This way, if either approach is hitting
			// a stale cache (NFS) we have a better chance of
			// getting the right generation.
			genB := int64(-1)
			genInput, err := fsf.directory.OpenInput(INDEX_FILENAME_SEGMENTS_GEN, store.IO_CONTEXT_READ)
			if err != nil {
				// if fsf.infoStream != nil {
				log.Printf("segments.gen open: %v", err)
				// }
			} else {
				defer genInput.Close()

				version, err := genInput.ReadInt()
				if _, ok := err.(*CorruptIndexError); ok {
					return nil, err
				}
				if version == FORMAT_SEGMENTS_GEN_CURRENT {
					gen0, err := genInput.ReadLong()
					if err != nil {
						if _, ok := err.(*CorruptIndexError); ok {
							return nil, err
						}
					} else {
						gen1, err := genInput.ReadLong()
						if err != nil {
							if _, ok := err.(*CorruptIndexError); ok {
								return nil, err
							}
						} else {
							// if fsf.infoStream != nil {
							log.Printf("fallback check: %v; %v", gen0, gen1)
							// }
							if gen0 == gen1 {
								// The file is consistent.
								genB = gen0
							}
						}
					}
				} else {
					return nil, NewIndexFormatTooNewError(genInput.DataInput, version, FORMAT_SEGMENTS_GEN_CURRENT, FORMAT_SEGMENTS_GEN_CURRENT)
				}
			}

			// if fsf.infoStream != nil {
			log.Printf("%v check: genB=%v", INDEX_FILENAME_SEGMENTS_GEN, genB)
			// }

			// Pick the larger of the two gen's:
			gen = genA
			if genB > gen {
				gen = genB
			}

			if gen == -1 {
				// Neither approach found a generation
				return nil, errors.New(fmt.Sprintf("no segments* file found in %v: files: %#v", fsf.directory, files))
			}
		}

		if useFirstMethod && lastGen == gen && retryCount >= 2 {
			// Give up on first method -- this is 3rd cycle on
			// listing directory and checking gen file to
			// attempt to locate the segments file.
			useFirstMethod = false
		}

		// Second method: since both directory cache and
		// file contents cache seem to be stale, just
		// advance the generation.
		if !useFirstMethod {
			if genLookaheadCount < fsf.defaultGenLookaheadCount {
				gen++
				genLookaheadCount++
				// if fsf.infoStream != nil {
				log.Printf("look ahead increment gen to %v", gen)
				// }
			} else {
				// All attempts have failed -- throw first exc:
				return nil, exc
			}
		} else if lastGen == gen {
			// This means we're about to try the same
			// segments_N last tried.
			retryCount++
		} else {
			// Segment file has advanced since our last loop
			// (we made "progress"), so reset retryCount:
			retryCount = 0
		}

		lastGen = gen
		segmentFileName := FileNameFromGeneration(INDEX_FILENAME_SEGMENTS, "", gen)

		v, err := fsf.doBody(segmentFileName)
		if err != nil {
			// Save the original root cause:
			if exc == nil {
				exc = err
			}

			// if fsf.infoStream != nil {
			log.Printf("primary Exception on '%v': %v; will retry: retryCount = %v; gen = %v",
				segmentFileName, err, retryCount, gen)
			// }

			if gen > 1 && useFirstMethod && retryCount == 1 {
				// This is our second time trying this same segments
				// file (because retryCount is 1), and, there is
				// possibly a segments_(N-1) (because gen > 1).
				// So, check if the segments_(N-1) exists and
				// try it if so:
				prevSegmentFileName := FileNameFromGeneration(INDEX_FILENAME_SEGMENTS, "", gen-1)

				if prevExists := fsf.directory.FileExists(prevSegmentFileName); prevExists {
					// if fsf.infoStream != nil {
					log.Printf("fallback to prior segment file '%v'", prevSegmentFileName)
					// }
					v, err = fsf.doBody(prevSegmentFileName)
					if err != nil {
						// if fsf.infoStream != nil {
						log.Printf("secondary Exception on '%v': %v; will retry", prevSegmentFileName, err)
						//}
					} else {
						// if fsf.infoStream != nil {
						log.Printf("success on fallback %v", prevSegmentFileName)
						//}
						return v, nil
					}
				}
			}
		} else {
			// if fsf.infoStream != nil {
			log.Printf("success on %v", segmentFileName)
			// }
			return v, nil
		}
	}
}

func FileNameFromGeneration(base, ext string, gen int64) string {
	switch {
	case gen == -1:
		return ""
	case gen == 0:
		return SegmentFileName(base, "", ext)
	default:
		// assert gen > 0
		// The '6' part in the length is: 1 for '.', 1 for '_' and 4 as estimate
		// to the gen length as string (hopefully an upper limit so SB won't
		// expand in the middle.
		var buffer bytes.Buffer
		fmt.Fprintf(&buffer, "%v_%v", base, strconv.FormatInt(gen, 36))
		if len(ext) > 0 {
			buffer.WriteString(".")
			buffer.WriteString(ext)
		}
		return buffer.String()
	}
}

func SegmentFileName(name, suffix, ext string) string {
	if len(ext) > 0 || len(suffix) > 0 {
		// assert ext[0] != '.'
		var buffer bytes.Buffer
		buffer.WriteString(name)
		if len(suffix) > 0 {
			buffer.WriteString("_")
			buffer.WriteString(suffix)
		}
		if len(ext) > 0 {
			buffer.WriteString(".")
			buffer.WriteString(ext)
		}
		return buffer.String()
	}
	return name
}

const (
	INDEX_FILENAME_SEGMENTS     = "segments"
	INDEX_FILENAME_SEGMENTS_GEN = "segments.gen"
	COMOPUND_FILE_EXTENSION     = "cfs"
	VERSION_40                  = 0
	FORMAT_SEGMENTS_GEN_CURRENT = -2
)

type SegmentInfo struct {
	dir            *store.Directory
	version        string
	name           string
	docCount       int
	isCompoundFile bool
	codec          Codec
	diagnostics    map[string]string
	attributes     map[string]string
	Files          map[string]bool // must use CheckFileNames()
}

var CODEC_FILE_PATTERN = regexp.MustCompile("_[a-z0-9]+(_.*)?\\..*")

func (si *SegmentInfo) CheckFileNames(files map[string]bool) {
	for file, _ := range files {
		if !CODEC_FILE_PATTERN.MatchString(file) {
			panic(fmt.Sprintf("invalid codec filename '%v', must match: %v", file, CODEC_FILE_PATTERN))
		}
	}
}

type SegmentInfos struct {
	counter        int
	version        int64
	generation     int64
	lastGeneration int64
	userData       map[string]string
	Segments       []SegmentInfoPerCommit
}

func LastCommitGeneration(files []string) int64 {
	if files == nil {
		return int64(-1)
	}
	max := int64(-1)
	for _, file := range files {
		if strings.HasPrefix(file, INDEX_FILENAME_SEGMENTS) && file != INDEX_FILENAME_SEGMENTS {
			gen := GenerationFromSegmentsFileName(file)
			if gen > max {
				max = gen
			}
		}
	}
	return max
}

func GenerationFromSegmentsFileName(fileName string) int64 {
	switch {
	case fileName == INDEX_FILENAME_SEGMENTS:
		return int64(0)
	case strings.HasPrefix(fileName, INDEX_FILENAME_SEGMENTS):
		d, err := strconv.ParseInt(fileName[1+len(INDEX_FILENAME_SEGMENTS):], 36, 64)
		if err != nil {
			panic(err)
		}
		return d
	default:
		panic(fmt.Sprintf("filename %v is not a segments file", fileName))
	}
}

func (sis *SegmentInfos) Read(directory *store.Directory, segmentFileName string) error {
	success := false

	// Clear any previous segments:
	sis.Clear()

	sis.generation = GenerationFromSegmentsFileName(segmentFileName)
	sis.lastGeneration = sis.generation

	main, err := directory.OpenInput(segmentFileName, store.IO_CONTEXT_READ)
	if err != nil {
		return err
	}
	input := store.NewChecksumIndexInput(main)
	defer func() {
		if !success {
			// Clear any segment infos we had loaded so we
			// have a clean slate on retry:
			sis.Clear()
			util.CloseWhileSupressingError(input)
		} else {
			input.Close()
		}
	}()

	format, err := input.ReadInt()
	if err != nil {
		return err
	}
	if format == CODEC_MAGIC {
		// 4.0+
		CheckHeaderNoMagic(input.DataInput, "segments", VERSION_40, VERSION_40)
		sis.version, err = input.ReadLong()
		if err != nil {
			return err
		}
		sis.counter, err = input.ReadInt()
		if err != nil {
			return err
		}
		numSegments, err := input.ReadInt()
		if err != nil {
			return err
		}
		if numSegments < 0 {
			return &CorruptIndexError{fmt.Sprintf("invalid segment count: %v (resource: %v)", numSegments, input)}
		}
		for seg := 0; seg < numSegments; seg++ {
			segName, err := input.ReadString()
			if err != nil {
				return err
			}
			codecName, err := input.ReadString()
			if err != nil {
				return err
			} else if codecName != "lucene42" {
				log.Panicf("Not supported yet: %v", codecName)
			}
			// method := CodecForName(codecName)
			method := NewLucene42Codec()
			info, err := method.ReadSegmentInfo(directory, segName, store.IO_CONTEXT_READ)
			if err != nil {
				return err
			}
			info.codec = method
			delGen, err := input.ReadLong()
			if err != nil {
				return err
			}
			delCount, err := input.ReadInt()
			if err != nil {
				return err
			}
			if delCount < 0 || delCount > info.docCount {
				return &CorruptIndexError{fmt.Sprintf("invalid deletion count: %v (resource: %v)", delCount, input)}
			}
			sis.Segments = append(sis.Segments, NewSegmentInfoPerCommit(info, delCount, delGen))
		}
		sis.userData, err = input.ReadStringStringMap()
		if err != nil {
			return err
		}
	} else {
		// TODO support <4.0 index
		panic("not supported yet")
	}

	checksumNow := int64(input.Checksum())
	checksumThen, err := input.ReadLong()
	if err != nil {
		return err
	}
	if checksumNow != checksumThen {
		return &CorruptIndexError{fmt.Sprintf("checksum mismatch in segments file (resource: %v)", input)}
	}

	success = true
	return nil
}

func (sis *SegmentInfos) Clear() {
	sis.Segments = make([]SegmentInfoPerCommit, 0)
}

type SegmentInfoPerCommit struct {
	info            SegmentInfo
	delCount        int
	delGen          int64
	nextWriteDelGen int64
}

func NewSegmentInfoPerCommit(info SegmentInfo, delCount int, delGen int64) SegmentInfoPerCommit {
	nextWriteDelGen := int64(1)
	if delGen != -1 {
		nextWriteDelGen = delGen + 1
	}
	return SegmentInfoPerCommit{info, delCount, delGen, nextWriteDelGen}
}

func (si SegmentInfoPerCommit) HasDeletions() bool {
	return si.delGen != -1
}

type SegmentReader struct {
	*AtomicReader
	si       SegmentInfoPerCommit
	liveDocs *util.Bits
	numDocs  int
	core     SegmentCoreReaders
}

func NewSegmentReader(si SegmentInfoPerCommit, termInfosIndexDivisor int, context store.IOContext) (r *SegmentReader, err error) {
	r = &SegmentReader{}
	r.AtomicReader = newAtomicReader(r)
	r.si = si
	r.core = newSegmentCoreReaders(r, si.info.dir, si, context, termInfosIndexDivisor)
	success := false
	defer func() {
		// With lock-less commits, it's entirely possible (and
		// fine) to hit a FileNotFound exception above.  In
		// this case, we want to explicitly close any subset
		// of things that were opened so that we don't have to
		// wait for a GC to do so.
		if !success {
			r.core.decRef <- true
		}
	}()

	if si.HasDeletions() {
		panic("not supported yet")
	} else {
		// assert si.getDelCount() == 0
		r.liveDocs = nil
	}
	r.numDocs = si.info.docCount - si.delCount
	success = true
	return r, nil
}

type SegmentCoreReaders struct {
	fieldInfos FieldInfos

	termsIndexDivisor int

	cfsReader store.CompoundFileDirectory

	addListener    chan *CoreClosedListener
	removeListener chan *CoreClosedListener
	decRef         chan bool
}

type CoreClosedListener interface {
	onClose(r *SegmentReader)
}

func newSegmentCoreReaders(owner SegmentReader, dir *store.Directory, si SegmentInfoPerCommit,
	context store.IOContext, termsIndexDivisor int) SegmentCoreReaders {
	if termsIndexDivisor == 0 {
		panic("indexDivisor must be < 0 (don't load terms index) or greater than 0 (got 0)")
	}

	self := SegmentCoreReaders{}

	self.addListener = make(chan *CoreClosedListener)
	self.removeListener = make(chan *CoreClosedListener)
	notifyListener := make(chan *SegmentReader)
	go func() {
		coreClosedListeners := make([]*CoreClosedListener, 0)
		nRemoved := 0
		isRunning := true
		var listener *CoreClosedListener
		for isRunning {
			select {
			case listener = <-self.addListener:
				coreClosedListeners = append(coreClosedListeners, listener)
			case listener = <-self.removeListener:
				for i, v := range coreClosedListeners {
					if v == listener {
						coreClosedListeners[i] = nil
						nRemoved++
					}
				}
				if n := len(coreClosedListeners); n > 16 && nRemoved > n/2 {
					newListeners := make([]*CoreClosedListeners, 0)
					i := 0
					for _, v := range coreClosedListeners {
						if v == nil {
							continue
						}
						newListeners = append(newListeners, v)
					}
				}
			case owner := <-notifyListener:
				isRunning = false
				for _, v := range coreClosedListeners {
					v.onClose(owner)
				}
			}
		}
	}()

	incRef := make(chan bool)
	self.decRef = make(chan bool)
	go func() {
		ref := 0
		isRunning := true
		for isRunning {
			select {
			case <-incRef:
				ref++
			case <-self.decRef:
				ref--
				if ref == 0 {
					util.Close(self.termVectorsLocal, self.fieldsReaderLocal, docValuesLocal, normsLocal, fields, dvProducer,
						termVectorsReaderOrig, fieldsReaderOrig, cfsReader, normsProducer)
					notifyListener <- true
					isRunning = false
				}
			}
		}
	}()

	success := false
	defer func() {
		if !success {
			self.decRef()
		}
	}()

	codec := si.info.codec
	var cfsDir *store.Directory // confusing name: if (cfs) its the cfsdir, otherwise its the segment's directory.
	if si.info.isCompoundFile {
		self.cfsReader = NewCompoundFileDirectory(dir,
			SegmentFileName(si.info.name, "", COMPOUND_FILE_EXTENSION), context, false)
		cfsDir = self.cfsReader
	} else {
		self.cfsReader = nil
		cfsDir = dir
	}
	self.fieldInfos = codec.ReadFieldInfos(cfsDir, si.info.name, IOContext.READONCE)
	self.termsIndexDivisor = termsIndexDivisor

	segmentReadState = NewSegmentReadState(cfsDir, si.info, fieldInfos, context, termsIndexDivisor)
	// Ask codec for its Fields
	self.fields = codec.FieldsProducer(segmentReadState)
	// assert fields != null;
	// ask codec for its Norms:
	// TODO: since we don't write any norms file if there are no norms,
	// kinda jaky to assume the codec handles the case of no norms file at all gracefully?!

	if self.fieldInfos.hasDocValues {
		self.dvProducer = codec.DocValuesProducer(segmentReadState)
		// assert dvProducer != null;
	} else {
		self.dvProducer = nil
	}

	if self.fieldInfos.hasNorms {
		self.normsProducer = codec.NormsProducer(segmentReadState)
		// assert normsProducer != null;
	} else {
		self.normsProducer = nil
	}

	self.fieldsReaderOrig = si.info.codec.StoredFieldsReader(cfsDir, si.info, fieldInfos, context)

	if self.fieldInfos.hasVectors { // open term vector files only as needed
		self.termVectorsReaderOrig = si.info.codecTermVectorsReader(cfsDir, si.info, fieldInfos, context)
	} else {
		self.termVectorsReaderOrig = nil
	}

	success = true

	// Must assign this at the end -- if we hit an
	// exception above core, we don't want to attempt to
	// purge the FieldCache (will hit NPE because core is
	// not assigned yet).
	self.owner = owner
	return self
}