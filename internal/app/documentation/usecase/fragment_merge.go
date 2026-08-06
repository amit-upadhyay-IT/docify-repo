package usecase

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode"

	sharedmodel "docify-repo/internal/model"
)

const (
	fragmentMergeVersion      = "v1"
	assemblyNoticeSlots       = 6
	assembledModelGapLimit    = assembledMaxReviewGapItems - assemblyNoticeSlots
	assemblyNoticeConflict    = 0
	assemblyNoticeSaturation  = 1
	assemblyNoticeSection     = 2
	assemblyNoticeEvidence    = 3
	assemblyNoticeOverview    = 4
	assemblyNoticeDiagram     = 5
	assemblyNoticeCategoryMax = 6
)

var assemblyNoticeTemplates = [assemblyNoticeCategoryMax]struct {
	kind           string
	description    string
	recommendation string
}{
	{kind: "inconsistency", description: "Assembly retained conflicting claims for %d structural identities.", recommendation: "Review the cited sources and resolve the conflicting claims."},
	{kind: "missing_context", description: "Fragment coverage reached a bounded item limit for %d source scopes.", recommendation: "Review the component for facts omitted by bounded fragment generation."},
	{kind: "missing_context", description: "Assembly omitted %d items because assembled section limits were reached.", recommendation: "Review the generation report and source to assess the omitted items."},
	{kind: "missing_context", description: "Assembly omitted %d item citations and %d top-level source paths because evidence limits were reached.", recommendation: "Review the generation report for bounded evidence omission counts."},
	{kind: "uncertainty", description: "Assembly used a deterministic component overview after %d bounded fallback event.", recommendation: "Review the generated title and purpose against the component source."},
	{kind: "missing_context", description: "Assembly omitted diagrams after %d bounded fallback event.", recommendation: "Review architecture, data models, and workflows without relying on diagrams."},
}

// fragmentScope is the stable identity of one required map operation. SplitPath
// carries full split ancestry; request chunk indexes alone can repeat at deeper levels.
type fragmentScope struct {
	ComponentKey     string
	RootComponent    bool
	Kind             sharedmodel.FragmentKind
	SourceBatchIndex int
	SourceBatchCount int
	SourceChunkIndex int
	SourceChunkCount int
	SplitDepth       int
	SplitPath        string
	AllowedEvidence  []string
}

type fragmentScopeKey struct {
	componentIdentity string
	kind              sharedmodel.FragmentKind
	batchIndex        int
	batchCount        int
	chunkIndex        int
	chunkCount        int
	splitPath         string
}

func (s fragmentScope) key() fragmentScopeKey {
	return fragmentScopeKey{
		componentIdentity: componentIdentity(s.ComponentKey, s.RootComponent), kind: s.Kind,
		batchIndex: s.SourceBatchIndex, batchCount: s.SourceBatchCount,
		chunkIndex: s.SourceChunkIndex, chunkCount: s.SourceChunkCount, splitPath: s.SplitPath,
	}
}

func requiredFragmentKinds() []sharedmodel.FragmentKind {
	return []sharedmodel.FragmentKind{
		sharedmodel.FragmentArchitecture,
		sharedmodel.FragmentInterfaces,
		sharedmodel.FragmentDataModels,
		sharedmodel.FragmentWorkflows,
		sharedmodel.FragmentDependencies,
		sharedmodel.FragmentReviewGaps,
	}
}

func requiredFragmentKind(kind sharedmodel.FragmentKind) bool {
	for _, required := range requiredFragmentKinds() {
		if kind == required {
			return true
		}
	}
	return false
}

type fragmentCoverageState string

const (
	fragmentCoveragePending   fragmentCoverageState = "pending"
	fragmentCoverageComplete  fragmentCoverageState = "complete"
	fragmentCoverageSaturated fragmentCoverageState = "saturated"
	fragmentCoverageReplaced  fragmentCoverageState = "replaced"
)

type fragmentCoverageEntry struct {
	scope            fragmentScope
	state            fragmentCoverageState
	validation       fragmentValidation
	expectedEvidence string
	children         []fragmentScopeKey
}

// fragmentCoverageLedger proves that empty-but-valid dossier sections are the result
// of completed required work rather than missing fragment calls.
type fragmentCoverageLedger struct {
	componentKey  string
	rootComponent bool
	roots         []fragmentScopeKey
	entries       map[fragmentScopeKey]*fragmentCoverageEntry
}

