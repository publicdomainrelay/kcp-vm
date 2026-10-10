package marketbed

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

type Object map[string]any

func (o Object) Kind() string       { s, _ := o["kind"].(string); return s }
func (o Object) APIVersion() string { s, _ := o["apiVersion"].(string); return s }

func (o Object) Group() string {
	v := o.APIVersion()
	if i := strings.Index(v, "/"); i >= 0 {
		return v[:i]
	}
	return ""
}

func (o Object) Name() string {
	md, _ := o["metadata"].(map[string]any)
	s, _ := md["name"].(string)
	return s
}

func DecodeFile(path string) ([]Object, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Decode(raw)
}

func Decode(raw []byte) ([]Object, error) {
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	var out []Object
	for {
		var doc any
		err := dec.Decode(&doc)
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		if doc == nil {
			continue
		}
		normalized, err := json.Marshal(doc)
		if err != nil {
			return nil, err
		}
		var obj Object
		if err := json.Unmarshal(normalized, &obj); err != nil {
			return nil, err
		}
		out = append(out, obj)
	}
}

type Schema struct {
	Type                 string             `json:"type"`
	Format               string             `json:"format"`
	Properties           map[string]*Schema `json:"properties"`
	Required             []string           `json:"required"`
	Items                *Schema            `json:"items"`
	AdditionalProperties json.RawMessage    `json:"additionalProperties"`
	Enum                 []any              `json:"enum"`
	Default              any                `json:"default"`
	Description          string             `json:"description"`
	PreserveUnknown      bool               `json:"x-kubernetes-preserve-unknown-fields"`
}

type ResourceSchema struct {
	Name     string
	Group    string
	Kind     string
	Plural   string
	Versions map[string]*Schema
}

func ParseResourceSchema(o Object) (ResourceSchema, error) {
	var rs ResourceSchema
	normalized, err := json.Marshal(o)
	if err != nil {
		return rs, err
	}
	var doc struct {
		Metadata struct {
			Name string `json:"name"`
		} `json:"metadata"`
		Spec struct {
			Group string `json:"group"`
			Names struct {
				Kind   string `json:"kind"`
				Plural string `json:"plural"`
			} `json:"names"`
			Versions []struct {
				Name   string          `json:"name"`
				Schema json.RawMessage `json:"schema"`
			} `json:"versions"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(normalized, &doc); err != nil {
		return rs, err
	}
	rs = ResourceSchema{
		Name:     doc.Metadata.Name,
		Group:    doc.Spec.Group,
		Kind:     doc.Spec.Names.Kind,
		Plural:   doc.Spec.Names.Plural,
		Versions: map[string]*Schema{},
	}
	for _, v := range doc.Spec.Versions {
		dec := json.NewDecoder(bytes.NewReader(v.Schema))
		dec.DisallowUnknownFields()
		var s Schema
		if err := dec.Decode(&s); err != nil {
			return rs, fmt.Errorf("%s version %s: the schema uses a keyword this check does not know: %w", rs.Name, v.Name, err)
		}
		rs.Versions[v.Name] = &s
	}
	if rs.Group == "" || rs.Kind == "" || len(rs.Versions) == 0 {
		return rs, fmt.Errorf("%s: no group, kind or version", rs.Name)
	}
	return rs, nil
}

func ValidateObject(s *Schema, o Object) []string {
	body := map[string]any{}
	for k, v := range o {
		if k == "metadata" {
			continue
		}
		body[k] = v
	}
	return Validate(s, body)
}

func StrictInto(o Object, out any) error {
	normalized, err := json.Marshal(o)
	if err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(normalized))
	dec.DisallowUnknownFields()
	return dec.Decode(out)
}

func Validate(s *Schema, value any) []string {
	var problems []string
	validate(s, value, "", &problems)
	sort.Strings(problems)
	return problems
}

func validate(s *Schema, value any, at string, problems *[]string) {
	if s == nil {
		return
	}
	where := at
	if where == "" {
		where = "."
	}
	if len(s.Enum) > 0 {
		found := false
		for _, e := range s.Enum {
			if reflect.DeepEqual(e, value) {
				found = true
				break
			}
		}
		if !found {
			*problems = append(*problems, fmt.Sprintf("%s: %v is not one of %v", where, value, s.Enum))
		}
	}
	switch s.Type {
	case "object":
		m, ok := value.(map[string]any)
		if !ok {
			*problems = append(*problems, fmt.Sprintf("%s: want an object, have %T", where, value))
			return
		}
		for _, req := range s.Required {
			if _, ok := m[req]; !ok {
				*problems = append(*problems, fmt.Sprintf("%s: required field %q is missing", where, req))
			}
		}
		additional, additionalAllowed := additionalSchema(s.AdditionalProperties)
		for key, child := range m {
			path := at + "." + key
			if prop, ok := s.Properties[key]; ok {
				validate(prop, child, path, problems)
				continue
			}
			if additional != nil {
				validate(additional, child, path, problems)
				continue
			}
			if s.PreserveUnknown || additionalAllowed {
				continue
			}
			*problems = append(*problems, fmt.Sprintf("%s: field is not in the schema and would be pruned", path))
		}
	case "array":
		list, ok := value.([]any)
		if !ok {
			*problems = append(*problems, fmt.Sprintf("%s: want an array, have %T", where, value))
			return
		}
		for i, item := range list {
			validate(s.Items, item, fmt.Sprintf("%s[%d]", at, i), problems)
		}
	case "string":
		if _, ok := value.(string); !ok {
			*problems = append(*problems, fmt.Sprintf("%s: want a string, have %T", where, value))
		}
	case "integer":
		f, ok := value.(float64)
		if !ok || f != float64(int64(f)) {
			*problems = append(*problems, fmt.Sprintf("%s: want an integer, have %v", where, value))
		}
	case "number":
		if _, ok := value.(float64); !ok {
			*problems = append(*problems, fmt.Sprintf("%s: want a number, have %T", where, value))
		}
	case "boolean":
		if _, ok := value.(bool); !ok {
			*problems = append(*problems, fmt.Sprintf("%s: want a boolean, have %T", where, value))
		}
	case "":
		if !s.PreserveUnknown {
			*problems = append(*problems, fmt.Sprintf("%s: the schema names no type", where))
		}
	default:
		*problems = append(*problems, fmt.Sprintf("%s: the schema names type %q, which this check does not know", where, s.Type))
	}
}

func additionalSchema(raw json.RawMessage) (*Schema, bool) {
	trimmed := strings.TrimSpace(string(raw))
	switch trimmed {
	case "":
		return nil, false
	case "true":
		return nil, true
	case "false":
		return nil, false
	}
	var s Schema
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, false
	}
	return &s, false
}
