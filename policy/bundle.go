package policy

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/pkg/errors"
	"github.com/rs/zerolog/log"
	"go.mondoo.com/cnquery/checksums"
	"go.mondoo.com/cnquery/explorer"
	"go.mondoo.com/cnquery/llx"
	"go.mondoo.com/cnquery/logger"
	"go.mondoo.com/cnquery/mrn"
	"sigs.k8s.io/yaml"
)

const (
	MRN_RESOURCE_QUERY     = "queries"
	MRN_RESOURCE_POLICY    = "policies"
	MRN_RESOURCE_ASSET     = "assets"
	MRN_RESOURCE_FRAMEWORK = "framework"
	MRN_RESOURCE_CONTROL   = "controls"
)

// BundleExecutionChecksum creates a combined execution checksum from a policy
// and framework. Either may be nil.
func BundleExecutionChecksum(policy *Policy, framework *Framework) string {
	res := checksums.New
	if policy != nil {
		res = res.Add(policy.GraphExecutionChecksum)
	}
	if framework != nil {
		res = res.Add(framework.GraphExecutionChecksum)
	}
	return res.String()
}

// BundleFromPaths loads a single policy bundle file or a bundle that
// was split into multiple files into a single PolicyBundle struct
func BundleFromPaths(paths ...string) (*Bundle, error) {
	// load all the source files
	resolvedFilenames, err := WalkPolicyBundleFiles(paths...)
	if err != nil {
		log.Error().Err(err).Msg("could not resolve bundle files")
		return nil, err
	}

	// aggregate all files into a single policy bundle
	aggregatedBundle, err := aggregateFilesToBundle(resolvedFilenames)
	if err != nil {
		log.Debug().Err(err).Msg("could merge bundle files")
		return nil, err
	}

	logger.DebugDumpYAML("resolved_mql_bundle.mql", aggregatedBundle)
	return aggregatedBundle, nil
}

// WalkPolicyBundleFiles iterates over all provided filenames and
// checks if the name is a file or a directory. If the filename
// is a directory, it walks the directory recursively
func WalkPolicyBundleFiles(filenames ...string) ([]string, error) {
	// resolve file names
	resolvedFilenames := []string{}
	for i := range filenames {
		filename := filenames[i]
		fi, err := os.Stat(filename)
		if err != nil {
			return nil, errors.Wrap(err, "could not load policy bundle file: "+filename)
		}

		if fi.IsDir() {
			filepath.WalkDir(filename, func(path string, d fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				// we ignore directories because WalkDir already walks them
				if d.IsDir() {
					return nil
				}

				// only consider .yaml|.yml files
				if strings.HasSuffix(d.Name(), ".mql.yaml") || strings.HasSuffix(d.Name(), ".mql.yml") {
					resolvedFilenames = append(resolvedFilenames, path)
				}

				return nil
			})
		} else {
			resolvedFilenames = append(resolvedFilenames, filename)
		}
	}

	return resolvedFilenames, nil
}

// aggregateFilesToBundle iterates over all provided files and loads its content.
// It assumes that all provided files are checked upfront and are not a directory
func aggregateFilesToBundle(paths []string) (*Bundle, error) {
	// iterate over all files, load them and merge them
	mergedBundle := &Bundle{}

	for i := range paths {
		path := paths[i]
		log.Debug().Str("path", path).Msg("loading policy bundle file")
		bundle, err := bundleFromSingleFile(path)
		if err != nil {
			return nil, errors.Wrap(err, "could not load file: "+path)
		}

		mergedBundle = aggregateBundles(mergedBundle, bundle)
	}

	return mergedBundle, nil
}

// bundleFromSingleFile loads a policy bundle from a single file
func bundleFromSingleFile(path string) (*Bundle, error) {
	bundleData, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	return BundleFromYAML(bundleData)
}