func newFragmentCoverageLedger(component sharedmodel.Component, planned []fragmentScope) (*fragmentCoverageLedger, error) {
	if len(planned) == 0 {
		return nil, fragmentAssemblyError{code: "coverage_plan_empty"}
	}
	ledger := &fragmentCoverageLedger{
		componentKey: component.Key, rootComponent: component.RootComponent,
		entries: make(map[fragmentScopeKey]*fragmentCoverageEntry, len(planned)),
	}
	type sourceScope struct {
		identity               string
		batchIndex, batchCount int
		chunkIndex, chunkCount int
		splitPath              string
	}
	plannedKinds := make(map[sourceScope]map[sharedmodel.FragmentKind]struct{})
	plannedEvidence := make(map[sourceScope]string)
	for _, scope := range planned {
		if err := validateCoverageScope(component, scope); err != nil {
			return nil, err
		}
		if !requiredFragmentKind(scope.Kind) {
			return nil, fragmentAssemblyError{code: "coverage_kind_not_required"}
		}
		key := scope.key()
		if _, duplicate := ledger.entries[key]; duplicate {
			return nil, fragmentAssemblyError{code: "coverage_scope_duplicate"}
		}
		expectedEvidence := encodeSealedStrings(scope.AllowedEvidence)
		ledger.entries[key] = &fragmentCoverageEntry{scope: scope, state: fragmentCoveragePending, expectedEvidence: expectedEvidence}
		ledger.roots = append(ledger.roots, key)
		group := sourceScope{
			identity: key.componentIdentity, batchIndex: key.batchIndex, batchCount: key.batchCount,
			chunkIndex: key.chunkIndex, chunkCount: key.chunkCount, splitPath: key.splitPath,
		}
		if plannedKinds[group] == nil {
			plannedKinds[group] = make(map[sharedmodel.FragmentKind]struct{})
			plannedEvidence[group] = expectedEvidence
		} else if plannedEvidence[group] != expectedEvidence {
			return nil, fragmentAssemblyError{code: "coverage_evidence_scope_mismatch"}
		}
		plannedKinds[group][scope.Kind] = struct{}{}
	}
	batchCount := 0
	batchChunks := make(map[int]map[int]sourceScope)
	for source, kinds := range plannedKinds {
		for _, required := range requiredFragmentKinds() {
			if _, ok := kinds[required]; !ok {
				return nil, fragmentAssemblyError{code: "coverage_required_kind_missing"}
			}
		}
		if batchCount == 0 {
			batchCount = source.batchCount
		} else if batchCount != source.batchCount {
			return nil, fragmentAssemblyError{code: "coverage_batch_count_mismatch"}
		}
		if batchChunks[source.batchIndex] == nil {
			batchChunks[source.batchIndex] = make(map[int]sourceScope)
		}
		if _, duplicate := batchChunks[source.batchIndex][source.chunkIndex]; duplicate {
			return nil, fragmentAssemblyError{code: "coverage_chunk_duplicate"}
		}
		batchChunks[source.batchIndex][source.chunkIndex] = source
	}
	if len(batchChunks) != batchCount {
		return nil, fragmentAssemblyError{code: "coverage_batch_missing"}
	}
	for batchIndex := 1; batchIndex <= batchCount; batchIndex++ {
		chunks := batchChunks[batchIndex]
		if len(chunks) == 0 {
			return nil, fragmentAssemblyError{code: "coverage_batch_missing"}
		}
		chunkCount := 0
		for _, source := range chunks {
			if chunkCount == 0 {
				chunkCount = source.chunkCount
			} else if chunkCount != source.chunkCount {
				return nil, fragmentAssemblyError{code: "coverage_chunk_count_mismatch"}
			}
		}
		if len(chunks) != chunkCount {
			return nil, fragmentAssemblyError{code: "coverage_chunk_missing"}
		}
		for chunkIndex := 1; chunkIndex <= chunkCount; chunkIndex++ {
			if _, ok := chunks[chunkIndex]; !ok {
				return nil, fragmentAssemblyError{code: "coverage_chunk_missing"}
			}
		}
	}
	sortScopeKeys(ledger.roots)
	return ledger, nil
}

func validateCoverageScope(component sharedmodel.Component, scope fragmentScope) error {
	if scope.ComponentKey != component.Key || scope.RootComponent != component.RootComponent {
		return fragmentAssemblyError{code: "coverage_component_mismatch"}
	}
	if scope.SourceBatchCount < 1 || scope.SourceBatchIndex < 1 || scope.SourceBatchIndex > scope.SourceBatchCount {
		return fragmentAssemblyError{code: "coverage_batch_invalid"}
	}
	if scope.SourceChunkCount < 1 || scope.SourceChunkIndex < 1 || scope.SourceChunkIndex > scope.SourceChunkCount {
		return fragmentAssemblyError{code: "coverage_chunk_invalid"}
	}
	if scope.SplitPath == "" {
		return fragmentAssemblyError{code: "coverage_split_path_empty"}
	}
	return nil
}

func (l *fragmentCoverageLedger) replace(parent fragmentScope, children []fragmentScope) error {
	if l == nil {
		return fragmentAssemblyError{code: "coverage_ledger_missing"}
	}
	entry, ok := l.entries[parent.key()]
	if !ok {
		return fragmentAssemblyError{code: "coverage_scope_unknown"}
	}
	if entry.state != fragmentCoveragePending || len(children) < 2 {
		return fragmentAssemblyError{code: "coverage_replacement_invalid"}
	}
	component := sharedmodel.Component{Key: l.componentKey, RootComponent: l.rootComponent}
	normalized := make([]fragmentScope, len(children))
	keys := make([]fragmentScopeKey, len(children))
	seen := make(map[fragmentScopeKey]struct{}, len(children))
	parentEvidence, err := decodeSealedStrings(entry.expectedEvidence)
	if err != nil {
		return fragmentAssemblyError{code: "coverage_parent_evidence_invalid"}
	}
	parentEvidenceSet := toSet(parentEvidence)
	childEvidence := make([]string, 0, len(parentEvidence))
	childCount := len(children)
	for index, child := range children {
		if err := validateCoverageScope(component, child); err != nil {
			return err
		}
		if child.Kind != parent.Kind || child.SourceBatchIndex != parent.SourceBatchIndex || child.SourceBatchCount != parent.SourceBatchCount {
			return fragmentAssemblyError{code: "coverage_child_scope_mismatch"}
		}
		if child.SourceChunkCount != childCount || child.SplitPath != parent.SplitPath+"/"+strconv.Itoa(child.SourceChunkIndex-1) {
			return fragmentAssemblyError{code: "coverage_child_lineage_invalid"}
		}
		for _, path := range sortedStrings(child.AllowedEvidence) {
			if _, ok := parentEvidenceSet[path]; !ok {
				return fragmentAssemblyError{code: "coverage_child_evidence_expansion"}
			}
			childEvidence = append(childEvidence, path)
		}
		key := child.key()
		if _, duplicate := seen[key]; duplicate {
			return fragmentAssemblyError{code: "coverage_child_duplicate"}
		}
		if _, exists := l.entries[key]; exists {
			return fragmentAssemblyError{code: "coverage_child_exists"}
		}
		seen[key] = struct{}{}
		normalized[index], keys[index] = child, key
	}
	if encodeSealedStrings(sortedUnique(childEvidence)) != entry.expectedEvidence {
		return fragmentAssemblyError{code: "coverage_child_evidence_missing"}
	}
	sortScopeKeys(keys)
	for _, child := range normalized {
		l.entries[child.key()] = &fragmentCoverageEntry{
			scope: child, state: fragmentCoveragePending, expectedEvidence: encodeSealedStrings(child.AllowedEvidence),
		}
	}
	entry.state = fragmentCoverageReplaced
	entry.children = keys
	return nil
}

