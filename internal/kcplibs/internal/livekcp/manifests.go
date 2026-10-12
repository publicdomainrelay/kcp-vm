//go:build linux

package livekcp

func workspaceYAML(name string) string {
	return `apiVersion: tenancy.kcp.io/v1alpha1
kind: Workspace
metadata:
  name: ` + name + `
spec:
  type:
    name: universal
    path: root
`
}

const SchemaYAML = `apiVersion: apis.kcp.io/v1alpha1
kind: APIResourceSchema
metadata:
  name: v1alpha1-1.widgets.example.computer
spec:
  group: example.computer
  names:
    kind: Widget
    listKind: WidgetList
    plural: widgets
    singular: widget
  scope: Namespaced
  versions:
    - name: v1alpha1
      served: true
      storage: true
      subresources:
        status: {}
      schema:
        type: object
        properties:
          apiVersion:
            type: string
          kind:
            type: string
          metadata:
            type: object
          spec:
            type: object
            x-kubernetes-preserve-unknown-fields: true
          status:
            type: object
            x-kubernetes-preserve-unknown-fields: true
`

const ExportYAML = `apiVersion: apis.kcp.io/v1alpha2
kind: APIExport
metadata:
  name: ` + Export + `
spec:
  resources:
    - group: ` + Group + `
      name: ` + Resource + `
      schema: v1alpha1-1.widgets.example.computer
      storage:
        crd: {}
`

const BindingYAML = `apiVersion: apis.kcp.io/v1alpha2
kind: APIBinding
metadata:
  name: ` + Export + `
spec:
  reference:
    export:
      path: ` + "root:" + ProviderWorkspace + `
      name: ` + Export + `
`

const GadgetSchemaYAML = `apiVersion: apis.kcp.io/v1alpha1
kind: APIResourceSchema
metadata:
  name: v1alpha1-1.gadgets.example.computer
spec:
  group: ` + Group + `
  names:
    kind: Gadget
    listKind: GadgetList
    plural: gadgets
    singular: gadget
  scope: Namespaced
  versions:
    - name: v1alpha1
      served: true
      storage: true
      subresources:
        status: {}
      schema:
        type: object
        properties:
          apiVersion:
            type: string
          kind:
            type: string
          metadata:
            type: object
          spec:
            type: object
            x-kubernetes-preserve-unknown-fields: true
          status:
            type: object
            x-kubernetes-preserve-unknown-fields: true
`

const GadgetExportYAML = `apiVersion: apis.kcp.io/v1alpha2
kind: APIExport
metadata:
  name: ` + SecondExport + `
spec:
  resources:
    - group: ` + Group + `
      name: ` + SecondResource + `
      schema: v1alpha1-1.gadgets.example.computer
      storage:
        crd: {}
`

const GadgetBindingYAML = `apiVersion: apis.kcp.io/v1alpha2
kind: APIBinding
metadata:
  name: ` + SecondExport + `
spec:
  reference:
    export:
      path: ` + "root:" + ProviderWorkspace + `
      name: ` + SecondExport + `
`
