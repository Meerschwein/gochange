package main

import (
	"github.com/meerschwein/gochange/internal/gochange"
	"golang.org/x/tools/go/analysis/singlechecker"
)

func main() {
	singlechecker.Main(gochange.Analyzer)
}
