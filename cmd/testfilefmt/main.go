package main

import (
	"bytes"
	"os"
	"os/exec"
	"strings"

	"golang.org/x/tools/txtar"
)

func main() {
	path := os.Args[1]
	orig, err := txtar.ParseFile(path)
	must(err)

	formatted := &txtar.Archive{}
	formatted.Comment = orig.Comment

	for _, f := range orig.Files {
		buf := bytes.Buffer{}

		if strings.HasSuffix(f.Name, ".go") || strings.HasSuffix(f.Name, ".go.golden") {
			cmd := exec.Command("gofmt")
			cmd.Stdin = bytes.NewReader(f.Data)
			cmd.Stdout = &buf

			err := cmd.Run()
			must(err)
		} else {
			buf.Write(f.Data)
		}

		formatted.Files = append(formatted.Files, txtar.File{
			Name: f.Name,
			Data: buf.Bytes(),
		})
	}

	f, err := os.Create(path)
	must(err)
	defer f.Close()

	_, err = f.Write(txtar.Format(formatted))
	must(err)
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}
