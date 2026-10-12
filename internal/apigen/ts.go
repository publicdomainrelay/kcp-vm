package apigen

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode"
)

const TSPackageDir = "socialweb-computer-k8s/lib/common/socialweb-computer-k8s-api"

const ComputeProviderSibling = "../hono-compute-provider"

const ComputeProviderTSPackageDir = ComputeProviderSibling + "/lib/common/compute-provider-kcp-common"

type TSTarget struct {
	Dir         string
	Sibling     string
	Exports     []string
	Fixtures    string
	FixturesDir string
}

var TSTargets = []TSTarget{
	{Dir: TSPackageDir, Exports: []string{"clusters", "atprotoidentities", "marketidentities"}},
	{
		Dir:         ComputeProviderTSPackageDir,
		Sibling:     ComputeProviderSibling,
		Exports:     []string{"computevms"},
		Fixtures:    "testdata/computevm",
		FixturesDir: ComputeProviderSibling + "/test/fixtures/computevm",
	},
}

func (m *Model) Subset(exports []string) (*Model, error) {
	want := map[string]bool{}
	for _, e := range exports {
		want[e] = true
	}
	kindFiles := map[string]bool{}
	selectedFiles := map[string]bool{}
	reached := map[string]bool{RefObjectMeta: true, RefCondition: true}
	var walk func(s *Schema)
	walk = func(s *Schema) {
		if s == nil {
			return
		}
		if s.Ref != "" {
			if reached[s.Ref] {
				return
			}
			reached[s.Ref] = true
		}
		for _, p := range s.Properties {
			walk(p.Schema)
		}
		walk(s.Items)
	}
	sub := &Model{Group: m.Group, Version: m.Version, Named: map[string]*Schema{}, TypeFiles: m.TypeFiles}
	found := map[string]bool{}
	for _, r := range m.Resources {
		kindFiles[m.TypeFiles[r.Kind]] = true
		if !want[r.Export] {
			continue
		}
		found[r.Export] = true
		selectedFiles[m.TypeFiles[r.Kind]] = true
		sub.Resources = append(sub.Resources, r)
		walk(r.Schema)
	}
	for _, e := range exports {
		if !found[e] {
			return nil, fmt.Errorf("no APIResource is exported as %s", e)
		}
	}
	for name, s := range m.Named {
		if reached[name] {
			sub.Named[name] = s
		}
	}
	for _, name := range m.Order {
		if reached[name] {
			sub.Order = append(sub.Order, name)
		}
	}
	for _, e := range m.Enums {
		if reached[e.Name] {
			sub.Enums = append(sub.Enums, e)
		}
	}
	for _, c := range m.Constants {
		if selectedFiles[c.File] || !kindFiles[c.File] {
			sub.Constants = append(sub.Constants, c)
		}
	}
	return sub, nil
}

func UpperSnake(name string) string {
	runes := []rune(name)
	var b strings.Builder
	for i, r := range runes {
		if i > 0 && unicode.IsUpper(r) {
			prev := runes[i-1]
			nextLower := i+1 < len(runes) && unicode.IsLower(runes[i+1])
			if unicode.IsLower(prev) || unicode.IsDigit(prev) || (unicode.IsUpper(prev) && nextLower) {
				b.WriteByte('_')
			}
		}
		b.WriteRune(unicode.ToUpper(r))
	}
	return b.String()
}

func enumArrayName(name string) string {
	return UpperSnake(name) + "S"
}

func quote(s string) string {
	data, _ := json.Marshal(s)
	return string(data)
}

func tsType(s *Schema) string {
	if s.Ref != "" {
		return s.Ref
	}
	switch s.Type {
	case TypeString:
		return "string"
	case TypeInteger:
		return "number"
	case TypeBoolean:
		return "boolean"
	case TypeArray:
		return tsType(s.Items) + "[]"
	default:
		return "Record<string, unknown>"
	}
}