// aggregateBundles combines two PolicyBundle and merges the data additive into one
// single PolicyBundle structure
func aggregateBundles(a *Bundle, b *Bundle) *Bundle {
	res := &Bundle{}

	res.OwnerMrn = a.OwnerMrn
	if b.OwnerMrn != "" {
		res.OwnerMrn = b.OwnerMrn
	}

	// merge in a
	for i := range a.Policies {
		res.Policies = append(res.Policies, a.Policies[i])
	}

	for i := range a.Props {
		res.Props = append(res.Props, a.Props[i])
	}

	for i := range a.Queries {
		res.Queries = append(res.Queries, a.Queries[i])
	}

	for i := range a.Frameworks {
		res.Frameworks = append(res.Frameworks, a.Frameworks[i])
	}

	// merge in b
	for i := range b.Policies {
		res.Policies = append(res.Policies, b.Policies[i])
	}

	for i := range b.Props {
		res.Props = append(res.Props, b.Props[i])
	}

	for i := range b.Queries {
		res.Queries = append(res.Queries, b.Queries[i])
	}

	for i := range b.Frameworks {
		res.Frameworks = append(res.Frameworks, b.Frameworks[i])
	}

	return res
}

// BundleFromYAML create a policy bundle from yaml contents
func BundleFromYAML(data []byte) (*Bundle, error) {
	var res Bundle
	err := yaml.Unmarshal(data, &res)

	// FIXME: DEPRECATED, remove in v9.0 vv
	// first we want to see if this looks like a new Bundle. If it does, just
	// return it and we are done. But if it doesn't, then we will try to
	// parse it as a v7 bundle instead and see if that works.
	if err == nil {
		// Only new policies and bundles support logic where you don't have
		// any policy in the bundle at all.
		if len(res.Policies) == 0 {
			return &res, nil
		}

		// If the policy as the groups field, then we know it's a new one
		for i := range res.Policies {
			cur := res.Policies[i]
			if cur.Groups != nil {
				return &res, nil
			}
		}
	}

	// We either got here because there is an error, or because it may also
	// be an old bundle. So let's try to parse it as an old bundle.
	var altRes DeprecatedV7_Bundle
	altErr := yaml.Unmarshal(data, &altRes)
	if altErr == nil && len(altRes.Policies) != 0 {
		// we still want to do a sanity check that this is a valid v7 policy
		for i := range altRes.Policies {
			cur := altRes.Policies[i]
			if cur.Specs != nil {
				return altRes.ToV8(), nil
			}
		}
	}

	// This is the final fallthrough, where we either have an error or
	// it's not a valid v7 policy
	return &res, err
	// ^^
}

// ToYAML returns the policy bundle as yaml
func (p *Bundle) ToYAML() ([]byte, error) {
	return yaml.Marshal(p)
}

func (p *Bundle) SourceHash() (string, error) {
	raw, err := p.ToYAML()
	if err != nil {
		return "", err
	}
	c := checksums.New
	c = c.Add(string(raw))
	return c.String(), nil
}

// ToMap turns the PolicyBundle into a PolicyBundleMap
// dataLake (optional) may be used to provide queries/policies not found in the bundle
func (p *Bundle) ToMap() *PolicyBundleMap {
	res := NewPolicyBundleMap(p.OwnerMrn)

	for i := range p.Policies {
		c := p.Policies[i]
		res.Policies[c.Mrn] = c
	}

	for i := range p.Frameworks {
		c := p.Frameworks[i]
		res.Frameworks[c.Mrn] = c
	}

	for i := range p.Queries {
		c := p.Queries[i]
		res.Queries[c.Mrn] = c
	}

	for i := range p.Props {
		c := p.Props[i]
		res.Props[c.Mrn] = c
	}

	return res
}

// FilterPolicies only keeps the given policy UIDs or MRNs and removes every other one.
// If a given policy has a MRN set (but no UID) it will try to get the UID from the MRN
// and also filter by that criteria.
// If the list of IDs is empty this function doesn't do anything.
// This function does not remove orphaned queries from the bundle.
func (p *Bundle) FilterPolicies(IDs []string) {
	if p == nil || len(IDs) == 0 {
		return
	}

	log.Debug().Msg("filter policies for asset")
	valid := make(map[string]struct{}, len(IDs))
	for i := range IDs {
		valid[IDs[i]] = struct{}{}
	}

	var cur *Policy
	var res []*Policy
	for i := range p.Policies {
		cur = p.Policies[i]

		if cur.Mrn != "" {
			if _, ok := valid[cur.Mrn]; ok {
				res = append(res, cur)
				continue
			}

			uid, _ := mrn.GetResource(cur.Mrn, MRN_RESOURCE_POLICY)
			if _, ok := valid[uid]; ok {
				res = append(res, cur)
				continue
			}

			log.Debug().Str("policy", cur.Mrn).Msg("policy does not match user-provided filter")
			// if we have a MRN we do not check the UID
			continue
		}

		if _, ok := valid[cur.Uid]; ok {
			res = append(res, cur)
			continue
		}
		log.Debug().Str("policy", cur.Uid).Msg("policy does not match user-provided filter")
	}

	p.Policies = res
}

