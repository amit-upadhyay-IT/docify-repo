package usecase

import (
	"fmt"
	"path"
	"sort"
	"strings"
	"unicode/utf8"

	documentationmodel "docify-repo/internal/app/documentation/model"
	sharedmodel "docify-repo/internal/model"
)

const (
	componentDiscoveryVersion = "v1"
	rootComponentKey          = "@root"
)

var conventionalComponentContainers = map[string]struct{}{
	"apps": {}, "cmd": {}, "internal": {}, "packages": {}, "pkg": {}, "services": {}, "src": {},
}

func discoverComponents(files []sharedmodel.SourceFile, policy documentationmodel.ComponentPolicy, docsDir string) ([]sharedmodel.Component, []sharedmodel.SourceFile, error) {
	if err := validateComponentPolicy(policy); err != nil {
		return nil, nil, err
	}

	manifestRoots := make(map[string]struct{})
	for _, file := range files {
		if file.Role != sharedmodel.RoleDependencyManifest {
			continue
		}
		directory := path.Dir(file.Path)
		if directory != "." {
			manifestRoots[directory] = struct{}{}
		}
	}

	ownedFiles := append([]sharedmodel.SourceFile(nil), files...)
	for index := range ownedFiles {
		ownedFiles[index].ComponentKey, ownedFiles[index].RootComponent = componentKey(ownedFiles[index].Path, policy, manifestRoots)
	}

	// Every non-root dependency-manifest directory becomes its own component root,
	// so the only manifests that are shared context across components are the
	// repo-root manifests (for example a root go.mod or package.json). They are
	// owned by @root but describe every child component's technology and
	// dependencies, so they are attached as relevant context to each non-root
	// component in stable path order.
	rootManifests := make([]sharedmodel.SourceFile, 0)
	for _, file := range ownedFiles {
		if file.Role == sharedmodel.RoleDependencyManifest && path.Dir(file.Path) == "." {
			rootManifests = append(rootManifests, file)
		}
	}
	sort.Slice(rootManifests, func(left, right int) bool {
		return rootManifests[left].Path < rootManifests[right].Path
	})

	type componentFiles struct {
		triggering []sharedmodel.SourceFile
		supporting []sharedmodel.SourceFile
	}
	grouped := make(map[string]*componentFiles)
	for _, file := range ownedFiles {
		if !file.TriggersRegeneration {
			continue
		}
		identity := componentIdentity(file.ComponentKey, file.RootComponent)
		group := grouped[identity]
		if group == nil {
			group = &componentFiles{}
			grouped[identity] = group
		}
		group.triggering = append(group.triggering, file)
	}
	for _, file := range ownedFiles {
		if file.TriggersRegeneration {
			continue
		}
		if group := grouped[componentIdentity(file.ComponentKey, file.RootComponent)]; group != nil {
			group.supporting = append(group.supporting, file)
		}
	}

	identities := make([]string, 0, len(grouped))
	for identity := range grouped {
		identities = append(identities, identity)
	}
	sort.Strings(identities)
	components := make([]sharedmodel.Component, 0, len(identities))
	for _, identity := range identities {
		group := grouped[identity]
		key := group.triggering[0].ComponentKey
		root := group.triggering[0].RootComponent
		var relevantManifests []sharedmodel.SourceFile
		if !root {
			relevantManifests = rootManifests
		}
		components = append(components, sharedmodel.Component{
			Key:              key,
			RootComponent:    root,
			Document:         componentDocumentPath(docsDir, key, root),
			TriggeringFiles:  group.triggering,
			SupportingFiles:  group.supporting,
			RelevantManifest: relevantManifests,
		})
	}
	return components, ownedFiles, nil
}

func componentKey(repositoryPath string, policy documentationmodel.ComponentPolicy, manifestRoots map[string]struct{}) (string, bool) {
	for _, root := range rootsBySpecificity(policy.Roots) {
		if repositoryPath == root || strings.HasPrefix(repositoryPath, root+"/") {
			return root, false
		}
	}
	if policy.Strategy == "explicit" {
		return rootComponentKey, true
	}

	directory := path.Dir(repositoryPath)
	for directory != "." {
		if _, exists := manifestRoots[directory]; exists {
			return directory, false
		}
		directory = path.Dir(directory)
	}

	segments := strings.Split(repositoryPath, "/")
	if len(segments) == 1 {
		return rootComponentKey, true
	}
	if _, conventional := conventionalComponentContainers[segments[0]]; conventional {
		if len(segments) == 2 {
			return segments[0], false
		}
		return strings.Join(segments[:2], "/"), false
	}
	return segments[0], false
}

func componentIdentity(key string, root bool) string {
	if root {
		return "0:" + key
	}
	return "1:" + key
}

func rootsBySpecificity(roots []string) []string {
	result := append([]string(nil), roots...)
	sort.Slice(result, func(left, right int) bool {
		leftDepth := strings.Count(result[left], "/")
		rightDepth := strings.Count(result[right], "/")
		if leftDepth != rightDepth {
			return leftDepth > rightDepth
		}
		if len(result[left]) != len(result[right]) {
			return len(result[left]) > len(result[right])
		}
		return result[left] < result[right]
	})
	return result
}

