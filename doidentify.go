package main

import (
	"fmt"
	"os"

	"github.com/9uanhuo/wizardry/interpreter"
	"github.com/9uanhuo/wizardry/parser"
	"github.com/9uanhuo/wizardry/utils"
	"github.com/pkg/errors"
)

func doIdentify() error {
	magdir := *identifyArgs.magdir

	NoLogf := func(format string, args ...interface{}) {}

	Logf := func(format string, args ...interface{}) {
		fmt.Println(fmt.Sprintf(format, args...))
	}

	pctx := &parser.ParseContext{
		Logf: NoLogf,
	}

	if *appArgs.debugParser {
		pctx.Logf = Logf
	}

	book := make(parser.Spellbook)
	err := pctx.ParseAll(magdir, book)
	if err != nil {
		return errors.WithStack(err)
	}

	target := *identifyArgs.target
	targetReader, err := os.Open(target)
	if err != nil {
		panic(err)
	}

	defer targetReader.Close()

	stat, _ := targetReader.Stat()

	ictx := &interpreter.InterpretContext{
		Logf: NoLogf,
		Book: book,
	}

	if *appArgs.debugInterpreter {
		ictx.Logf = Logf
	}

	sr := utils.NewSliceReader(targetReader, 0, stat.Size())

	result, err := ictx.Identify(sr)
	if err != nil {
		panic(err)
	}

	fmt.Printf("%s: %s\n", target, utils.MergeStrings(result))

	return nil
}
