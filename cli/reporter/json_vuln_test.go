package reporter

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mondoo.com/cnquery/shared"
	"go.mondoo.com/cnquery/upstream/mvd"
)

func TestJsonConverter(t *testing.T) {
	reportRaw, err := os.ReadFile("./testdata/mondoo-debug-vulnReport.json")
	require.NoError(t, err)

	report := &mvd.VulnReport{}
	err = json.Unmarshal(reportRaw, report)
	require.NoError(t, err)

	buf := bytes.Buffer{}
	writer := shared.IOWriter{Writer: &buf}
	err = VulnReportToJSON("index.docker.io/ubutnu:focal-20220113", report, &writer)
	require.NoError(t, err)

	assert.Contains(t, buf.String(), "\"cves\":[\"CVE-2021-43618\"]")
}
