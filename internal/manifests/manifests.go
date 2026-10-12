package manifests

import (
	"errors"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

var ErrMissingValue = errors.New("manifests: a value the template names is unset")

var errStub = errors.New("manifests: not built")

type Index struct {
	Sets      []Set
	Unapplied []Unapplied
}

type Unapplied struct{ Path, Why string }

type Set struct {
	Name, Summary, Values string
	Phases                []Phase
}

type Phase struct {
	Name, Workspace string
	Files           []File
	Before, After   []Wait
}

type File struct {
	Path, Each, As     string
	Kinds, ExceptKinds []string
}

type Wait struct {
	Kind, Name, Workspace, For string
	Capture                    *Capture
	BestEffort                 bool
}

type Capture struct{ Into, From string }

const (
	CaptureAddressPort    = "AddressPort"
	CaptureLogicalCluster = "LogicalCluster"
)

type Text struct {
	Path string
	Body []byte
}

type Object struct {
	Path         string
	Typed        any
	Unstructured *unstructured.Unstructured
}

type Rendered struct {
	Phase     Phase
	Workspace string
	Objects   []Object
}

type None struct{}
type PermissionInventory struct{ ProviderCluster string }
type MarketIdentity struct{ Workspace string }
type Bidder struct{ Name, PrivateKeyHex string }
type Firecracker struct{ NodeImage, NodeBoot, Config, RepoDir, Preinstall, VMM, RunnerImage, WorkRoot string }
type MarketBed struct {
	OrgRoot, BedID, StateDir, DenoDir, GuestTLSDir string
	PDSPrivateKeyHex, BobPDSPrivateKeyHex          string
	Bidders                                        []Bidder
	DispatcherPort, GuestTLSPort                   int
	Firecracker                                    Firecracker
}

func Load() (Index, error)                                      { return Index{}, errStub }
func Parse(b []byte) (Index, error)                             { return Index{}, errStub }
func (ix Index) Set(name string) (Set, bool)                    { return Set{}, false }
func NewValues(name string) (any, error)                        { return nil, errStub }
func (s Set) RenderText(p Phase, v any) (string, []Text, error) { return "", nil, errStub }
func (s Set) RenderPhase(p Phase, v any) (Rendered, error)      { return Rendered{}, errStub }
func (s Set) Render(v any) ([]Rendered, error)                  { return nil, errStub }
func Decode(path string, body []byte) ([]Object, error)         { return nil, errStub }
func SetCaptured(v any, c Capture, raw string) error            { return errStub }
func AdaptedFiles() (map[string]bool, error)                    { return nil, errStub }
func readFile(path string) ([]byte, error)                      { return nil, errStub }