func shape(s *Schema, top bool) map[string]any {
	if !top && s.Ref != "" {
		return map[string]any{"ref": s.Ref}
	}
	out := map[string]any{"type": s.Type}
	switch s.Type {
	case TypeString:
		if len(s.Enum) > 0 {
			out["enum"] = s.Enum
		}
	case TypeArray:
		out["items"] = shape(s.Items, false)
	case TypeObject:
		if !s.Closed || s.PreserveUnknown {
			out["open"] = true
			break
		}
		props := map[string]any{}
		for _, p := range s.Properties {
			props[p.Name] = shape(p.Schema, false)
		}
		out["properties"] = props
		required := s.Required()
		if required == nil {
			required = []string{}
		}
		out["required"] = required
	}
	return out
}

const tsObjectMeta = `export interface ObjectMeta {
  name?: string;
  generateName?: string;
  namespace?: string;
  uid?: string;
  resourceVersion?: string;
  generation?: number;
  creationTimestamp?: string;
  deletionTimestamp?: string;
  deletionGracePeriodSeconds?: number;
  labels?: Record<string, string>;
  annotations?: Record<string, string>;
  finalizers?: string[];
  [key: string]: unknown;
}

export type Condition = Record<string, unknown>;
`

const tsResourceInterface = `export interface ApiResource {
  group: string;
  version: string;
  kind: string;
  listKind: string;
  plural: string;
  singular: string;
  schema: string;
  export: string;
}
`

const tsValidator = `type Shape =
  | { type: "string"; enum?: readonly string[] }
  | { type: "integer" }
  | { type: "boolean" }
  | { type: "array"; items: Shape }
  | { type: "object"; open: true }
  | { type: "object"; properties: Readonly<Record<string, Shape>>; required: readonly string[] }
  | { ref: ApiTypeName };

export interface ApiIssue {
  path: string;
  message: string;
}

export class ApiValidationError extends Error {
  constructor(readonly typeName: ApiTypeName, readonly issues: readonly ApiIssue[]) {
    super(` + "`" + `invalid ${typeName}: ${issues.map((i) => ` + "`" + `${i.path || "(root)"}: ${i.message}` + "`" + `).join("; ")}` + "`" + `);
    this.name = "ApiValidationError";
  }
}

function joinPath(path: string, key: string): string {
  return path === "" ? key : ` + "`" + `${path}.${key}` + "`" + `;
}

function describe(value: unknown): string {
  if (value === null) return "null";
  if (Array.isArray(value)) return "an array";
  switch (typeof value) {
    case "object":
      return "an object";
    case "string":
      return "a string";
    case "boolean":
      return "a boolean";
    case "number":
      return "a number";
    default:
      return typeof value;
  }
}

function isPlainObject(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function check(shape: Shape, value: unknown, path: string, out: ApiIssue[]): void {
  if ("ref" in shape) {
    check(SHAPES[shape.ref], value, path, out);
    return;
  }
  switch (shape.type) {
    case "object": {
      if (!isPlainObject(value)) {
        out.push({ path, message: ` + "`" + `expected an object, got ${describe(value)}` + "`" + ` });
        return;
      }
      if ("open" in shape) return;
      for (const [name, prop] of Object.entries(shape.properties)) {
        const v = value[name];
        if (v === undefined) {
          if (shape.required.includes(name)) out.push({ path: joinPath(path, name), message: "missing required field" });
          continue;
        }
        check(prop, v, joinPath(path, name), out);
      }
      for (const key of Object.keys(value).sort()) {
        if (!(key in shape.properties) && value[key] !== undefined) {
          out.push({ path: joinPath(path, key), message: "unknown field" });
        }
      }
      return;
    }
    case "array": {
      if (!Array.isArray(value)) {
        out.push({ path, message: ` + "`" + `expected an array, got ${describe(value)}` + "`" + ` });
        return;
      }
      value.forEach((item, i) => check(shape.items, item, ` + "`" + `${path}[${i}]` + "`" + `, out));
      return;
    }
    case "string": {
      if (typeof value !== "string") {
        out.push({ path, message: ` + "`" + `expected a string, got ${describe(value)}` + "`" + ` });
        return;
      }
      if (shape.enum && !shape.enum.includes(value)) {
        out.push({ path, message: ` + "`" + `${JSON.stringify(value)} is not one of ${shape.enum.join(", ")}` + "`" + ` });
      }
      return;
    }
    case "integer": {
      if (typeof value !== "number" || !Number.isInteger(value)) {
        out.push({ path, message: ` + "`" + `expected an integer, got ${describe(value)}` + "`" + ` });
      }
      return;
    }
    case "boolean": {
      if (typeof value !== "boolean") {
        out.push({ path, message: ` + "`" + `expected a boolean, got ${describe(value)}` + "`" + ` });
      }
      return;
    }
  }
}

export function validate(typeName: ApiTypeName, value: unknown): ApiIssue[] {
  const out: ApiIssue[] = [];
  check(SHAPES[typeName], value, "", out);
  return out;
}
`