func (p *Bundle) RemoveOrphaned() {
	panic("Not yet implemented, please open an issue at https://github.com/mondoohq/cnspec")
}

// Clean the policy bundle to turn a few nil fields into empty fields for consistency
func (p *Bundle) Clean() *Bundle {
	for i := range p.Policies {
		policy := p.Policies[i]
		if policy.ComputedFilters == nil {
			policy.ComputedFilters = &explorer.Filters{
				Items: map[string]*explorer.Mquery{},
			}
		}
	}

	// consistency between db backends
	if p.Props != nil && len(p.Props) == 0 {
		p.Props = nil
	}

	return p
}

// Add another policy bundle into this. No duplicate policies, queries, or
// properties are allowed and will lead to an error. Both bundles must have
// MRNs for everything. OwnerMRNs must be identical as well.
func (p *Bundle) AddBundle(other *Bundle) error {
	if p.OwnerMrn == "" {
		p.OwnerMrn = other.OwnerMrn
	} else if p.OwnerMrn != other.OwnerMrn {
		return errors.New("when combining policy bundles the owner MRNs must be identical")
	}

	for i := range other.Policies {
		c := other.Policies[i]
		if c.Mrn == "" {
			return errors.New("source policy bundle that is added has missing policy MRNs")
		}

		for j := range p.Policies {
			if p.Policies[j].Mrn == c.Mrn {
				return errors.New("cannot combine policy bundles, duplicate policy: " + c.Mrn)
			}
		}

		p.Policies = append(p.Policies, c)
	}

	for i := range other.Queries {
		c := other.Queries[i]
		if c.Mrn == "" {
			return errors.New("source policy bundle that is added has missing query MRNs")
		}

		for j := range p.Queries {
			if p.Queries[j].Mrn == c.Mrn {
				return errors.New("cannot combine policy bundles, duplicate query: " + c.Mrn)
			}
		}

		p.Queries = append(p.Queries, c)
	}

	for i := range other.Props {
		c := other.Props[i]
		if c.Mrn == "" {
			return errors.New("source policy bundle that is added has missing property MRNs")
		}

		for j := range p.Props {
			if p.Props[j].Mrn == c.Mrn {
				return errors.New("cannot combine policy bundles, duplicate property: " + c.Mrn)
			}
		}

		p.Props = append(p.Props, c)
	}

	return nil
}

// PolicyMRNs in this bundle
func (p *Bundle) PolicyMRNs() []string {
	mrns := []string{}
	for i := range p.Policies {
		// ensure a mrn is generated
		p.Policies[i].RefreshMRN(p.OwnerMrn)
		mrns = append(mrns, p.Policies[i].Mrn)
	}
	return mrns
}

// SortContents of this policy bundle sorts Queries and Policies by MRNs
func (p *Bundle) SortContents() {
	sort.SliceStable(p.Queries, func(i, j int) bool {
		return p.Queries[i].Mrn < p.Queries[j].Mrn
	})

	sort.SliceStable(p.Policies, func(i, j int) bool {
		return p.Policies[i].Mrn < p.Policies[j].Mrn
	})
}

type docsPrinter interface {
	Write(section string, data string)
}

type nocodeWriter struct {
	docsWriter
}

var reCode = regexp.MustCompile("(?s)`[^`]+`|```.*```")

func (n nocodeWriter) Write(section string, data string) {
	n.docsWriter.Write(section, reCode.ReplaceAllString(data, ""))
}

type docsWriter struct {
	out io.Writer
}

func (n docsWriter) Write(section string, data string) {
	if data == "" {
		return
	}
	io.WriteString(n.out, section)
	io.WriteString(n.out, ": ")
	io.WriteString(n.out, data)
	n.out.Write([]byte{'\n'})
}

