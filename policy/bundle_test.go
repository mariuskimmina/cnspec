package policy

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBundleFromPaths(t *testing.T) {
	t.Run("mql bundle file with multiple queries", func(t *testing.T) {
		bundle, err := BundleFromPaths("../examples/example.mql.yaml")
		require.NoError(t, err)
		require.NotNil(t, bundle)
		assert.Len(t, bundle.Queries, 1)
		require.Len(t, bundle.Policies, 1)
		require.Len(t, bundle.Policies[0].Groups, 1)
		assert.Len(t, bundle.Policies[0].Groups[0].Checks, 3)
		assert.Len(t, bundle.Policies[0].Groups[0].Queries, 2)
	})

	t.Run("mql bundle file with multiple policies and queries", func(t *testing.T) {
		bundle, err := BundleFromPaths("../examples/complex.mql.yaml")
		require.NoError(t, err)
		require.NotNil(t, bundle)
		assert.Len(t, bundle.Queries, 5)
		assert.Len(t, bundle.Policies, 2)
	})

	t.Run("mql bundle file with directory structure", func(t *testing.T) {
		bundle, err := BundleFromPaths("../examples/directory")
		require.NoError(t, err)
		require.NotNil(t, bundle)
		assert.Len(t, bundle.Queries, 5)
		assert.Len(t, bundle.Policies, 2)
	})
}

func TestPolicyBundleSort(t *testing.T) {
	pb, err := BundleFromPaths("./testdata/policybundle-deps.mql.yaml")
	require.NoError(t, err)
	assert.Equal(t, 3, len(pb.Policies))
	pbm := pb.ToMap()

	policies, err := pbm.PoliciesSortedByDependency()
	require.NoError(t, err)
	assert.Equal(t, 3, len(policies))

	assert.Equal(t, "//policy.api.mondoo.app/policies/debian-10-level-1-server", policies[0].Mrn)
	assert.Equal(t, "//captain.api.mondoo.app/spaces/adoring-moore-542492", policies[1].Mrn)
	assert.Equal(t, "//assets.api.mondoo.app/spaces/adoring-moore-542492/assets/1dKBiOi5lkI2ov48plcowIy8WEl", policies[2].Mrn)
}

func TestBundleCompile(t *testing.T) {
	bundle, err := BundleFromPaths("../examples/complex.mql.yaml")
	require.NoError(t, err)
	require.NotNil(t, bundle)

	bundlemap, err := bundle.Compile(context.Background(), nil)
	require.NoError(t, err)
	require.NotNil(t, bundlemap)

	base := bundlemap.Queries["//local.cnspec.io/run/local-execution/queries/uname"]
	require.NotNil(t, base, "variant base cannot be nil")

	variant1 := bundlemap.Queries["//local.cnspec.io/run/local-execution/queries/unix-uname"]
	require.NotNil(t, variant1, "variant cannot be nil")

	assert.Equal(t, base.Title, variant1.Title)
}