func (l *fragmentCoverageLedger) record(scope fragmentScope, validation fragmentValidation, retainSaturation bool) error {
	if l == nil {
		return fragmentAssemblyError{code: "coverage_ledger_missing"}
	}
	entry, ok := l.entries[scope.key()]
	if !ok {
		return fragmentAssemblyError{code: "coverage_scope_unknown"}
	}
	if entry.state != fragmentCoveragePending {
		return fragmentAssemblyError{code: "coverage_result_duplicate"}
	}
	if !validation.valid() {
		return fragmentAssemblyError{code: "coverage_fragment_invalid"}
	}
	if !validation.scopeBound || validation.boundScope != scope.key() {
		return fragmentAssemblyError{code: "coverage_fragment_scope_mismatch"}
	}
	if encodeSealedStrings(scope.AllowedEvidence) != entry.expectedEvidence || validation.sealedEvidence != entry.expectedEvidence {
		return fragmentAssemblyError{code: "coverage_fragment_evidence_mismatch"}
	}
	trusted, err := validation.revalidateSealed()
	if err != nil {
		return err
	}
	validation = trusted
	if validation.kind != scope.Kind || !requiredFragmentKind(validation.kind) {
		return fragmentAssemblyError{code: "coverage_fragment_kind_mismatch"}
	}
	if validation.saturated {
		if !retainSaturation {
			return fragmentAssemblyError{code: "coverage_saturation_unresolved"}
		}
		entry.state = fragmentCoverageSaturated
	} else {
		if !validation.complete {
			return fragmentAssemblyError{code: "coverage_fragment_incomplete"}
		}
		entry.state = fragmentCoverageComplete
	}
	entry.validation = validation
	return nil
}

type coveredFragment struct {
	scope      fragmentScope
	validation fragmentValidation
}

type fragmentCoverageSummary struct {
	fragments          []coveredFragment
	saturatedScopes    int
	saturationEvidence []string
}

func (l *fragmentCoverageLedger) verify(component sharedmodel.Component) (fragmentCoverageSummary, error) {
	if l == nil {
		return fragmentCoverageSummary{}, fragmentAssemblyError{code: "coverage_ledger_missing"}
	}
	if l.componentKey != component.Key || l.rootComponent != component.RootComponent {
		return fragmentCoverageSummary{}, fragmentAssemblyError{code: "coverage_component_mismatch"}
	}
	summary := fragmentCoverageSummary{}
	var visit func(fragmentScopeKey) error
	visit = func(key fragmentScopeKey) error {
		entry := l.entries[key]
		if entry == nil {
			return fragmentAssemblyError{code: "coverage_scope_unknown"}
		}
		switch entry.state {
		case fragmentCoverageComplete:
			trusted, err := entry.validation.revalidateSealed()
			if err != nil || !trusted.scopeBound || trusted.boundScope != entry.scope.key() || trusted.sealedEvidence != entry.expectedEvidence {
				return fragmentAssemblyError{code: "coverage_fragment_seal_invalid"}
			}
			summary.fragments = append(summary.fragments, coveredFragment{scope: entry.scope, validation: trusted})
		case fragmentCoverageSaturated:
			trusted, err := entry.validation.revalidateSealed()
			if err != nil || !trusted.scopeBound || trusted.boundScope != entry.scope.key() || trusted.sealedEvidence != entry.expectedEvidence {
				return fragmentAssemblyError{code: "coverage_fragment_seal_invalid"}
			}
			summary.fragments = append(summary.fragments, coveredFragment{scope: entry.scope, validation: trusted})
			summary.saturatedScopes++
			summary.saturationEvidence = append(summary.saturationEvidence, trusted.evidenceUsed...)
		case fragmentCoverageReplaced:
			if len(entry.children) == 0 {
				return fragmentAssemblyError{code: "coverage_replacement_empty"}
			}
			for _, child := range entry.children {
				if err := visit(child); err != nil {
					return err
				}
			}
		default:
			return fragmentAssemblyError{code: "coverage_scope_uncovered"}
		}
		return nil
	}
	for _, root := range l.roots {
		if err := visit(root); err != nil {
			return fragmentCoverageSummary{}, err
		}
	}
	sort.Slice(summary.fragments, func(left, right int) bool {
		return scopeKeyLess(summary.fragments[left].scope.key(), summary.fragments[right].scope.key())
	})
	summary.saturationEvidence = sortedUnique(summary.saturationEvidence)
	return summary, nil
}

func sortScopeKeys(keys []fragmentScopeKey) {
	sort.Slice(keys, func(left, right int) bool { return scopeKeyLess(keys[left], keys[right]) })
}

func scopeKeyLess(left, right fragmentScopeKey) bool {
	if left.componentIdentity != right.componentIdentity {
		return left.componentIdentity < right.componentIdentity
	}
	if left.batchIndex != right.batchIndex {
		return left.batchIndex < right.batchIndex
	}
	if left.batchCount != right.batchCount {
		return left.batchCount < right.batchCount
	}
	if left.chunkIndex != right.chunkIndex {
		return left.chunkIndex < right.chunkIndex
	}
	if left.chunkCount != right.chunkCount {
		return left.chunkCount < right.chunkCount
	}
	if left.splitPath != right.splitPath {
		return left.splitPath < right.splitPath
	}
	return left.kind < right.kind
}

// fragmentAssemblyStats contains safe numeric merge outcomes. It carries no model
// prose, source content, prompts, schemas, or provider data.
type fragmentAssemblyStats struct {
	ConflictIdentities         int
	SaturatedScopes            int
	ItemsWithEvidenceOverflow  int
	ItemEvidencePathsOmitted   int
	NoticeEvidencePathsOmitted int
	TopLevelSourcePathsOmitted int
	ModelReviewGapsOmitted     int
	OverviewFallbacks          int
	DiagramFallbacks           int
	SectionItemsOmitted        map[sharedmodel.FragmentKind]int
}

type fragmentAssemblyInput struct {
	Component          sharedmodel.Component
	Catalog            []string
	Coverage           *fragmentCoverageLedger
	OverviewCandidates []fragmentValidation
	Overview           *overviewValidation
	OverviewFallback   bool
	DiagramFragments   []fragmentValidation
	DiagramFallback    bool
}

type fragmentAssemblyError struct {
	code       string
	issueCodes []string
}

func (e fragmentAssemblyError) Error() string {
	if len(e.issueCodes) == 0 {
		return "fragment assembly failed: " + e.code
	}
	return "fragment assembly failed: " + e.code + ": " + strings.Join(e.issueCodes, ",")
}