func extractQueryDocs(query *explorer.Mquery, w docsPrinter, noIDs bool) {
	if !noIDs {
		w.Write("query ID", query.Uid)
		w.Write("query Mrn", query.Mrn)
	}

	w.Write("query title", query.Title)
	w.Write("query description", query.Desc)

	if query.Docs != nil {
		w.Write("query description", query.Docs.Desc)
		w.Write("query audit", query.Docs.Audit)
		if query.Docs.Remediation != nil {
			for i := range query.Docs.Remediation.Items {
				remediation := query.Docs.Remediation.Items[i]
				if noIDs {
					w.Write("query remediation", remediation.Desc)
				} else {
					w.Write("query remediation "+remediation.Id, remediation.Desc)
				}
			}
		}
	}
}

func extractPropertyDocs(prop *explorer.Property, w docsPrinter, noIDs bool) {
	if !noIDs {
		w.Write("property ID", prop.Uid)
		w.Write("property Mrn", prop.Mrn)
	}

	w.Write("property title", prop.Title)
	w.Write("property description", prop.Desc)
}

func extractPolicyDocs(policy *Policy, w docsPrinter, noIDs bool) {
	if !noIDs {
		w.Write("policy ID", policy.Uid)
		w.Write("policy Mrn", policy.Mrn)
	}

	w.Write("policy summary", policy.Summary)

	for i := range policy.Props {
		extractPropertyDocs(policy.Props[i], w, noIDs)
	}
	if policy.Docs != nil {
		w.Write("policy description", policy.Docs.Desc)
	}

	for g := range policy.Groups {
		group := policy.Groups[g]

		w.Write("group summary", group.Title)

		if group.Docs != nil {
			w.Write("group description", group.Docs.Desc)
		}

		for i := range group.Queries {
			extractQueryDocs(group.Queries[i], w, noIDs)
		}
		for i := range group.Checks {
			extractQueryDocs(group.Checks[i], w, noIDs)
		}
	}
}

func (p *Bundle) ExtractDocs(out io.Writer, noIDs bool, noCode bool) {
	if p == nil {
		return
	}

	var printer docsPrinter
	if noCode {
		printer = nocodeWriter{docsWriter: docsWriter{out}}
	} else {
		printer = docsWriter{out}
	}

	for i := range p.Queries {
		extractQueryDocs(p.Queries[i], printer, noIDs)
	}
	for i := range p.Props {
		extractPropertyDocs(p.Props[i], printer, noIDs)
	}
	for i := range p.Policies {
		extractPolicyDocs(p.Policies[i], printer, noIDs)
	}
}

func (p *bundleCache) ensureNoCyclesInVariants(queries []*explorer.Mquery) error {
	// Gather all top-level queries with variants
	queriesMap := map[string]*explorer.Mquery{}
	for _, q := range queries {
		if q == nil {
			continue
		}
		if q.Mrn == "" {
			// This should never happen. This function is called after all
			// queries have their MRNs set.
			panic("BUG: expected query MRN to be set for variant cycle detection")
		}
		queriesMap[q.Mrn] = q
	}

	if err := detectVariantCycles(queriesMap); err != nil {
		return err
	}
	return nil
}

type nodeVisitStatus byte

const (
	// NEW is the initial state of visiting a node. It means that the node has not been visited yet.
	NEW nodeVisitStatus = iota
	// ACTIVE means that the node is currently being visited. If we encounter a node that is in
	// ACTIVE state, it means that we have a cycle.
	ACTIVE
	// VISITED means that the node has been visited.
	VISITED
)

var ErrVariantCycleDetected = errors.New("variant cycle detected")

func detectVariantCycles(queries map[string]*explorer.Mquery) error {
	statusMap := map[string]nodeVisitStatus{}
	for _, query := range queries {
		err := detectVariantCyclesDFS(query.Mrn, statusMap, queries)
		if err != nil {
			return err
		}
	}
	return nil
}

func detectVariantCyclesDFS(mrn string, statusMap map[string]nodeVisitStatus, queries map[string]*explorer.Mquery) error {
	q := queries[mrn]
	if q == nil {
		return nil
	}
	s := statusMap[mrn]
	if s == VISITED {
		return nil
	} else if s == ACTIVE {
		return ErrVariantCycleDetected
	}
	statusMap[q.Mrn] = ACTIVE
	for _, variant := range q.Variants {
		if variant.Mrn == "" {
			// This should never happen. This function is called after all
			// queries have their MRNs set.
			panic("BUG: expected variant MRN to be set for variant cycle detection")
		}
		v := queries[variant.Mrn]
		if v == nil {
			continue
		}
		err := detectVariantCyclesDFS(v.Mrn, statusMap, queries)
		if err != nil {
			return err
		}
	}
	statusMap[q.Mrn] = VISITED
	return nil
}

