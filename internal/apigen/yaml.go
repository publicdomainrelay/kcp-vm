package apigen

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	kcpSchemaAPIVersion = "apis.kcp.io/v1alpha1"
	kcpExportAPIVersion = "apis.kcp.io/v1alpha2"
	scopeCluster        = "Cluster"
)

func scalar(tag, value string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: tag, Value: value}
}

func str(value string) *yaml.Node {
	return scalar("!!str", value)
}

func boolean(value bool) *yaml.Node {
	if value {
		return scalar("!!bool", "true")
	}
	return scalar("!!bool", "false")
}

func mapping(pairs ...any) *yaml.Node {
	n := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	for i := 0; i < len(pairs); i += 2 {
		n.Content = append(n.Content, str(pairs[i].(string)), pairs[i+1].(*yaml.Node))
	}
	if len(n.Content) == 0 {
		n.Style = yaml.FlowStyle
	}
	return n
}

func sequence(items ...*yaml.Node) *yaml.Node {
	return &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Content: items}
}

func strings2nodes(values []string) []*yaml.Node {
	out := make([]*yaml.Node, len(values))
	for i, v := range values {
		out[i] = str(v)
	}
	return out
}

func schemaNode(s *Schema) *yaml.Node {
	pairs := []any{"type", str(s.Type)}
	if s.Format != "" {
		pairs = append(pairs, "format", str(s.Format))
	}
	if len(s.Enum) > 0 {
		pairs = append(pairs, "enum", sequence(strings2nodes(s.Enum)...))
	}
	if len(s.Properties) > 0 {
		props := mapping()
		props.Style = 0
		for _, p := range s.Properties {
			props.Content = append(props.Content, str(p.Name), schemaNode(p.Schema))
		}
		pairs = append(pairs, "properties", props)
	}
	if req := s.Required(); len(req) > 0 {
		pairs = append(pairs, "required", sequence(strings2nodes(req)...))
	}
	if s.Items != nil {
		pairs = append(pairs, "items", schemaNode(s.Items))
	}
	if s.PreserveUnknown {
		pairs = append(pairs, "x-kubernetes-preserve-unknown-fields", boolean(true))
	}
	return mapping(pairs...)
}

func encodeYAML(doc *yaml.Node) ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(doc); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func (m *Model) APIResourceSchema(r Resource) ([]byte, error) {
	doc := mapping(
		"apiVersion", str(kcpSchemaAPIVersion),
		"kind", str("APIResourceSchema"),
		"metadata", mapping("name", str(r.SchemaName(m.Group, m.Version))),
		"spec", mapping(
			"group", str(m.Group),
			"names", mapping(
				"kind", str(r.Kind),
				"listKind", str(r.ListKind),
				"plural", str(r.Plural),
				"singular", str(r.Singular),
			),
			"scope", str(scopeCluster),
			"versions", sequence(mapping(
				"name", str(m.Version),
				"served", boolean(true),
				"storage", boolean(true),
				"subresources", mapping("status", mapping()),
				"schema", schemaNode(r.Schema),
			)),
		),
	)
	return encodeYAML(doc)
}

type Export struct {
	Name      string
	Resources []Resource
}

func (m *Model) Exports() []Export {
	var out []Export
	index := map[string]int{}
	for _, r := range m.Resources {
		i, ok := index[r.Export]
		if !ok {
			i = len(out)
			index[r.Export] = i
			out = append(out, Export{Name: r.Export})
		}
		out[i].Resources = append(out[i].Resources, r)
	}
	return out
}

func (m *Model) APIExport(e Export) ([]byte, error) {
	var resources []*yaml.Node
	for _, r := range e.Resources {
		resources = append(resources, mapping(
			"group", str(m.Group),
			"name", str(r.Plural),
			"schema", str(r.SchemaName(m.Group, m.Version)),
			"storage", mapping("crd", mapping()),
		))
	}
	doc := mapping(
		"apiVersion", str(kcpExportAPIVersion),
		"kind", str("APIExport"),
		"metadata", mapping("name", str(e.Name)),
		"spec", mapping("resources", sequence(resources...)),
	)
	return encodeYAML(doc)
}

func SchemaFile(r Resource) string {
	return "deploy/" + r.Singular + "-apiresourceschema.yaml"
}

func ExportFile(e Export) string {
	return "deploy/" + e.Resources[0].Singular + "-apiexport.yaml"
}

func SpecHash(manifest []byte) (string, string, error) {
	var doc struct {
		Metadata struct {
			Name string `yaml:"name"`
		} `yaml:"metadata"`
		Spec any `yaml:"spec"`
	}
	if err := yaml.Unmarshal(manifest, &doc); err != nil {
		return "", "", err
	}
	if doc.Metadata.Name == "" || doc.Spec == nil {
		return "", "", fmt.Errorf("not an APIResourceSchema: no metadata.name or spec")
	}
	canonical, err := json.Marshal(doc.Spec)
	if err != nil {
		return "", "", err
	}
	sum := sha256.Sum256(canonical)
	return doc.Metadata.Name, "sha256:" + hex.EncodeToString(sum[:]), nil
}

type RevisionLock map[string]string

func ParseRevisionLock(data []byte) (RevisionLock, error) {
	lock := RevisionLock{}
	if len(bytes.TrimSpace(data)) == 0 {
		return lock, nil
	}
	if err := json.Unmarshal(data, &lock); err != nil {
		return nil, err
	}
	return lock, nil
}

func (l RevisionLock) Admit(name, hash string) error {
	if prior, ok := l[name]; ok && prior != hash {
		plural := strings.SplitN(name, ".", 3)
		kind := name
		if len(plural) == 3 {
			kind = plural[1]
		}
		return fmt.Errorf("the spec of APIResourceSchema %s changed (%s, was %s), and kcp holds "+
			"APIResourceSchema.spec IMMUTABLE: applying it fails on every host that already has it. "+
			"Bump Revision for %s in api/v1alpha1/resources.go so the change ships as a new schema "+
			"the APIExport is re-pointed at", name, hash, prior, kind)
	}
	l[name] = hash
	return nil
}

func (l RevisionLock) Marshal() ([]byte, error) {
	data, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}
