package testutil

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/meerschwein/gochange/internal/testutil/assert"
)

func EachFile(t *testing.T, glob string, f func(t *testing.T, input string)) {
	// adapted from
	// https://github.com/earthboundkid/be/blob/v0.24.1/testfile/testfile.go#L161

	t.Helper()

	matches, err := filepath.Glob(glob)
	assert.NilErr(t, err)

	for _, match := range matches {
		name := filepath.Base(match)
		name = name[:len(name)-len(filepath.Ext(name))] // Strip extension
		t.Run(name, func(t *testing.T) {
			f(t, match)
		})
	}
}

func CopyToTmp(t *testing.T, src fs.FS) string {
	// adapted from
	// https://cs.opensource.google/go/x/tools/+/refs/tags/v0.30.0:internal/testfiles/testfiles.go;l=46

	t.Helper()

	dstdir := t.TempDir()

	err := os.CopyFS(dstdir, src)
	assert.NilErr(t, err)

	return dstdir
}
