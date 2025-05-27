package gochange_test

import (
	"testing"

	"github.com/meerschwein/gochange/internal/gochange"
	"github.com/meerschwein/gochange/internal/testutil"
	"github.com/meerschwein/gochange/internal/testutil/assert"
	"golang.org/x/tools/go/analysis/analysistest"
	"golang.org/x/tools/txtar"
)

func init() {
	gochange.SwallowPanic = false
}

func TestAllCases(t *testing.T) {
	testutil.EachFile(t, "./testdata/full/*.txtar", func(t *testing.T, input string) {
		t.Parallel()

		ar, err := txtar.ParseFile(input)
		assert.NilErr(t, err)

		fs, err := txtar.FS(ar)
		assert.NilErr(t, err)

		dir := testutil.CopyToTmp(t, fs)

		analysistest.RunWithSuggestedFixes(t, dir, gochange.Analyzer, "./...")
	})
}
