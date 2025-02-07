package bundle

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLintPass(t *testing.T) {
	file := "../../examples/example.mql.yaml"
	rootDir := "../../examples"
	results, err := Lint(file)
	require.NoError(t, err)

	assert.Equal(t, 1, len(results.BundleLocations))
	assert.Equal(t, 0, len(results.Entries))
	assert.False(t, results.HasError())

	report, err := results.sarifReport(rootDir)
	require.NoError(t, err)

	assert.Equal(t, 1, len(report.Runs))
	assert.Equal(t, len(rules), len(report.Runs[0].Tool.Driver.Rules))
	assert.Equal(t, 0, len(report.Runs[0].Results))

	data, err := results.ToSarif(rootDir)
	require.NoError(t, err)
	assert.True(t, len(data) > 0)
}

func TestLintPassComplex(t *testing.T) {
	file := "../../examples/complex.mql.yaml"
	rootDir := "../../examples"
	results, err := Lint(file)
	require.NoError(t, err)

	assert.Equal(t, 1, len(results.BundleLocations))
	assert.Equal(t, 0, len(results.Entries))
	assert.False(t, results.HasError())

	report, err := results.sarifReport(rootDir)
	require.NoError(t, err)

	assert.Equal(t, 1, len(report.Runs))
	assert.Equal(t, len(rules), len(report.Runs[0].Tool.Driver.Rules))
	assert.Equal(t, 0, len(report.Runs[0].Results))

	data, err := results.ToSarif(rootDir)
	require.NoError(t, err)
	assert.True(t, len(data) > 0)
}

func TestLintFail(t *testing.T) {
	file := "./testdata/failing_lint.mql.yaml"
	rootDir := "./testdata"
	results, err := Lint(file)
	require.NoError(t, err)

	assert.Equal(t, 1, len(results.BundleLocations))
	assert.Equal(t, 5, len(results.Entries))
	assert.True(t, results.HasError())

	report, err := results.sarifReport(rootDir)
	require.NoError(t, err)

	assert.Equal(t, 1, len(report.Runs))
	assert.Equal(t, len(rules), len(report.Runs[0].Tool.Driver.Rules))
	assert.Equal(t, 5, len(report.Runs[0].Results))

	data, err := results.ToSarif(rootDir)
	require.NoError(t, err)
	assert.True(t, len(data) > 0)
}