// Compile PolicyBundle into a PolicyBundleMap
// Does 4 things:
// 1. turns policy bundle into a map for easier access
// 2. compile all queries. store code in the bundle map
// 3. validation of all contents
// 4. generate MRNs for all policies, queries, and properties and updates referencing local fields
// 5. snapshot all queries into the packs
// 6. make queries public that are only embedded
func (p *Bundle) Compile(ctx context.Context, library Library) (*PolicyBundleMap, error) {
	ownerMrn := p.OwnerMrn
	if ownerMrn == "" {
		// this only happens for local bundles where queries have no mrn yet
		ownerMrn = "//local.cnspec.io/run/local-execution"
	}

	// FIXME: DEPRECATED, remove in v9.0 vv
	p.DeprecatedV7Conversions()
	// ^^

	cache := &bundleCache{
		ownerMrn:    ownerMrn,
		bundle:      p,
		uid2mrn:     map[string]string{},
		lookupProp:  map[string]explorer.PropertyRef{},
		lookupQuery: map[string]*explorer.Mquery{},
		codeBundles: map[string]*llx.CodeBundle{},
	}

	// Process variants and inherit attributes filled from their parents
	for _, query := range p.Queries {
		if len(query.Variants) == 0 {
			continue
		}
		// we do not have a bundle map yet. we need to do the check here
		// so props are copied down before we compile props and queries
		for i := range query.Variants {
			ref := query.Variants[i]

			for _, variant := range p.Queries {
				if variant.Uid == ref.Uid || variant.Mrn == ref.Mrn {
					addBaseToVariant(query, variant)
				}
			}
		}
	}

	// TODO: Make this compatible as a store for shared properties across queries.
	// Also pre-compile as many as possible before returning any errors.
	for i := range p.Props {
		if err := cache.compileProp(p.Props[i]); err != nil {
			return nil, err
		}
	}

	if err := cache.compileQueries(p.Queries, nil); err != nil {
		return nil, err
	}

	// Index policies + update MRNs and checksums, link properties via MRNs
	for i := range p.Policies {
		policy := p.Policies[i]

		// make sure we get a copy of the UID before it is removed (via refresh MRN)
		policyUID := policy.Uid

		// !this is very important to prevent user overrides! vv
		policy.InvalidateAllChecksums()

		err := policy.RefreshMRN(ownerMrn)
		if err != nil {
			return nil, errors.New("failed to refresh policy " + policy.Mrn + ": " + err.Error())
		}

		if policyUID != "" {
			cache.uid2mrn[policyUID] = policy.Mrn
		}

		// Properties
		for i := range policy.Props {
			if err := cache.compileProp(policy.Props[i]); err != nil {
				return nil, err
			}
		}

		// Filters: prep a data structure in case it doesn't exist yet and add
		// any filters that child groups may carry with them
		if policy.ComputedFilters == nil || policy.ComputedFilters.Items == nil {
			policy.ComputedFilters = &explorer.Filters{Items: map[string]*explorer.Mquery{}}
		}
		policy.ComputedFilters.Compile(ownerMrn)

		// ---- GROUPs -------------
		for i := range policy.Groups {
			group := policy.Groups[i]

			// When filters are initially added they haven't been compiled
			group.Filters.Compile(ownerMrn)
			if group.Filters != nil {
				for j := range group.Filters.Items {
					filter := group.Filters.Items[j]
					policy.ComputedFilters.Items[filter.CodeId] = filter
				}
			}

			for j := range group.Policies {
				policyRef := group.Policies[j]
				if err = policyRef.RefreshMRN(ownerMrn); err != nil {
					return nil, err
				}
				policyRef.RefreshChecksum()
			}

			if err := cache.compileQueries(group.Queries, policy); err != nil {
				return nil, err
			}
			if err := cache.compileQueries(group.Checks, policy); err != nil {
				return nil, err
			}
		}
	}

	// cannot be done before all policies and queries have their MRNs set
	bundleMap := p.ToMap()
	bundleMap.Library = library
	bundleMap.Code = cache.codeBundles

	// Validate integrity of references + translate UIDs to MRNs
	for i := range p.Policies {
		policy := p.Policies[i]
		if policy == nil {
			return nil, errors.New("received null policy")
		}

		err := translateGroupUIDs(ownerMrn, policy, cache.uid2mrn)
		if err != nil {
			return nil, errors.New("failed to validate policy: " + err.Error())
		}

		err = bundleMap.ValidatePolicy(ctx, policy)
		if err != nil {
			return nil, errors.New("failed to validate policy: " + err.Error())
		}
	}

	for i := range p.Frameworks {
		framework := p.Frameworks[i]
		if err := framework.compile(ctx, ownerMrn, cache, library); err != nil {
			return nil, errors.New("failed to validate framework: " + err.Error())
		}

		bundleMap.Frameworks[framework.Mrn] = framework
	}

	return bundleMap, cache.error()
}