type itemMergeSpec[T any] struct {
	normalize func(T) T
	paths     func(T) []string
	withPaths func(T, []string) T
	name      func(T) string
	kind      func(T) string
	identity  func(T) string
}

type mergedFragmentItem[T any] struct {
	item      T
	fullPaths []string
	support   int
	firstPath string
	name      string
	kind      string
	canonical []byte
	digest    [sha256.Size]byte
}

func mergeFragmentItems[T any](items []T, limit int, section sharedmodel.FragmentKind, spec itemMergeSpec[T], stats *fragmentAssemblyStats) ([]T, int, []string, error) {
	groups := make(map[string]*mergedFragmentItem[T], len(items))
	for _, raw := range items {
		item := spec.normalize(raw)
		paths := sortedUnique(spec.paths(item))
		content := spec.withPaths(item, []string{})
		canonical, err := json.Marshal(content)
		if err != nil {
			return nil, 0, nil, fragmentAssemblyError{code: "item_key_encoding"}
		}
		key := string(canonical)
		if existing := groups[key]; existing != nil {
			existing.fullPaths = sortedUnique(append(existing.fullPaths, paths...))
			continue
		}
		groups[key] = &mergedFragmentItem[T]{item: item, fullPaths: paths}
	}

	merged := make([]mergedFragmentItem[T], 0, len(groups))
	for _, item := range groups {
		item.support = len(item.fullPaths)
		bounded, omitted := boundedPaths(item.fullPaths, assembledMaxItemSourcePaths)
		if omitted > 0 {
			stats.ItemsWithEvidenceOverflow++
			stats.ItemEvidencePathsOmitted += omitted
		}
		item.item = spec.withPaths(item.item, bounded)
		if err := prepareMergedSort(item, spec); err != nil {
			return nil, 0, nil, err
		}
		merged = append(merged, *item)
	}

	identities := make(map[string][]int)
	for index := range merged {
		if identity := spec.identity(merged[index].item); identity != "" {
			identities[identity] = append(identities[identity], index)
		}
	}
	conflicts := 0
	conflictPaths := make([]string, 0)
	for _, indexes := range identities {
		if len(indexes) < 2 {
			continue
		}
		conflicts++
		for _, index := range indexes {
			conflictPaths = append(conflictPaths, merged[index].fullPaths...)
		}
	}
	conflictPaths = sortedUnique(conflictPaths)

	if len(merged) > limit {
		sort.Slice(merged, func(left, right int) bool {
			if merged[left].support != merged[right].support {
				return merged[left].support > merged[right].support
			}
			return mergedItemLess(merged[left], merged[right])
		})
		omitted := len(merged) - limit
		stats.SectionItemsOmitted[section] += omitted
		if section == sharedmodel.FragmentReviewGaps {
			stats.ModelReviewGapsOmitted += omitted
		}
		merged = merged[:limit]
	}
	sort.Slice(merged, func(left, right int) bool { return mergedItemLess(merged[left], merged[right]) })
	result := make([]T, len(merged))
	for index := range merged {
		result[index] = merged[index].item
	}
	return result, conflicts, conflictPaths, nil
}

func prepareMergedSort[T any](item *mergedFragmentItem[T], spec itemMergeSpec[T]) error {
	paths := spec.paths(item.item)
	if len(paths) > 0 {
		item.firstPath = paths[0]
	}
	item.name, item.kind = spec.name(item.item), spec.kind(item.item)
	canonical, err := json.Marshal(item.item)
	if err != nil {
		return fragmentAssemblyError{code: "item_order_encoding"}
	}
	item.canonical = canonical
	item.digest = sha256.Sum256(canonical)
	return nil
}

func mergedItemLess[T any](left, right mergedFragmentItem[T]) bool {
	if left.firstPath != right.firstPath {
		return left.firstPath < right.firstPath
	}
	if left.name != right.name {
		return left.name < right.name
	}
	if left.kind != right.kind {
		return left.kind < right.kind
	}
	if compared := bytes.Compare(left.digest[:], right.digest[:]); compared != 0 {
		return compared < 0
	}
	return bytes.Compare(left.canonical, right.canonical) < 0
}

