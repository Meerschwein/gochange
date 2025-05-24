package main

import (
	"bytes"
	"os"
	"os/exec"
	"strings"
	"sync"

	"golang.org/x/tools/txtar"
)

func main() {
	paths := os.Args[1:]
	wg := sync.WaitGroup{}
	for _, path := range paths {
		wg.Add(1)
		go func() {
			defer wg.Done()
			formatFile(path)
		}()
	}
	wg.Wait()
}

func formatFile(path string) {
	orig, err := txtar.ParseFile(path)
	if err != nil {
		logErr(err)
		return
	}

	formatted := &txtar.Archive{}
	formatted.Comment = orig.Comment

	for _, f := range orig.Files {
		buf := bytes.Buffer{}

		if strings.HasSuffix(f.Name, ".go") || strings.HasSuffix(f.Name, ".go.golden") {
			cmd := exec.Command("gofmt")
			cmd.Stdin = bytes.NewReader(f.Data)
			cmd.Stdout = &buf

			err := cmd.Run()
			if err != nil {
				logErr(err)
				return
			}
		} else {
			buf.Write(f.Data)
		}

		formatted.Files = append(formatted.Files, txtar.File{
			Name: f.Name,
			Data: buf.Bytes(),
		})
	}

	f, err := os.Create(path)
	if err != nil {
		logErr(err)
		return
	}
	defer f.Close()

	_, err = f.Write(txtar.Format(formatted))
	if err != nil {
		logErr(err)
		return
	}
}

func logErr(err error) {
	if err != nil {
		os.Stderr.WriteString(err.Error() + "\n")
	}
}