// this uses a subset of the calls of Mquery.AddBase(), because we don't want
// to push all the fields into the variant, only a select few that are
// needed for context and execution.
func addBaseToVariant(base *explorer.Mquery, variant *explorer.Mquery) {
	if variant == nil {
		return
	}

	if variant.Title == "" {
		variant.Title = base.Title
	}
	if variant.Desc == "" {
		variant.Desc = base.Desc
	}
	if variant.Docs == nil {
		variant.Docs = base.Docs
	} else if base.Docs != nil {
		if variant.Docs.Desc == "" {
			variant.Docs.Desc = base.Docs.Desc
		}
		if variant.Docs.Audit == "" {
			variant.Docs.Audit = base.Docs.Audit
		}
		if variant.Docs.Remediation == nil {
			variant.Docs.Remediation = base.Docs.Remediation
		}
		if variant.Docs.Refs == nil {
			variant.Docs.Refs = base.Docs.Refs
		}
	}
	if variant.Impact == nil {
		variant.Impact = base.Impact
	}
	if len(variant.Props) == 0 {
		variant.Props = base.Props
	}

	// We are not copying filters, variants should have their own.

	// We can't copy Tags because the parent query can have way more tags
	// than are applicable to the variant.
}

type bundleCache struct {
	ownerMrn    string
	lookupQuery map[string]*explorer.Mquery
	lookupProp  map[string]explorer.PropertyRef
	uid2mrn     map[string]string
	codeBundles map[string]*llx.CodeBundle
	bundle      *Bundle
	errors      []error
}

func (c *bundleCache) hasErrors() bool {
	return len(c.errors) != 0
}

func (c *bundleCache) error() error {
	if len(c.errors) == 0 {
		return nil
	}

	var msg strings.Builder
	for i := range c.errors {
		msg.WriteString(c.errors[i].Error())
		msg.WriteString("\n")
	}
	return errors.New(msg.String())
}

func (c *bundleCache) compileQueries(queries []*explorer.Mquery, policy *Policy) error {
	mergedQueries := make([]*explorer.Mquery, len(queries))
	for i := range queries {
		mergedQueries[i] = c.precompileQuery(queries[i], policy)
	}

	// Check for cycles in variants
	if err := c.ensureNoCyclesInVariants(mergedQueries); err != nil {
		return err
	}

	// After the first pass we may have errors. We try to collect as many errors
	// as we can before returning, so more problems can be fixed at once.
	// We have to return at this point, because these errors will prevent us from
	// compiling the queries.
	if c.hasErrors() {
		return c.error()
	}

	for i := range mergedQueries {
		query := mergedQueries[i]
		if query != nil {
			c.compileQuery(query)

			if query != queries[i] {
				queries[i].Checksum = query.Checksum
				queries[i].CodeId = query.CodeId
				queries[i].Type = query.Type
			}
		}
	}

	// The second pass on errors is done after we have compiled as much as possible.
	// Since shared queries may be used in other places, any errors here will prevent
	// us from compiling further.
	return c.error()
}