func assembleComponentDossier(input fragmentAssemblyInput) (sharedmodel.ComponentDossier, fragmentAssemblyStats, error) {
	stats := fragmentAssemblyStats{SectionItemsOmitted: make(map[sharedmodel.FragmentKind]int)}
	coverage, err := input.Coverage.verify(input.Component)
	if err != nil {
		return sharedmodel.ComponentDossier{}, stats, err
	}
	stats.SaturatedScopes = coverage.saturatedScopes
	saturationEvidence := append([]string(nil), coverage.saturationEvidence...)

	architecture := make([]sharedmodel.ArchitectureItem, 0)
	interfaces := make([]sharedmodel.InterfaceItem, 0)
	dataModels := make([]sharedmodel.DataModelItem, 0)
	workflows := make([]sharedmodel.WorkflowItem, 0)
	dependencies := make([]sharedmodel.DependencyItem, 0)
	modelGaps := make([]sharedmodel.ReviewGap, 0)
	evidenceGroups := make([][]string, 0, len(coverage.fragments)+len(input.OverviewCandidates)+len(input.DiagramFragments))
	for _, covered := range coverage.fragments {
		evidenceGroups = append(evidenceGroups, covered.validation.evidenceUsed)
		switch covered.scope.Kind {
		case sharedmodel.FragmentArchitecture:
			fragment, ok := covered.validation.value.(sharedmodel.ArchitectureFragment)
			if !ok {
				return sharedmodel.ComponentDossier{}, stats, fragmentAssemblyError{code: "fragment_value_type_mismatch"}
			}
			architecture = append(architecture, fragment.Items...)
		case sharedmodel.FragmentInterfaces:
			fragment, ok := covered.validation.value.(sharedmodel.InterfacesFragment)
			if !ok {
				return sharedmodel.ComponentDossier{}, stats, fragmentAssemblyError{code: "fragment_value_type_mismatch"}
			}
			interfaces = append(interfaces, fragment.Items...)
		case sharedmodel.FragmentDataModels:
			fragment, ok := covered.validation.value.(sharedmodel.DataModelsFragment)
			if !ok {
				return sharedmodel.ComponentDossier{}, stats, fragmentAssemblyError{code: "fragment_value_type_mismatch"}
			}
			dataModels = append(dataModels, fragment.Items...)
		case sharedmodel.FragmentWorkflows:
			fragment, ok := covered.validation.value.(sharedmodel.WorkflowsFragment)
			if !ok {
				return sharedmodel.ComponentDossier{}, stats, fragmentAssemblyError{code: "fragment_value_type_mismatch"}
			}
			workflows = append(workflows, fragment.Items...)
		case sharedmodel.FragmentDependencies:
			fragment, ok := covered.validation.value.(sharedmodel.DependenciesFragment)
			if !ok {
				return sharedmodel.ComponentDossier{}, stats, fragmentAssemblyError{code: "fragment_value_type_mismatch"}
			}
			dependencies = append(dependencies, fragment.Items...)
		case sharedmodel.FragmentReviewGaps:
			fragment, ok := covered.validation.value.(sharedmodel.ReviewGapsFragment)
			if !ok {
				return sharedmodel.ComponentDossier{}, stats, fragmentAssemblyError{code: "fragment_value_type_mismatch"}
			}
			modelGaps = append(modelGaps, fragment.Items...)
		default:
			return sharedmodel.ComponentDossier{}, stats, fragmentAssemblyError{code: "coverage_fragment_kind_mismatch"}
		}
	}

	title, purpose, overviewEvidence, overviewFallback, err := deterministicOverview(input.Component, input.OverviewCandidates)
	if err != nil {
		return sharedmodel.ComponentDossier{}, stats, err
	}
	if input.Overview != nil {
		trusted, err := input.Overview.revalidateSealed(input.Component)
		if err != nil {
			return sharedmodel.ComponentDossier{}, stats, err
		}
		title, purpose, overviewFallback = trusted.value.Title, trusted.value.Purpose, false
	}
	if overviewFallback || input.OverviewFallback {
		stats.OverviewFallbacks = 1
	}
	evidenceGroups = append(evidenceGroups, overviewEvidence)

	diagrams := make([]sharedmodel.Diagram, 0)
	for _, validation := range input.DiagramFragments {
		if !validation.valid() || validation.kind != sharedmodel.FragmentDiagrams {
			return sharedmodel.ComponentDossier{}, stats, fragmentAssemblyError{code: "diagram_fragment_invalid"}
		}
		trusted, err := validation.revalidateSealed()
		if err != nil {
			return sharedmodel.ComponentDossier{}, stats, err
		}
		validation = trusted
		if !validation.scopeBound || validation.boundScope.componentIdentity != componentIdentity(input.Component.Key, input.Component.RootComponent) {
			return sharedmodel.ComponentDossier{}, stats, fragmentAssemblyError{code: "diagram_fragment_scope_mismatch"}
		}
		if !validation.complete {
			stats.DiagramFallbacks = 1
			continue
		}
		if validation.saturated {
			stats.SaturatedScopes++
			saturationEvidence = append(saturationEvidence, validation.evidenceUsed...)
		}
		fragment, ok := validation.value.(sharedmodel.DiagramsFragment)
		if !ok {
			return sharedmodel.ComponentDossier{}, stats, fragmentAssemblyError{code: "fragment_value_type_mismatch"}
		}
		diagrams = append(diagrams, fragment.Items...)
		evidenceGroups = append(evidenceGroups, validation.evidenceUsed)
	}
	if input.DiagramFallback {
		stats.DiagramFallbacks = 1
	}

	conflictPaths := make([]string, 0)
	architecture, conflicts, paths, err := mergeFragmentItems(architecture, assembledMaxArchitectureItems, sharedmodel.FragmentArchitecture, architectureMergeSpec(), &stats)
	if err != nil {
		return sharedmodel.ComponentDossier{}, stats, err
	}
	stats.ConflictIdentities += conflicts
	conflictPaths = append(conflictPaths, paths...)
	interfaces, conflicts, paths, err = mergeFragmentItems(interfaces, assembledMaxInterfaceItems, sharedmodel.FragmentInterfaces, interfaceMergeSpec(), &stats)
	if err != nil {
		return sharedmodel.ComponentDossier{}, stats, err
	}
	stats.ConflictIdentities += conflicts
	conflictPaths = append(conflictPaths, paths...)
	dataModels, conflicts, paths, err = mergeFragmentItems(dataModels, assembledMaxDataModelItems, sharedmodel.FragmentDataModels, dataModelMergeSpec(), &stats)
	if err != nil {
		return sharedmodel.ComponentDossier{}, stats, err
	}
	stats.ConflictIdentities += conflicts
	conflictPaths = append(conflictPaths, paths...)
	workflows, conflicts, paths, err = mergeFragmentItems(workflows, assembledMaxWorkflowItems, sharedmodel.FragmentWorkflows, workflowMergeSpec(), &stats)
	if err != nil {
		return sharedmodel.ComponentDossier{}, stats, err
	}
	stats.ConflictIdentities += conflicts
	conflictPaths = append(conflictPaths, paths...)
	dependencies, conflicts, paths, err = mergeFragmentItems(dependencies, assembledMaxDependencyItems, sharedmodel.FragmentDependencies, dependencyMergeSpec(), &stats)
	if err != nil {
		return sharedmodel.ComponentDossier{}, stats, err
	}
	stats.ConflictIdentities += conflicts
	conflictPaths = append(conflictPaths, paths...)
	modelGaps, _, _, err = mergeFragmentItems(modelGaps, assembledModelGapLimit, sharedmodel.FragmentReviewGaps, reviewGapMergeSpec(), &stats)
	if err != nil {
		return sharedmodel.ComponentDossier{}, stats, err
	}
	diagrams, conflicts, paths, err = mergeFragmentItems(diagrams, assembledMaxDiagrams, sharedmodel.FragmentDiagrams, diagramMergeSpec(), &stats)
	if err != nil {
		return sharedmodel.ComponentDossier{}, stats, err
	}
	stats.ConflictIdentities += conflicts
	conflictPaths = append(conflictPaths, paths...)

	conflictNoticePaths := boundNoticeEvidence(sortedUnique(conflictPaths), &stats)
	saturationNoticePaths := boundNoticeEvidence(saturationEvidence, &stats)
	notices := make([]sharedmodel.ReviewGap, 0, assemblyNoticeSlots)
	if stats.ConflictIdentities > 0 {
		notices = append(notices, assemblyNotice(assemblyNoticeConflict, conflictNoticePaths, stats.ConflictIdentities))
	}
	if stats.SaturatedScopes > 0 {
		notices = append(notices, assemblyNotice(assemblyNoticeSaturation, saturationNoticePaths, stats.SaturatedScopes))
	}
	if omitted := totalSectionOmissions(stats.SectionItemsOmitted); omitted > 0 {
		notices = append(notices, assemblyNotice(assemblyNoticeSection, nil, omitted))
	}
	if stats.OverviewFallbacks > 0 {
		notices = append(notices, assemblyNotice(assemblyNoticeOverview, nil, stats.OverviewFallbacks))
	}
	if stats.DiagramFallbacks > 0 {
		notices = append(notices, assemblyNotice(assemblyNoticeDiagram, nil, stats.DiagramFallbacks))
	}

	fullEvidence := unionSorted(evidenceGroups)
	citationCounts := make(map[string]int)
	countArchitectureEvidence(citationCounts, architecture)
	countInterfaceEvidence(citationCounts, interfaces)
	countDataModelEvidence(citationCounts, dataModels)
	countWorkflowEvidence(citationCounts, workflows)
	countDependencyEvidence(citationCounts, dependencies)
	countReviewGapEvidence(citationCounts, modelGaps)
	countDiagramEvidence(citationCounts, diagrams)
	countReviewGapEvidence(citationCounts, notices)
	topLevelPaths := selectTopLevelPaths(fullEvidence, citationCounts, &stats)

	if stats.ItemEvidencePathsOmitted+stats.NoticeEvidencePathsOmitted > 0 || stats.TopLevelSourcePathsOmitted > 0 {
		notices = append(notices, assemblyNotice(assemblyNoticeEvidence, nil, stats.ItemEvidencePathsOmitted+stats.NoticeEvidencePathsOmitted, stats.TopLevelSourcePathsOmitted))
	}
	reviewGaps := append(modelGaps, notices...)
	reviewGaps, err = sortReviewGaps(reviewGaps)
	if err != nil {
		return sharedmodel.ComponentDossier{}, stats, err
	}

	dossier := sharedmodel.ComponentDossier{
		Title: title, Purpose: purpose, SourcePaths: nonNil(topLevelPaths),
		Architecture: nonNil(architecture), Interfaces: nonNil(interfaces), DataModels: nonNil(dataModels),
		Workflows: nonNil(workflows), Dependencies: nonNil(dependencies), ReviewGaps: nonNil(reviewGaps), Diagrams: nonNil(diagrams),
	}
	validated, err := validateAssembledDossier(dossier, fullEvidence, input.Catalog)
	if err != nil {
		return sharedmodel.ComponentDossier{}, stats, err
	}
	return validated, stats, nil
}

