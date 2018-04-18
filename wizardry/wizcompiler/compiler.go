package wizcompiler

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	humanize "github.com/dustin/go-humanize"
	"github.com/itchio/wizardry/wizardry/wizparser"
	"github.com/pkg/errors"
)

type indentCallback func()

type ruleNode struct {
	id       int64
	rule     wizparser.Rule
	children []*ruleNode
}

type nodeEmitter func(node *ruleNode, defaultMarker string, prevSibling *ruleNode)

type PageUsage struct {
	EmitNormal  bool
	EmitSwapped bool
}

// Compile generates go code from a spellbook
func Compile(book wizparser.Spellbook, output string, chatty bool, emitComments bool, pkg string) error {
	startTime := time.Now()

	f, err := os.Create(output)
	if err != nil {
		return errors.WithStack(err)
	}

	fmt.Println("Generating into:", output)

	defer f.Close()

	lf := []byte("\n")
	oneIndent := []byte("  ")
	indentLevel := 0

	indent := func() {
		indentLevel++
	}

	outdent := func() {
		indentLevel--
	}

	emit := func(format string, args ...interface{}) {
		if format != "" {
			for i := 0; i < indentLevel; i++ {
				f.Write(oneIndent)
			}
			fmt.Fprintf(f, format, args...)
		}
		f.Write(lf)
	}

	emitLabel := func(label string) {
		// labels have one less indent than usual
		for i := 1; i < indentLevel; i++ {
			f.Write(oneIndent)
		}
		f.Write([]byte(label))
		f.WriteString(":")
		f.Write(lf)
	}

	withIndent := func(f indentCallback) {
		indent()
		f()
		outdent()
	}

	emit("// this file has been generated by github.com/itchio/wizardry")
	emit("// from a set of magic rules. you probably don't want to edit it by hand")
	emit("")

	emit("package %s", pkg)
	emit("")
	emit("import (")
	withIndent(func() {
		emit(strconv.Quote("fmt"))
		emit(strconv.Quote("encoding/binary"))
		emit(strconv.Quote("github.com/itchio/wizardry/wizardry"))
		emit(strconv.Quote("github.com/itchio/wizardry/wizardry/wizutil"))
	})
	emit(")")
	emit("")

	emit("// silence import errors, if we don't use string/search etc.")
	emit("var _ wizardry.StringTestFlags")
	emit("var _ fmt.State")

	emit("var l binary.ByteOrder=binary.LittleEndian")
	emit("var b binary.ByteOrder=binary.BigEndian")
	emit("var gt=wizardry.StringTest")
	emit("var ht=wizardry.SearchTest")
	emit("var t=true")
	emit("var f=false")
	emit("var tb=make([]byte, 8)")
	emit("")

	for _, byteWidth := range []byte{1, 2, 4, 8} {
		for _, endianness := range []wizparser.Endianness{wizparser.LittleEndian, wizparser.BigEndian} {
			retType := "uint64"

			emit("// reads an unsigned %d-bit %s integer", byteWidth*8, endianness)
			emit("func f%d%s(r *wizutil.SliceReader, off int64) (%s, bool) {", byteWidth, endiannessString(endianness, false), retType)
			withIndent(func() {
				emit("n,err:=r.ReadAt(tb,int64(off))")
				emit("if n<%d||err!=nil {return 0,f}", byteWidth)
				if byteWidth == 1 {
					emit("return %s(tb[0]),t", retType)
				} else {
					emit("return %s(%s.Uint%d(tb)),t", retType, endiannessString(endianness, false), byteWidth*8)
				}
			})
			emit("}")
			emit("")
		}
	}

	// sort pages
	var pages []string
	for page := range book {
		pages = append(pages, page)
	}
	sort.Strings(pages)

	usages := computePagesUsage(book)

	for _, page := range pages {
		nodes := treeify(book[page])
		usage := usages[page]

		for _, swapEndian := range []bool{false, true} {
			defaultSeed := 0

			if swapEndian {
				if !usage.EmitSwapped {
					continue
				}
			} else {
				if !usage.EmitNormal {
					continue
				}
			}

			emit("func Identify%s(r *wizutil.SliceReader, po int64) []string {", pageSymbol(page, swapEndian))
			withIndent(func() {
				emit("var out []string")
				emit("var ss []string; ss=ss[0:]")
				emit("var gf int64; gf&=gf") // globalOffset
				emit("var ra uint64; ra&=ra")
				emit("var rb uint64; rb&=rb")
				emit("var rc uint64; rc&=rc")
				emit("var rA int64; rA&=rA")
				emit("var k bool; k=!!k")
				emit("var l bool; l=!!l")
				emit("var m bool; m=!!m")
				emit("var d=make([]bool, 32); d[0]=!!d[0]")
				emit("")

				emit("a:=func (args... string) {")
				withIndent(func() {
					emit("out=append(out, args...)")
				})
				emit("}")

				var emitNode nodeEmitter

				emitNode = func(node *ruleNode, defaultMarker string, prevSiblingNode *ruleNode) {
					rule := node.rule

					canFail := false

					if emitComments {
						emit("// %s", rule.Line)
					}

					// don't bother emitting global offset if no direct children
					// have relative offsets. if grandchildren have relative offsets,
					// they'll be relative to their own parent
					emitGlobalOffset := false
					for _, child := range node.children {
						cof := child.rule.Offset
						if cof.IsRelative || (cof.OffsetType == wizparser.OffsetTypeIndirect && cof.Indirect.IsRelative) {
							emitGlobalOffset = true
							break
						}
					}

					var off Expression

					// if the previous node has exactly the same offset,
					// then we can reuse their offset without having to
					// recomput it (especially if it's indirect)
					reuseOffset := false
					if prevSiblingNode != nil {
						pr := prevSiblingNode.rule
						reuseOffset = pr.Offset.Equals(rule.Offset)
					}

					switch rule.Offset.OffsetType {
					case wizparser.OffsetTypeDirect:
						off = &BinaryOp{
							LHS:      &VariableAccess{"po"},
							Operator: OperatorAdd,
							RHS:      &NumberLiteral{rule.Offset.Direct},
						}
						if rule.Offset.IsRelative {
							off = &BinaryOp{
								LHS:      off,
								Operator: OperatorAdd,
								RHS:      &VariableAccess{"gf"},
							}
						}
					case wizparser.OffsetTypeIndirect:
						indirect := rule.Offset.Indirect

						var offsetAddress Expression = &NumberLiteral{indirect.OffsetAddress}
						if indirect.IsRelative {
							offsetAddress = &BinaryOp{
								LHS:      offsetAddress,
								Operator: OperatorAdd,
								RHS:      &VariableAccess{"gf"},
							}
						}

						if !reuseOffset {
							emit("ra,k=f%d%s(r,%s)",
								indirect.ByteWidth,
								endiannessString(indirect.Endianness, swapEndian),
								offsetAddress)
						}
						canFail = true
						emit("if !k {goto %s}", failLabel(node))
						var offsetAdjustValue Expression = &NumberLiteral{indirect.OffsetAdjustmentValue}

						if indirect.OffsetAdjustmentIsRelative {
							offsetAdjustAddress := fmt.Sprintf("%s + %s", offsetAddress, quoteNumber(indirect.OffsetAdjustmentValue))
							emit("rb,l=f%d%s(r,%s)",
								indirect.ByteWidth,
								endiannessString(indirect.Endianness, swapEndian),
								offsetAdjustAddress)
							emit("if !l {goto %s}", failLabel(node))
							offsetAdjustValue = &VariableAccess{"int64(rb)"}
						}

						off = &VariableAccess{"int64(ra)"}

						switch indirect.OffsetAdjustmentType {
						case wizparser.AdjustmentAdd:
							off = &BinaryOp{
								LHS:      off,
								Operator: OperatorAdd,
								RHS:      offsetAdjustValue,
							}
						case wizparser.AdjustmentSub:
							off = &BinaryOp{
								LHS:      off,
								Operator: OperatorSub,
								RHS:      offsetAdjustValue,
							}
						case wizparser.AdjustmentMul:
							off = &BinaryOp{
								LHS:      off,
								Operator: OperatorMul,
								RHS:      offsetAdjustValue,
							}
						case wizparser.AdjustmentDiv:
							off = &BinaryOp{
								LHS:      off,
								Operator: OperatorDiv,
								RHS:      offsetAdjustValue,
							}
						}

						if rule.Offset.IsRelative {
							off = &BinaryOp{
								LHS:      off,
								Operator: OperatorAdd,
								RHS:      &VariableAccess{"gf"},
							}
						}
					}

					off = off.Fold()

					switch rule.Kind.Family {
					case wizparser.KindFamilySwitch:
						sk, _ := rule.Kind.Data.(*wizparser.SwitchKind)

						emit("rc,m=f%d%s(r,%s)",
							sk.ByteWidth,
							endiannessString(sk.Endianness, swapEndian),
							off,
						)

						canFail = true
						emit("switch rc {")
						withIndent(func() {
							for _, c := range sk.Cases {
								emit("case %d: a(%s)", c.Value, strconv.Quote(string(c.Description)))
							}
							emit("default: {goto %s}", failLabel(node))
						})
						emit("}")

					case wizparser.KindFamilyInteger:
						ik, _ := rule.Kind.Data.(*wizparser.IntegerKind)

						if !ik.MatchAny {
							reuseSibling := false
							if prevSiblingNode != nil {
								pr := prevSiblingNode.rule
								if pr.Offset.Equals(rule.Offset) && pr.Kind.Family == wizparser.KindFamilyInteger {
									pik, _ := pr.Kind.Data.(*wizparser.IntegerKind)
									if pik.ByteWidth == ik.ByteWidth {
										reuseSibling = true
									}
								}
							}

							if !reuseSibling {
								emit("rc,m=f%d%s(r,%s)",
									ik.ByteWidth,
									endiannessString(ik.Endianness, swapEndian),
									off,
								)
							}

							lhs := "rc"

							operator := "=="
							switch ik.IntegerTest {
							case wizparser.IntegerTestEqual:
								operator = "=="
							case wizparser.IntegerTestNotEqual:
								operator = "!="
							case wizparser.IntegerTestLessThan:
								operator = "< "
							case wizparser.IntegerTestGreaterThan:
								operator = ">"
							}

							if ik.Signed && (ik.IntegerTest == wizparser.IntegerTestGreaterThan || ik.IntegerTest == wizparser.IntegerTestLessThan) {
								lhs = fmt.Sprintf("int64(int%d(%s))", ik.ByteWidth*8, lhs)
							}

							if ik.DoAnd {
								lhs = fmt.Sprintf("%s&%s", lhs, quoteNumber(int64(ik.AndValue)))
							}

							switch ik.AdjustmentType {
							case wizparser.AdjustmentAdd:
								lhs = fmt.Sprintf("(%s+%s)", lhs, quoteNumber(ik.AdjustmentValue))
							case wizparser.AdjustmentSub:
								lhs = fmt.Sprintf("(%s-%s)", lhs, quoteNumber(ik.AdjustmentValue))
							case wizparser.AdjustmentMul:
								lhs = fmt.Sprintf("(%s*%s)", lhs, quoteNumber(ik.AdjustmentValue))
							case wizparser.AdjustmentDiv:
								lhs = fmt.Sprintf("(%s/%s)", lhs, quoteNumber(ik.AdjustmentValue))
							}

							rhs := quoteNumber(ik.Value)

							ruleTest := fmt.Sprintf("m&&%s%s%s", lhs, operator, rhs)
							canFail = true
							emit("if !(%s) {goto %s}", ruleTest, failLabel(node))
						}
						if emitGlobalOffset {
							gfValue := &BinaryOp{
								LHS:      off,
								Operator: OperatorAdd,
								RHS:      &NumberLiteral{int64(ik.ByteWidth)},
							}
							emit("gf=%s", gfValue.Fold())
						}
					case wizparser.KindFamilyString:
						sk, _ := rule.Kind.Data.(*wizparser.StringKind)
						emit("rA = gt(r,%s,%s,%d)", off, strconv.Quote(string(sk.Value)), sk.Flags)
						canFail = true
						if sk.Negate {
							emit("if rA>=0 {goto %s}", failLabel(node))
						} else {
							emit("if rA<0 {goto %s}", failLabel(node))
						}
						if emitGlobalOffset {
							gfValue := &BinaryOp{
								LHS:      off,
								Operator: OperatorAdd,
								RHS:      &VariableAccess{"rA"},
							}
							emit("gf=%s", gfValue.Fold())
						}

					case wizparser.KindFamilySearch:
						sk, _ := rule.Kind.Data.(*wizparser.SearchKind)
						emit("rA=ht(r,%s,%s,%s)", off, quoteNumber(int64(sk.MaxLen)), strconv.Quote(string(sk.Value)))
						canFail = true
						emit("if rA<0 {goto %s}", failLabel(node))
						if emitGlobalOffset {
							gfValue := &BinaryOp{
								LHS:      off,
								Operator: OperatorAdd,
								RHS: &BinaryOp{
									LHS:      &VariableAccess{"rA"},
									Operator: OperatorAdd,
									RHS:      &NumberLiteral{int64(len(sk.Value))},
								},
							}
							emit("gf=%s", gfValue.Fold())
						}

					case wizparser.KindFamilyUse:
						uk, _ := rule.Kind.Data.(*wizparser.UseKind)
						emit("a(Identify%s(r,%s)...)", pageSymbol(uk.Page, uk.SwapEndian), off)

					case wizparser.KindFamilyName:
						// do nothing, pretty much

					case wizparser.KindFamilyClear:
						// reset defaultMarker for this level
						if defaultMarker == "" {
							panic("compiler error: nil defaultMarker for clear rule")
						}
						emit("%s=f", defaultMarker)

					case wizparser.KindFamilyDefault:
						// only succeed if defaultMarker is unset
						// (so, fail if it's set)
						if defaultMarker == "" {
							panic("compiler error: nil defaultMarker for default rule")
						}
						canFail = true
						emit("if %s {goto %s}", defaultMarker, failLabel(node))
						if emitGlobalOffset {
							emit("gf=%s", off)
						}

					default:
						emit("// fixme: unhandled %s", rule.Kind)
						canFail = true
						emit("goto %s", failLabel(node))
					}

					if chatty {
						emit("fmt.Printf(\"%%s\\n\", %s)", strconv.Quote(rule.Line))
					}
					if len(rule.Description) > 0 {
						emit("a(%s)", strconv.Quote(string(rule.Description)))
					}

					numChildren := len(node.children)
					childDefaultMarker := ""

					if numChildren > 0 {
						for _, child := range node.children {
							if child.rule.Kind.Family == wizparser.KindFamilyDefault {
								childDefaultMarker = fmt.Sprintf("d[%d]", rule.Level)
								defaultSeed++
								emit("%s=f", childDefaultMarker)
								break
							}
						}

						var prevSibling = node
						for _, child := range node.children {
							emitNode(child, childDefaultMarker, prevSibling)
							prevSibling = child
						}
					}

					if defaultMarker != "" {
						emit("%s=t", defaultMarker)
					}

					if canFail {
						emitLabel(failLabel(node))
					}
				}

				for _, node := range nodes {
					switchify(node)

					emitNode(node, "", nil)
				}

				emit("return out")
			})
			emit("}")
			emit("")
		}

	}

	fmt.Printf("Compiled in %s\n", time.Since(startTime))

	fSize, _ := f.Seek(0, os.SEEK_CUR)
	fmt.Printf("Generated code is %s\n", humanize.IBytes(uint64(fSize)))

	return nil
}

func pageSymbol(page string, swapEndian bool) string {
	result := ""
	for _, token := range strings.Split(page, "-") {
		result += strings.Title(token)
	}

	if swapEndian {
		result += "__Swapped"
	}

	return result
}

func endiannessString(en wizparser.Endianness, swapEndian bool) string {
	if en.MaybeSwapped(swapEndian) == wizparser.BigEndian {
		return "b"
	}
	return "l"
}

func quoteNumber(number int64) string {
	return fmt.Sprintf("%d", number)
}

func failLabel(node *ruleNode) string {
	return fmt.Sprintf("f%x", node.id)
}
