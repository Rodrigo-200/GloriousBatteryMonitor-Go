package main

import (
	"fmt"
	"go/scanner"
	"go/token"
	"io/ioutil"
)

func main() {
	src, err := ioutil.ReadFile("d:/GloriousBatteryMonitor-clean/main.go")
	if err != nil {
		panic(err)
	}
	fset := token.NewFileSet()
	file := fset.AddFile("main.go", -1, len(src))
	var s scanner.Scanner
	s.Init(file, src, nil, scanner.ScanComments)
	for {
		posTok, tok, lit := s.Scan()
		if tok == token.EOF {
			break
		}
		pos := fset.Position(posTok)
		if pos.Line >= 2138 && pos.Line <= 2450 {
			fmt.Printf("%5d:%4d %-12s %q\n", pos.Line, pos.Column, tok.String(), lit)
		}
	}
}