func validateAssembledDossier(dossier sharedmodel.ComponentDossier, allowedEvidence, catalog []string) (sharedmodel.ComponentDossier, error) {
	body, err := json.Marshal(dossier)
	if err != nil {
		return sharedmodel.ComponentDossier{}, fragmentAssemblyError{code: "dossier_encoding"}
	}
	validation := validateDossier(body, allowedEvidence, catalog)
	if !validation.valid() {
		return sharedmodel.ComponentDossier{}, fragmentAssemblyError{code: "final_validation", issueCodes: issueCodes(validation.issues)}
	}
	return validation.dossier, nil
}

func deterministicOverview(component sharedmodel.Component, validations []fragmentValidation) (string, string, []string, bool, error) {
	type candidate struct {
		value     sharedmodel.OverviewCandidate
		firstPath string
		canonical []byte
		digest    [sha256.Size]byte
	}
	candidates := make([]candidate, 0, len(validations))
	evidence := make([]string, 0)
	for _, validation := range validations {
		if !validation.valid() || validation.kind != sharedmodel.FragmentOverviewCandidate {
			return "", "", nil, false, fragmentAssemblyError{code: "overview_candidate_invalid"}
		}
		trusted, err := validation.revalidateSealed()
		if err != nil {
			return "", "", nil, false, err
		}
		validation = trusted
		if !validation.scopeBound || validation.boundScope.componentIdentity != componentIdentity(component.Key, component.RootComponent) {
			return "", "", nil, false, fragmentAssemblyError{code: "overview_candidate_scope_mismatch"}
		}
		value, ok := validation.value.(sharedmodel.OverviewCandidate)
		if !ok {
			return "", "", nil, false, fragmentAssemblyError{code: "fragment_value_type_mismatch"}
		}
		value.SourcePaths = sortedUnique(value.SourcePaths)
		body, err := json.Marshal(value)
		if err != nil {
			return "", "", nil, false, fragmentAssemblyError{code: "overview_order_encoding"}
		}
		item := candidate{value: value, canonical: body, digest: sha256.Sum256(body)}
		if len(value.SourcePaths) > 0 {
			item.firstPath = value.SourcePaths[0]
		}
		candidates = append(candidates, item)
		evidence = append(evidence, validation.evidenceUsed...)
	}
	sort.Slice(candidates, func(left, right int) bool {
		if candidates[left].firstPath != candidates[right].firstPath {
			return candidates[left].firstPath < candidates[right].firstPath
		}
		if candidates[left].value.Title != candidates[right].value.Title {
			return candidates[left].value.Title < candidates[right].value.Title
		}
		if candidates[left].value.Purpose != candidates[right].value.Purpose {
			return candidates[left].value.Purpose < candidates[right].value.Purpose
		}
		if compared := bytes.Compare(candidates[left].digest[:], candidates[right].digest[:]); compared != 0 {
			return compared < 0
		}
		return bytes.Compare(candidates[left].canonical, candidates[right].canonical) < 0
	})
	if len(candidates) > 0 {
		return candidates[0].value.Title, candidates[0].value.Purpose, sortedUnique(evidence), false, nil
	}
	key := safeComponentKeyProse(component.Key)
	return fallbackComponentTitle(component), "Documentation for the " + key + " component.", nil, true, nil
}

