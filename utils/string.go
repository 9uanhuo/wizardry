package utils

// StringTestFlags describes how to perform a string test
type StringTestFlags int64

const (
	// CompactWhitespace ("W" flag) compacts whitespace in the target,
	// which must contain at least one whitespace character
	CompactWhitespace = 1 << iota
	// OptionalBlanks ("w" flag) treats every blank in the magic as an optional blank
	OptionalBlanks
	// LowerMatchesBoth ("c" flag) specifies case-insensitive matching: lower case
	// characters in the magic match both lower and upper case characters
	// in the target
	LowerMatchesBoth
	// UpperMatchesBoth ("C" flag) specifies case-insensitive matching: upper case
	// characters in the magic match both lower and upper case characters
	// in the target
	UpperMatchesBoth
	// ForceText ("t" flag) forces the test to be done for text files
	ForceText
	// ForceBinary ("b" flag) forces the test to be done for binary files
	ForceBinary
)

// StringTest looks for a string pattern in target, at given index
func StringTest(sr *SliceReader, targetIndex int64, patternString string, flags StringTestFlags) int64 {
	bv := &ByteView{
		Input:    sr,
		LookBack: 0,
	}

	pattern := []byte(patternString)
	patternSize := len(pattern)
	patternIndex := 0

	for {
		patternByte := pattern[patternIndex]
		targetInt := bv.Get(targetIndex)
		if targetInt == -1 {
			return -1
		}
		targetByte := byte(targetInt)

		matches := patternByte == targetByte
		if matches {
			// perfect match, advance both
			targetIndex++
			patternIndex++
		} else if flags&OptionalBlanks > 0 && IsWhitespace(patternByte) {
			// cool, it's optional then
			patternIndex++
		} else if flags&LowerMatchesBoth > 0 && IsLowerLetter(patternByte) && ToLower(targetByte) == patternByte {
			// case insensitive match
			targetIndex++
			patternIndex++
		} else if flags&UpperMatchesBoth > 0 && IsUpperLetter(patternByte) && ToUpper(targetByte) == patternByte {
			// case insensitive match
			targetIndex++
			patternIndex++
		} else {
			// not a match
			return -1
		}

		if flags&CompactWhitespace > 0 && IsWhitespace(targetByte) {
			// if we had whitespace, skip any whitespace coming after it
			for {
				targetIndex++
				targetInt = bv.Get(targetIndex)
				if targetInt == -1 {
					return -1
				}
				targetByte = byte(targetInt)
				if !IsWhitespace(targetByte) {
					break
				}
			}
		}

		if patternIndex >= patternSize {
			// hey it matched all the way!
			return targetIndex
		}
	}
}