// precompileQuery indexes the query, turns UIDs into MRNs, compiles properties
// and filters, and pre-processes variants. Also makes sure the query isn't nil.
func (c *bundleCache) precompileQuery(query *explorer.Mquery, policy *Policy) *explorer.Mquery {
	if query == nil {
		c.errors = append(c.errors, errors.New("query or check is null"))
		return nil
	}

	// remove leading and trailing whitespace of docs, refs and tags
	query.Sanitize()

	// ensure the correct mrn is set
	uid := query.Uid
	if err := query.RefreshMRN(c.ownerMrn); err != nil {
		c.errors = append(c.errors, errors.New("failed to refresh MRN for "+query.Uid))
		return nil
	}
	if uid != "" {
		c.uid2mrn[uid] = query.Mrn
	}

	// the policy is only nil if we are dealing with shared queries
	if policy == nil {
		c.lookupQuery[query.Mrn] = query
	} else if existing, ok := c.lookupQuery[query.Mrn]; ok {
		query = query.Merge(existing)
	} else {
		// Any other query that is in a pack, that does not exist globally,
		// we share out to be available in the bundle.
		c.bundle.Queries = append(c.bundle.Queries, query)
		c.lookupQuery[query.Mrn] = query
	}

	// ensure MRNs for properties
	for i := range query.Props {
		if err := c.compileProp(query.Props[i]); err != nil {
			c.errors = append(c.errors, errors.New("failed to compile properties for "+query.Mrn))
			return nil
		}
	}

	// filters have no dependencies, so we can compile them early
	if err := query.Filters.Compile(c.ownerMrn); err != nil {
		c.errors = append(c.errors, errors.New("failed to compile filters for query "+query.Mrn))
		return nil
	}

	// ensure MRNs for variants
	for i := range query.Variants {
		variant := query.Variants[i]
		uid := variant.Uid
		if err := variant.RefreshMRN(c.ownerMrn); err != nil {
			c.errors = append(c.errors, errors.New("failed to refresh MRN for variant in query "+query.Uid))
			return nil
		}
		if uid != "" {
			c.uid2mrn[uid] = variant.Mrn
		}
	}

	// Filters will need to be aggregated into the pack's filters
	// note: must happen after all MRNs (including variants) are computed
	if policy != nil {
		if err := policy.ComputedFilters.AddQueryFilters(query, c.lookupQuery); err != nil {
			c.errors = append(c.errors, fmt.Errorf("failed to register filters for query %s: %v", query.Mrn, err))
			return nil
		}
	}

	return query
}

// Note: you only want to run this, after you are sure that all connected
// dependencies have been processed. Properties must be compiled. Connected
// queries may not be ready yet, but we have to have precompiled them.
func (c *bundleCache) compileQuery(query *explorer.Mquery) {
	_, err := query.RefreshChecksumAndType(c.lookupQuery, c.lookupProp)
	if err != nil {
		c.errors = append(c.errors, errors.Wrap(err, "failed to compile "+query.Mrn))
	}
}

func (c *bundleCache) compileProp(prop *explorer.Property) error {
	var name string

	if prop.Mrn == "" {
		uid := prop.Uid
		if err := prop.RefreshMRN(c.ownerMrn); err != nil {
			return err
		}
		if uid != "" {
			c.uid2mrn[uid] = prop.Mrn
		}

		// TODO: uid's can be namespaced, extract the name
		name = uid
	} else {
		m, err := mrn.NewMRN(prop.Mrn)
		if err != nil {
			return errors.Wrap(err, "failed to compile prop, invalid mrn: "+prop.Mrn)
		}

		name = m.Basename()
	}

	if _, err := prop.RefreshChecksumAndType(); err != nil {
		return err
	}

	c.lookupProp[prop.Mrn] = explorer.PropertyRef{
		Property: prop,
		Name:     name,
	}

	return nil
}

// for a given policy, translate all local UIDs into global IDs/MRNs
func translateGroupUIDs(ownerMrn string, policyObj *Policy, uid2mrn map[string]string) error {
	for i := range policyObj.Groups {
		group := policyObj.Groups[i]

		for i := range group.Queries {
			query := group.Queries[i]
			if mrn, ok := uid2mrn[query.Uid]; ok {
				query.Mrn = mrn
				query.Uid = ""
			}
		}

		for i := range group.Checks {
			check := group.Checks[i]
			if mrn, ok := uid2mrn[check.Uid]; ok {
				check.Mrn = mrn
				check.Uid = ""
			}
		}

		for i := range group.Policies {
			policy := group.Policies[i]
			if mrn, ok := uid2mrn[policy.Uid]; ok {
				policy.Mrn = mrn
				policy.Uid = ""
			}
		}

	}

	return nil
}