func fallbackComponentTitle(component sharedmodel.Component) string {
	if component.RootComponent {
		return "Repository Root"
	}
	name := component.Key
	if slash := strings.LastIndexByte(name, '/'); slash >= 0 {
		name = name[slash+1:]
	}
	var result strings.Builder
	space := true
	for _, character := range name {
		if character > unicode.MaxASCII || (!unicode.IsLetter(character) && !unicode.IsDigit(character)) {
			if result.Len() > 0 {
				space = true
			}
			continue
		}
		requiredBytes := 1
		if space && result.Len() > 0 {
			requiredBytes++
		}
		if result.Len()+requiredBytes > schemaMaxTitle {
			break
		}
		if space && result.Len() > 0 {
			result.WriteByte(' ')
		}
		if space {
			character = unicode.ToUpper(character)
		}
		result.WriteRune(character)
		space = false
	}
	if result.Len() == 0 {
		return "Component"
	}
	return result.String()
}

func safeComponentKeyProse(value string) string {
	const hexadecimal = "0123456789ABCDEF"
	var result strings.Builder
	for index := 0; index < len(value); index++ {
		character := value[index]
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || strings.ContainsRune("/._@-", rune(character)) {
			result.WriteByte(character)
			continue
		}
		result.WriteByte('%')
		result.WriteByte(hexadecimal[character>>4])
		result.WriteByte(hexadecimal[character&0x0f])
	}
	if result.Len() == 0 {
		return "component"
	}
	return result.String()
}

func assemblyNotice(category int, paths []string, counts ...any) sharedmodel.ReviewGap {
	template := assemblyNoticeTemplates[category]
	return sharedmodel.ReviewGap{
		Kind: template.kind, Description: fmt.Sprintf(template.description, counts...),
		Recommendation: template.recommendation, SourcePaths: nonNil(paths),
	}
}

func boundNoticeEvidence(paths []string, stats *fragmentAssemblyStats) []string {
	bounded, omitted := boundedPaths(sortedUnique(paths), assembledMaxItemSourcePaths)
	stats.NoticeEvidencePathsOmitted += omitted
	return bounded
}

func totalSectionOmissions(omitted map[sharedmodel.FragmentKind]int) int {
	total := 0
	for _, count := range omitted {
		total += count
	}
	return total
}

func selectTopLevelPaths(evidence []string, citationCounts map[string]int, stats *fragmentAssemblyStats) []string {
	paths := sortedUnique(evidence)
	sort.Slice(paths, func(left, right int) bool {
		if citationCounts[paths[left]] != citationCounts[paths[right]] {
			return citationCounts[paths[left]] > citationCounts[paths[right]]
		}
		return paths[left] < paths[right]
	})
	if len(paths) > assembledMaxSourcePaths {
		stats.TopLevelSourcePathsOmitted = len(paths) - assembledMaxSourcePaths
		paths = paths[:assembledMaxSourcePaths]
	}
	return nonNil(paths)
}

func sortReviewGaps(items []sharedmodel.ReviewGap) ([]sharedmodel.ReviewGap, error) {
	merged := make([]mergedFragmentItem[sharedmodel.ReviewGap], len(items))
	spec := reviewGapMergeSpec()
	for index, item := range items {
		item = spec.normalize(item)
		merged[index] = mergedFragmentItem[sharedmodel.ReviewGap]{item: item}
		if err := prepareMergedSort(&merged[index], spec); err != nil {
			return nil, err
		}
	}
	sort.Slice(merged, func(left, right int) bool { return mergedItemLess(merged[left], merged[right]) })
	result := make([]sharedmodel.ReviewGap, len(merged))
	for index := range merged {
		result[index] = merged[index].item
	}
	return result, nil
}

func architectureMergeSpec() itemMergeSpec[sharedmodel.ArchitectureItem] {
	return itemMergeSpec[sharedmodel.ArchitectureItem]{
		normalize: func(item sharedmodel.ArchitectureItem) sharedmodel.ArchitectureItem {
			item.SourcePaths = sortedUnique(item.SourcePaths)
			return item
		},
		paths: func(item sharedmodel.ArchitectureItem) []string { return item.SourcePaths },
		withPaths: func(item sharedmodel.ArchitectureItem, paths []string) sharedmodel.ArchitectureItem {
			item.SourcePaths = nonNil(paths)
			return item
		},
		name: func(item sharedmodel.ArchitectureItem) string { return item.Title }, kind: func(sharedmodel.ArchitectureItem) string { return "" },
		identity: func(item sharedmodel.ArchitectureItem) string { return identityKey(item.Title) },
	}
}

func interfaceMergeSpec() itemMergeSpec[sharedmodel.InterfaceItem] {
	return itemMergeSpec[sharedmodel.InterfaceItem]{
		normalize: func(item sharedmodel.InterfaceItem) sharedmodel.InterfaceItem {
			item.SourcePaths = sortedUnique(item.SourcePaths)
			return item
		},
		paths: func(item sharedmodel.InterfaceItem) []string { return item.SourcePaths },
		withPaths: func(item sharedmodel.InterfaceItem, paths []string) sharedmodel.InterfaceItem {
			item.SourcePaths = nonNil(paths)
			return item
		},
		name: func(item sharedmodel.InterfaceItem) string { return item.Name }, kind: func(item sharedmodel.InterfaceItem) string { return item.Kind },
		identity: func(item sharedmodel.InterfaceItem) string { return identityKey(item.Name, item.Kind, item.Direction) },
	}
}

func dataModelMergeSpec() itemMergeSpec[sharedmodel.DataModelItem] {
	return itemMergeSpec[sharedmodel.DataModelItem]{
		normalize: func(item sharedmodel.DataModelItem) sharedmodel.DataModelItem {
			item.Fields = nonNil(item.Fields)
			item.Relationships = nonNil(item.Relationships)
			item.SourcePaths = sortedUnique(item.SourcePaths)
			return item
		},
		paths: func(item sharedmodel.DataModelItem) []string { return item.SourcePaths },
		withPaths: func(item sharedmodel.DataModelItem, paths []string) sharedmodel.DataModelItem {
			item.SourcePaths = nonNil(paths)
			return item
		},
		name: func(item sharedmodel.DataModelItem) string { return item.Name }, kind: func(item sharedmodel.DataModelItem) string { return item.Kind },
		identity: func(item sharedmodel.DataModelItem) string { return identityKey(item.Name, item.Kind) },
	}
}