func validateComponentPolicy(policy documentationmodel.ComponentPolicy) error {
	if policy.Strategy != "inferred" && policy.Strategy != "explicit" {
		return fmt.Errorf("component strategy must be %q or %q", "inferred", "explicit")
	}
	if policy.Strategy == "explicit" && len(policy.Roots) == 0 {
		return fmt.Errorf("explicit component strategy requires at least one root")
	}
	seen := make(map[string]struct{}, len(policy.Roots))
	for _, root := range policy.Roots {
		if err := validateOwnedPath("component root", root, false); err != nil {
			return err
		}
		if _, exists := seen[root]; exists {
			return fmt.Errorf("duplicate component root %q", root)
		}
		seen[root] = struct{}{}
	}
	limits := []struct {
		name  string
		value int64
	}{
		{name: "max context bytes", value: policy.MaxContextBytes},
		{name: "max batch bytes", value: policy.MaxBatchBytes},
		{name: "max supporting bytes", value: policy.MaxSupportingBytes},
		{name: "max manifest bytes", value: policy.MaxManifestBytes},
		{name: "max diff bytes", value: policy.MaxDiffBytes},
		{name: "max request bytes", value: policy.MaxRequestBytes},
	}
	for _, limit := range limits {
		if limit.value <= 0 {
			return fmt.Errorf("component %s must be greater than zero", limit.name)
		}
	}
	if policy.MaxBatchBytes > policy.MaxContextBytes {
		return fmt.Errorf("component max batch bytes must not exceed max context bytes")
	}
	return nil
}

func componentDocumentPath(docsDir, key string, root bool) string {
	if root {
		return path.Join(docsDir, "components", rootComponentKey, "index.md")
	}
	return path.Join(docsDir, "components", encodeComponentPath(key), "index.md")
}

func encodeComponentPath(key string) string {
	segments := strings.Split(key, "/")
	for index, segment := range segments {
		segments[index] = encodeComponentSegment(segment)
	}
	return strings.Join(segments, "/")
}

func decodeComponentPath(encoded string) (string, error) {
	if encoded == rootComponentKey {
		return rootComponentKey, nil
	}
	if encoded == "" || path.Clean(encoded) != encoded || strings.HasPrefix(encoded, "/") {
		return "", fmt.Errorf("invalid encoded component path %q", encoded)
	}
	segments := strings.Split(encoded, "/")
	decoded := make([]string, len(segments))
	for index, segment := range segments {
		value, err := decodeComponentSegment(segment)
		if err != nil {
			return "", err
		}
		decoded[index] = value
	}
	result := strings.Join(decoded, "/")
	if !utf8.ValidString(result) || path.Clean(result) != result {
		return "", fmt.Errorf("invalid decoded component path")
	}
	if encodeComponentPath(result) != encoded {
		return "", fmt.Errorf("non-canonical encoded component path %q", encoded)
	}
	return result, nil
}

func encodeComponentSegment(segment string) string {
	reserved := windowsDeviceName(segment)
	trailingDot := len(segment)
	for trailingDot > 0 && segment[trailingDot-1] == '.' {
		trailingDot--
	}
	var result strings.Builder
	for index := 0; index < len(segment); index++ {
		value := segment[index]
		literal := value >= 'a' && value <= 'z' || value >= '0' && value <= '9' || value == '.' || value == '_' || value == '-'
		if reserved && index == 0 || index >= trailingDot && value == '.' {
			literal = false
		}
		if literal {
			result.WriteByte(value)
			continue
		}
		const hexadecimal = "0123456789ABCDEF"
		result.WriteByte('%')
		result.WriteByte(hexadecimal[value>>4])
		result.WriteByte(hexadecimal[value&0x0f])
	}
	return result.String()
}

func decodeComponentSegment(segment string) (string, error) {
	if segment == "" {
		return "", fmt.Errorf("empty encoded component segment")
	}
	decoded := make([]byte, 0, len(segment))
	for index := 0; index < len(segment); index++ {
		if segment[index] != '%' {
			decoded = append(decoded, segment[index])
			continue
		}
		if index+2 >= len(segment) {
			return "", fmt.Errorf("invalid percent encoding in %q", segment)
		}
		high, okHigh := uppercaseHex(segment[index+1])
		low, okLow := uppercaseHex(segment[index+2])
		if !okHigh || !okLow {
			return "", fmt.Errorf("invalid percent encoding in %q", segment)
		}
		decoded = append(decoded, high<<4|low)
		index += 2
	}
	return string(decoded), nil
}

func uppercaseHex(value byte) (byte, bool) {
	switch {
	case value >= '0' && value <= '9':
		return value - '0', true
	case value >= 'A' && value <= 'F':
		return value - 'A' + 10, true
	default:
		return 0, false
	}
}

func windowsDeviceName(segment string) bool {
	name := segment
	if index := strings.IndexByte(name, '.'); index >= 0 {
		name = name[:index]
	}
	name = strings.ToUpper(name)
	switch name {
	case "CON", "PRN", "AUX", "NUL":
		return true
	}
	return len(name) == 4 && (strings.HasPrefix(name, "COM") || strings.HasPrefix(name, "LPT")) && name[3] >= '1' && name[3] <= '9'
}
