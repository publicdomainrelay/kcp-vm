package apigen

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/johnandersen777/socialweb-computer-kcp/api/v1alpha1"
)

const (
	TypeObject  = "object"
	TypeString  = "string"
	TypeInteger = "integer"
	TypeBoolean = "boolean"
	TypeArray   = "array"

	RefObjectMeta = "ObjectMeta"
	RefCondition  = "Condition"

	optionalTag = "optional"
)

type Schema struct {
	Type            string
	Format          string
	Enum            []string
	Ref             string
	Properties      []Property
	Closed          bool
	Items           *Schema
	PreserveUnknown bool
}

type Property struct {
	Name     string
	Required bool
	Schema   *Schema
}

func (s *Schema) Required() []string {
	var out []string
	for _, p := range s.Properties {
		if p.Required {
			out = append(out, p.Name)
		}
	}
	return out
}

type Resource struct {
	Kind     string
	ListKind string
	Plural   string
	Singular string
	Revision int
	Export   string
	Schema   *Schema
}

func (r Resource) SchemaName(group, version string) string {
	return fmt.Sprintf("%s-%d.%s.%s", version, r.Revision, r.Plural, group)
}

type Enum struct {
	Name   string
	Values []string
}

type Constant struct {
	Name  string
	Value string
}

type Model struct {
	Group     string
	Version   string
	Resources []Resource
	Named     map[string]*Schema
	Order     []string
	Enums     []Enum
	Constants []Constant
}

var (
	conditionType  = reflect.TypeOf(metav1.Condition{})
	objectMetaType = reflect.TypeOf(metav1.ObjectMeta{})
	typeMetaType   = reflect.TypeOf(metav1.TypeMeta{})
	apiPkgPath     = reflect.TypeOf(v1alpha1.APIResource{}).PkgPath()
)

func APISourceDir(root string) string {
	return filepath.Join(root, "api", "v1alpha1")
}

func BuildV1alpha1(root string) (*Model, error) {
	consts, enumValues, err := readConstants(APISourceDir(root))
	if err != nil {
		return nil, err
	}
	b := &builder{
		model: &Model{
			Group:     v1alpha1.GroupName,
			Version:   v1alpha1.Version,
			Named:     map[string]*Schema{},
			Constants: consts,
		},
		enumValues: enumValues,
	}
	b.model.Named[RefObjectMeta] = &Schema{Type: TypeObject, Ref: RefObjectMeta}
	b.model.Named[RefCondition] = &Schema{Type: TypeObject, Ref: RefCondition, PreserveUnknown: true}
	seen := map[string]bool{}
	for _, r := range v1alpha1.APIResources {
		if seen[r.Kind] {
			return nil, fmt.Errorf("api/v1alpha1.APIResources lists %s twice", r.Kind)
		}
		seen[r.Kind] = true
		if r.Revision < 1 {
			return nil, fmt.Errorf("%s: Revision must be 1 or more, got %d", r.Kind, r.Revision)
		}
		root, err := b.root(reflect.TypeOf(r.Object), r.Kind)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", r.Kind, err)
		}
		b.model.Resources = append(b.model.Resources, Resource{
			Kind:     r.Kind,
			ListKind: r.Kind + "List",
			Plural:   r.Plural,
			Singular: strings.ToLower(r.Kind),
			Revision: r.Revision,
			Export:   r.Export,
			Schema:   root,
		})
	}
	return b.model, nil
}

type builder struct {
	model      *Model
	enumValues map[string][]string
}

func (b *builder) name(s *Schema) {
	b.model.Named[s.Ref] = s
	b.model.Order = append(b.model.Order, s.Ref)
}

func (b *builder) root(t reflect.Type, kind string) (*Schema, error) {
	if t.Kind() != reflect.Struct || t.Name() != kind {
		return nil, fmt.Errorf("the registered Object is %v, want the struct %s", t, kind)
	}
	s := &Schema{Type: TypeObject, Ref: kind, Closed: true}
	b.name(s)
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		switch {
		case f.Type == typeMetaType:
			s.Properties = append(s.Properties,
				Property{Name: "apiVersion", Schema: &Schema{Type: TypeString}},
				Property{Name: "kind", Schema: &Schema{Type: TypeString}})
		case f.Type == objectMetaType:
			s.Properties = append(s.Properties, Property{Name: "metadata", Schema: b.model.Named[RefObjectMeta]})
		default:
			p, ok, err := b.property(f)
			if err != nil {
				return nil, err
			}
			if ok {
				s.Properties = append(s.Properties, p)
			}
		}
	}
	return s, nil
}