func (m *Model) TypeScript() ([]byte, error) {
	var b strings.Builder
	w := func(format string, args ...any) { fmt.Fprintf(&b, format, args...) }

	for _, c := range m.Constants {
		w("export const %s = %s;\n", UpperSnake(c.Name), quote(c.Value))
	}
	w("\n")

	for _, e := range m.Enums {
		quoted := make([]string, len(e.Values))
		for i, v := range e.Values {
			quoted[i] = quote(v)
		}
		w("export const %s = [%s] as const;\n", enumArrayName(e.Name), strings.Join(quoted, ", "))
		w("export type %s = typeof %s[number];\n\n", e.Name, enumArrayName(e.Name))
	}

	w("%s\n", tsObjectMeta)

	for _, name := range m.Order {
		s := m.Named[name]
		if s.Type != TypeObject {
			continue
		}
		w("export interface %s {\n", name)
		for _, p := range s.Properties {
			opt := "?"
			if p.Required {
				opt = ""
			}
			w("  %s%s: %s;\n", p.Name, opt, tsType(p.Schema))
		}
		w("}\n\n")
	}

	w("%s\n", tsResourceInterface)
	var all []string
	for _, r := range m.Resources {
		constName := UpperSnake(r.Kind) + "_RESOURCE"
		all = append(all, constName)
		w("export const %s: ApiResource = {\n", constName)
		w("  group: %s,\n", quote(m.Group))
		w("  version: %s,\n", quote(m.Version))
		w("  kind: %s,\n", quote(r.Kind))
		w("  listKind: %s,\n", quote(r.ListKind))
		w("  plural: %s,\n", quote(r.Plural))
		w("  singular: %s,\n", quote(r.Singular))
		w("  schema: %s,\n", quote(r.SchemaName(m.Group, m.Version)))
		w("  export: %s,\n", quote(r.Export))
		w("};\n\n")
	}
	w("export const API_RESOURCES: readonly ApiResource[] = [%s];\n\n", strings.Join(all, ", "))

	names := make([]string, 0, len(m.Named))
	for name := range m.Named {
		names = append(names, name)
	}
	sort.Strings(names)
	quotedNames := make([]string, len(names))
	for i, n := range names {
		quotedNames[i] = quote(n)
	}
	w("export type ApiTypeName =\n  | %s;\n\n", strings.Join(quotedNames, "\n  | "))

	shapes := map[string]any{}
	for _, name := range names {
		shapes[name] = shape(m.Named[name], true)
	}
	data, err := json.MarshalIndent(shapes, "", "  ")
	if err != nil {
		return nil, err
	}
	w("%s\n", tsValidator)
	w("const SHAPES: Readonly<Record<ApiTypeName, Shape>> = %s;\n", data)

	for _, name := range m.Order {
		s := m.Named[name]
		if s.Ref == RefObjectMeta || s.Ref == RefCondition {
			continue
		}
		w("\nexport function validate%s(value: unknown): ApiIssue[] {\n", name)
		w("  return validate(%s, value);\n}\n", quote(name))
		w("\nexport function is%s(value: unknown): value is %s {\n", name, name)
		w("  return validate(%s, value).length === 0;\n}\n", quote(name))
		w("\nexport function assert%s(value: unknown): asserts value is %s {\n", name, name)
		w("  const issues = validate(%s, value);\n", quote(name))
		w("  if (issues.length > 0) throw new ApiValidationError(%s, issues);\n}\n", quote(name))
		w("\nexport function parse%s(value: unknown): %s {\n", name, name)
		w("  assert%s(value);\n  return value;\n}\n", name)
	}
	return []byte(b.String()), nil
}
