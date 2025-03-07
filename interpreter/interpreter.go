package interpreter

import (
	"fmt"
	"io"

	"github.com/9uanhuo/wizardry/parser"
	"github.com/9uanhuo/wizardry/utils"
)

// MaxLevels is the maximum level of magic rules that are interpreted
const MaxLevels = 32

// LogFunc logs something somewhere
type LogFunc func(format string, args ...interface{})

// InterpretContext holds state for the interpreter
type InterpretContext struct {
	Logf LogFunc
	Book parser.Spellbook
}

// Identify follows the rules in a spellbook to find out the type of a file
func (ctx *InterpretContext) Identify(sr *utils.SliceReader) ([]string, error) {
	outStrings, err := ctx.identifyInternal(sr, 0, "", false)
	if err != nil {
		return nil, err
	}

	return outStrings, nil
}

func (ctx *InterpretContext) identifyInternal(sr *utils.SliceReader, pageOffset int64, page string, swapEndian bool) ([]string, error) {
	var outStrings []string

	matchedLevels := make([]bool, MaxLevels)
	everMatchedLevels := make([]bool, MaxLevels)
	globalOffset := int64(0)

	ctx.Logf("|====> identifying at %d using page %s (%d rules)", pageOffset, page, len(ctx.Book[page]))

	if page != "" {
		matchedLevels[0] = true
		everMatchedLevels[0] = true
	}

	for _, rule := range ctx.Book[page] {
		stopProcessing := false

		// if any of the deeper levels have ever matched, stop working
		for l := rule.Level + 1; l < len(matchedLevels); l++ {
			if everMatchedLevels[l] {
				stopProcessing = true
				break
			}
		}

		if stopProcessing {
			break
		}

		skipRule := false
		for l := 0; l < rule.Level; l++ {
			if !matchedLevels[l] {
				// if any of the parent levels aren't matched, skip the rule entirely
				skipRule = true
				break
			}
		}

		if skipRule {
			continue
		}

		lookupOffset := int64(0)

		ctx.Logf("| %s", rule)

		switch rule.Offset.OffsetType {
		case parser.OffsetTypeIndirect:
			indirect := rule.Offset.Indirect
			offsetAddress := indirect.OffsetAddress

			if indirect.IsRelative {
				offsetAddress += int64(globalOffset)
			}

			readAddress, err := readAnyUint(sr, int(offsetAddress), indirect.ByteWidth, indirect.Endianness.MaybeSwapped(swapEndian))
			if err != nil {
				ctx.Logf("Error while dereferencing: %s - skipping rule", err.Error())
				continue
			}
			lookupOffset = int64(readAddress)

			offsetAdjustValue := indirect.OffsetAdjustmentValue
			if indirect.OffsetAdjustmentIsRelative {
				offsetAdjustAddress := int64(offsetAddress) + offsetAdjustValue
				readAdjustAddress, err := readAnyUint(sr, int(offsetAdjustAddress), indirect.ByteWidth, indirect.Endianness)
				if err != nil {
					ctx.Logf("Error while dereferencing: %s - skipping rule", err.Error())
					continue
				}
				offsetAdjustValue = int64(readAdjustAddress)
			}

			switch indirect.OffsetAdjustmentType {
			case parser.AdjustmentAdd:
				lookupOffset = lookupOffset + offsetAdjustValue
			case parser.AdjustmentSub:
				lookupOffset = lookupOffset - offsetAdjustValue
			case parser.AdjustmentMul:
				lookupOffset = lookupOffset * offsetAdjustValue
			case parser.AdjustmentDiv:
				lookupOffset = lookupOffset / offsetAdjustValue
			}

		case parser.OffsetTypeDirect:
			lookupOffset = rule.Offset.Direct + pageOffset
		}

		if rule.Offset.IsRelative {
			lookupOffset += globalOffset
		}

		if lookupOffset < 0 || lookupOffset >= sr.Size() {
			ctx.Logf("we done goofed, lookupOffset %d is out of bounds, skipping %#v", lookupOffset, rule)
			continue
		}

		success := false

		switch rule.Kind.Family {
		case parser.KindFamilyInteger:
			ik, _ := rule.Kind.Data.(*parser.IntegerKind)

			if ik.MatchAny {
				success = true
			} else {
				targetValue, err := readAnyUint(sr, int(lookupOffset), ik.ByteWidth, ik.Endianness)
				if err != nil {
					ctx.Logf("in integer test, while reading target value: %s", err.Error())
					continue
				}

				if ik.DoAnd {
					targetValue &= ik.AndValue
				}

				switch ik.AdjustmentType {
				case parser.AdjustmentAdd:
					targetValue = uint64(int64(targetValue) + ik.AdjustmentValue)
				case parser.AdjustmentSub:
					targetValue = uint64(int64(targetValue) - ik.AdjustmentValue)
				case parser.AdjustmentMul:
					targetValue = uint64(int64(targetValue) * ik.AdjustmentValue)
				case parser.AdjustmentDiv:
					targetValue = uint64(int64(targetValue) / ik.AdjustmentValue)
				}

				switch ik.IntegerTest {
				case parser.IntegerTestEqual:
					success = targetValue == uint64(ik.Value)
				case parser.IntegerTestNotEqual:
					success = targetValue != uint64(ik.Value)
				case parser.IntegerTestLessThan:
					if ik.Signed {
						switch ik.ByteWidth {
						case 1:
							success = int8(targetValue) < int8(ik.Value)
						case 2:
							success = int16(targetValue) < int16(ik.Value)
						case 4:
							success = int32(targetValue) < int32(ik.Value)
						case 8:
							success = int64(targetValue) < int64(ik.Value)
						}
					} else {
						success = targetValue < uint64(ik.Value)
					}
				case parser.IntegerTestGreaterThan:
					if ik.Signed {
						switch ik.ByteWidth {
						case 1:
							success = int8(targetValue) > int8(ik.Value)
						case 2:
							success = int16(targetValue) > int16(ik.Value)
						case 4:
							success = int32(targetValue) > int32(ik.Value)
						case 8:
							success = int64(targetValue) > int64(ik.Value)
						}
					} else {
						success = targetValue > uint64(ik.Value)
					}
				}

				if success {
					globalOffset = lookupOffset + int64(ik.ByteWidth)
				}
			}

		case parser.KindFamilyString:
			sk, _ := rule.Kind.Data.(*parser.StringKind)

			matchLen := utils.StringTest(sr, lookupOffset, string(sk.Value), sk.Flags)
			success = matchLen >= 0

			if sk.Negate {
				success = !success
			} else {
				if success {
					globalOffset = lookupOffset + int64(matchLen)
				}
			}

		case parser.KindFamilySearch:
			sk, _ := rule.Kind.Data.(*parser.SearchKind)

			matchPos := utils.SearchTest(sr, lookupOffset, sk.MaxLen, string(sk.Value))
			success = matchPos >= 0

			if success {
				globalOffset = lookupOffset + matchPos + int64(len(sk.Value))
			}

		case parser.KindFamilyDefault:
			// default tests match if nothing has matched before
			if !everMatchedLevels[rule.Level] {
				success = true
			}

		case parser.KindFamilyUse:
			uk, _ := rule.Kind.Data.(*parser.UseKind)

			ctx.Logf("|====> using %s", uk.Page)

			subStrings, err := ctx.identifyInternal(sr, lookupOffset, uk.Page, uk.SwapEndian)
			if err != nil {
				return nil, err
			}
			outStrings = append(outStrings, subStrings...)

		case parser.KindFamilyClear:
			everMatchedLevels[rule.Level] = false
		}

		if success {
			descString := string(rule.Description)

			ctx.Logf("|==========> rule matched!")

			if descString != "" {
				outStrings = append(outStrings, descString)
			}
			matchedLevels[rule.Level] = true
			everMatchedLevels[rule.Level] = true
		} else {
			matchedLevels[rule.Level] = false
		}
	}

	ctx.Logf("|====> done identifying at %d using page %s (%d rules)", pageOffset, page, len(ctx.Book[page]))

	return outStrings, nil
}

func readAnyUint(sr *utils.SliceReader, j int, byteWidth int, endianness parser.Endianness) (uint64, error) {
	if int64(j+byteWidth) > sr.Size() {
		return 0, io.EOF
	}

	intBytes := make([]byte, byteWidth)
	n, err := sr.ReadAt(intBytes, int64(j))
	if n < byteWidth {
		if err != nil && err != io.EOF {
			return 0, err
		}
		return 0, io.EOF
	}

	var ret uint64

	switch byteWidth {
	case 1:
		ret = uint64(intBytes[0])
	case 2:
		ret = uint64(endianness.ByteOrder().Uint16(intBytes))
	case 4:
		ret = uint64(endianness.ByteOrder().Uint32(intBytes))
	case 8:
		ret = uint64(endianness.ByteOrder().Uint64(intBytes))
	default:
		return 0, fmt.Errorf("dunno how to read an uint of %d bytes", byteWidth)
	}

	return ret, nil
}