func workflowMergeSpec() itemMergeSpec[sharedmodel.WorkflowItem] {
	return itemMergeSpec[sharedmodel.WorkflowItem]{
		normalize: func(item sharedmodel.WorkflowItem) sharedmodel.WorkflowItem {
			item.Steps = nonNil(item.Steps)
			item.SourcePaths = sortedUnique(item.SourcePaths)
			return item
		},
		paths: func(item sharedmodel.WorkflowItem) []string { return item.SourcePaths },
		withPaths: func(item sharedmodel.WorkflowItem, paths []string) sharedmodel.WorkflowItem {
			item.SourcePaths = nonNil(paths)
			return item
		},
		name: func(item sharedmodel.WorkflowItem) string { return item.Name }, kind: func(sharedmodel.WorkflowItem) string { return "" },
		identity: func(item sharedmodel.WorkflowItem) string { return identityKey(item.Name) },
	}
}

func dependencyMergeSpec() itemMergeSpec[sharedmodel.DependencyItem] {
	return itemMergeSpec[sharedmodel.DependencyItem]{
		normalize: func(item sharedmodel.DependencyItem) sharedmodel.DependencyItem {
			item.SourcePaths = sortedUnique(item.SourcePaths)
			return item
		},
		paths: func(item sharedmodel.DependencyItem) []string { return item.SourcePaths },
		withPaths: func(item sharedmodel.DependencyItem, paths []string) sharedmodel.DependencyItem {
			item.SourcePaths = nonNil(paths)
			return item
		},
		name: func(item sharedmodel.DependencyItem) string { return item.Name }, kind: func(item sharedmodel.DependencyItem) string { return item.Kind },
		identity: func(item sharedmodel.DependencyItem) string {
			return identityKey(item.Name, item.Kind, item.ComponentKey)
		},
	}
}

func reviewGapMergeSpec() itemMergeSpec[sharedmodel.ReviewGap] {
	return itemMergeSpec[sharedmodel.ReviewGap]{
		normalize: func(item sharedmodel.ReviewGap) sharedmodel.ReviewGap {
			item.SourcePaths = sortedUnique(item.SourcePaths)
			return item
		},
		paths: func(item sharedmodel.ReviewGap) []string { return item.SourcePaths },
		withPaths: func(item sharedmodel.ReviewGap, paths []string) sharedmodel.ReviewGap {
			item.SourcePaths = nonNil(paths)
			return item
		},
		name: func(item sharedmodel.ReviewGap) string { return item.Description }, kind: func(item sharedmodel.ReviewGap) string { return item.Kind },
		identity: func(sharedmodel.ReviewGap) string { return "" },
	}
}

func diagramMergeSpec() itemMergeSpec[sharedmodel.Diagram] {
	return itemMergeSpec[sharedmodel.Diagram]{
		normalize: normalizeDiagram,
		paths:     func(item sharedmodel.Diagram) []string { return item.SourcePaths },
		withPaths: func(item sharedmodel.Diagram, paths []string) sharedmodel.Diagram {
			item.SourcePaths = nonNil(paths)
			return item
		},
		name: func(item sharedmodel.Diagram) string { return item.Title }, kind: func(item sharedmodel.Diagram) string { return string(item.Type) },
		identity: func(item sharedmodel.Diagram) string { return identityKey(string(item.Type), item.Title) },
	}
}

func normalizeDiagram(item sharedmodel.Diagram) sharedmodel.Diagram {
	item.SourcePaths = sortedUnique(item.SourcePaths)
	switch item.Type {
	case sharedmodel.DiagramFlowchart:
		item.Nodes, item.Edges = nonNil(item.Nodes), nonNil(item.Edges)
	case sharedmodel.DiagramSequence:
		item.Participants, item.Messages = nonNil(item.Participants), nonNil(item.Messages)
	case sharedmodel.DiagramClass:
		item.Classes, item.Relationships = nonNil(item.Classes), nonNil(item.Relationships)
		for index := range item.Classes {
			item.Classes[index].Members = nonNil(item.Classes[index].Members)
		}
	}
	return item
}

func identityKey(values ...string) string {
	encoded, _ := json.Marshal(values)
	return string(encoded)
}

func boundedPaths(paths []string, limit int) ([]string, int) {
	paths = sortedUnique(paths)
	if len(paths) <= limit {
		return nonNil(paths), 0
	}
	return append([]string(nil), paths[:limit]...), len(paths) - limit
}

func sortedUnique(values []string) []string {
	if len(values) == 0 {
		return []string{}
	}
	result := append([]string(nil), values...)
	sort.Strings(result)
	write := 1
	for read := 1; read < len(result); read++ {
		if result[read] == result[write-1] {
			continue
		}
		result[write] = result[read]
		write++
	}
	return result[:write]
}

func nonNil[T any](values []T) []T {
	if values == nil {
		return []T{}
	}
	return values
}

func countArchitectureEvidence(counts map[string]int, items []sharedmodel.ArchitectureItem) {
	for _, item := range items {
		countItemEvidence(counts, item.SourcePaths)
	}
}
func countInterfaceEvidence(counts map[string]int, items []sharedmodel.InterfaceItem) {
	for _, item := range items {
		countItemEvidence(counts, item.SourcePaths)
	}
}
func countDataModelEvidence(counts map[string]int, items []sharedmodel.DataModelItem) {
	for _, item := range items {
		countItemEvidence(counts, item.SourcePaths)
	}
}
func countWorkflowEvidence(counts map[string]int, items []sharedmodel.WorkflowItem) {
	for _, item := range items {
		countItemEvidence(counts, item.SourcePaths)
	}
}
func countDependencyEvidence(counts map[string]int, items []sharedmodel.DependencyItem) {
	for _, item := range items {
		countItemEvidence(counts, item.SourcePaths)
	}
}
func countReviewGapEvidence(counts map[string]int, items []sharedmodel.ReviewGap) {
	for _, item := range items {
		countItemEvidence(counts, item.SourcePaths)
	}
}
func countDiagramEvidence(counts map[string]int, items []sharedmodel.Diagram) {
	for _, item := range items {
		countItemEvidence(counts, item.SourcePaths)
	}
}
func countItemEvidence(counts map[string]int, paths []string) {
	for _, path := range sortedUnique(paths) {
		counts[path]++
	}
}