func (b *builder) property(f reflect.StructField) (Property, bool, error) {
	tag := f.Tag.Get("json")
	parts := strings.Split(tag, ",")
	name := parts[0]
	if name == "-" {
		return Property{}, false, nil
	}
	if name == "" {
		return Property{}, false, fmt.Errorf("field %s carries no json name", f.Name)
	}
	if f.Anonymous {
		return Property{}, false, fmt.Errorf("field %s is embedded; only TypeMeta may be", f.Name)
	}
	omitempty := false
	for _, o := range parts[1:] {
		if o == "omitempty" {
			omitempty = true
		}
	}
	optional := false
	for _, o := range strings.Split(f.Tag.Get("apigen"), ",") {
		switch o {
		case "":
		case optionalTag:
			optional = true
		default:
			return Property{}, false, fmt.Errorf("field %s: unknown apigen tag option %q", f.Name, o)
		}
	}
	if f.Type.Kind() == reflect.Pointer && !omitempty {
		return Property{}, false, fmt.Errorf("field %s is a pointer without omitempty, which writes null", f.Name)
	}
	s, err := b.schema(f.Type)
	if err != nil {
		return Property{}, false, fmt.Errorf("field %s: %w", f.Name, err)
	}
	return Property{Name: name, Required: !omitempty && !optional, Schema: s}, true, nil
}

func (b *builder) schema(t reflect.Type) (*Schema, error) {
	if t == conditionType {
		return b.model.Named[RefCondition], nil
	}
	switch t.Kind() {
	case reflect.Pointer:
		return b.schema(t.Elem())
	case reflect.String:
		if t.PkgPath() == "" {
			return &Schema{Type: TypeString}, nil
		}
		if t.PkgPath() != apiPkgPath {
			return nil, fmt.Errorf("named string type %v is outside api/v1alpha1", t)
		}
		if s, ok := b.model.Named[t.Name()]; ok {
			return s, nil
		}
		values := b.enumValues[t.Name()]
		if len(values) == 0 {
			return nil, fmt.Errorf("named string type %s declares no constants, so it has no enum", t.Name())
		}
		s := &Schema{Type: TypeString, Enum: values, Ref: t.Name()}
		b.name(s)
		b.model.Enums = append(b.model.Enums, Enum{Name: t.Name(), Values: values})
		return s, nil
	case reflect.Bool:
		return &Schema{Type: TypeBoolean}, nil
	case reflect.Int:
		return &Schema{Type: TypeInteger}, nil
	case reflect.Int32:
		return &Schema{Type: TypeInteger, Format: "int32"}, nil
	case reflect.Int64:
		return &Schema{Type: TypeInteger, Format: "int64"}, nil
	case reflect.Slice:
		items, err := b.schema(t.Elem())
		if err != nil {
			return nil, err
		}
		return &Schema{Type: TypeArray, Items: items}, nil
	case reflect.Struct:
		if t.PkgPath() != apiPkgPath {
			return nil, fmt.Errorf("struct %v is outside api/v1alpha1 and has no mapping", t)
		}
		if s, ok := b.model.Named[t.Name()]; ok {
			return s, nil
		}
		s := &Schema{Type: TypeObject, Ref: t.Name(), Closed: true}
		b.name(s)
		for i := 0; i < t.NumField(); i++ {
			p, ok, err := b.property(t.Field(i))
			if err != nil {
				return nil, fmt.Errorf("%s: %w", t.Name(), err)
			}
			if ok {
				s.Properties = append(s.Properties, p)
			}
		}
		return s, nil
	default:
		return nil, fmt.Errorf("kind %v of %v has no OpenAPI mapping", t.Kind(), t)
	}
}

func readConstants(dir string) ([]Constant, map[string][]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil, err
	}
	var files []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		files = append(files, filepath.Join(dir, name))
	}
	sort.Strings(files)
	fset := token.NewFileSet()
	var consts []Constant
	enums := map[string][]string{}
	for _, path := range files {
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return nil, nil, err
		}
		for _, decl := range f.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.CONST {
				continue
			}
			for _, spec := range gen.Specs {
				vs := spec.(*ast.ValueSpec)
				for i, name := range vs.Names {
					if i >= len(vs.Values) || !name.IsExported() {
						continue
					}
					lit, ok := vs.Values[i].(*ast.BasicLit)
					if !ok || lit.Kind != token.STRING {
						continue
					}
					value, err := strconv.Unquote(lit.Value)
					if err != nil {
						return nil, nil, fmt.Errorf("%s: %w", fset.Position(lit.Pos()), err)
					}
					if ident, ok := vs.Type.(*ast.Ident); ok {
						enums[ident.Name] = append(enums[ident.Name], value)
						continue
					}
					if vs.Type != nil {
						continue
					}
					consts = append(consts, Constant{Name: name.Name, Value: value})
				}
			}
		}
	}
	return consts, enums, nil
}
