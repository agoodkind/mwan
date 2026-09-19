package yangpub

/*
#cgo pkg-config: libyang
#include <stdlib.h>
#include <libyang/libyang.h>
*/
import "C"

import (
	"errors"
	"fmt"
	"unsafe"
)

// ErrSchemaClosed means the schema was already closed. A closed handle is
// rejected rather than handed back to libyang as a freed context.
var ErrSchemaClosed = errors.New("yangpub: schema is closed")

// A configuration instance is read strictly, so a node the schema does not
// define is an error rather than a silently ignored key, and state nodes are
// refused. Present validation checks the subtrees the file carries rather than
// a whole datastore, so a mandatory leaf inside a present container is enforced
// while an absent container is not. PRESENT is required, not a relaxation:
// without it libyang validates every implemented module in the context, not
// just the one the document carries. This is stricter than the deploy's
// `yanglint -t config` check, which sets only the two NO_STATE flags and
// ignores unknown nodes unless given `--strict`.
const (
	schemaParseOptions    = C.LYD_PARSE_STRICT | C.LYD_PARSE_NO_STATE
	schemaValidateOptions = C.LYD_VALIDATE_PRESENT | C.LYD_VALIDATE_NO_STATE
)

// Schema is a libyang context holding the modules the gateway's configuration
// is written against.
type Schema struct {
	ctx *C.struct_ly_ctx
}

// schemaModule is one module loaded by name, with the features that must be
// enabled for its types to carry values.
type schemaModule struct {
	name     string
	features []string
}

// schemaModules names the modules a configuration instance needs beyond what
// imports pull in. The interface-type registry is here because only the data
// references it, through the mandatory type leaf. ietf-nat is here because its
// enums are feature-gated and a module loaded with no features has leaves with
// no valid value, which libyang rejects; loading it first with the features the
// deploy enables means the steering module's import finds it already enabled.
// ietf-interfaces, ietf-ip, ietf-inet-types, and ietf-yang-types resolve as
// imports from the same directory.
var schemaModules = []schemaModule{
	{name: "iana-if-type", features: nil},
	{name: "ietf-nat", features: []string{"basic-nat44", "napt44", "dst-nat", "nptv6"}},
	{name: "goodkind-mwan-steering", features: nil},
}

// LoadSchema builds a context from the model files in searchDir, the directory
// the deploy installs the gateway's models into. Each module is loaded without a
// revision, because that directory holds exactly one file per module and the
// revision then has one home. The caller closes the result.
func LoadSchema(searchDir string) (*Schema, error) {
	cSearchDir := C.CString(searchDir)
	defer C.free(unsafe.Pointer(cSearchDir))

	var ctx *C.struct_ly_ctx
	newErr := C.ly_ctx_new(cSearchDir, C.uint16_t(C.LY_CTX_NO_YANGLIBRARY), &ctx)
	if lyFailed(newErr) {
		return nil, fmt.Errorf("yangpub: ly_ctx_new %s: libyang code %d", searchDir, int(newErr))
	}
	for _, module := range schemaModules {
		if err := loadSchemaModule(ctx, module); err != nil {
			C.ly_ctx_destroy(ctx)
			return nil, err
		}
	}
	return &Schema{ctx: ctx}, nil
}

// loadSchemaModule loads one module from the context's search directory,
// handing libyang a NULL-terminated array of feature names or NULL when the
// module needs none.
func loadSchemaModule(ctx *C.struct_ly_ctx, module schemaModule) error {
	cName := C.CString(module.name)
	defer C.free(unsafe.Pointer(cName))

	var cFeatures **C.char
	if len(module.features) > 0 {
		slotSize := C.size_t(unsafe.Sizeof((*C.char)(nil)))
		block := C.malloc(slotSize * C.size_t(len(module.features)+1))
		defer C.free(block)
		slots := unsafe.Slice((**C.char)(block), len(module.features)+1)
		for i, feature := range module.features {
			slots[i] = C.CString(feature)
			defer C.free(unsafe.Pointer(slots[i]))
		}
		slots[len(module.features)] = nil
		cFeatures = (**C.char)(block)
	}
	if loaded := C.ly_ctx_load_module(ctx, cName, nil, cFeatures); loaded == nil {
		return fmt.Errorf("yangpub: load module %s: %s", module.name, lastSchemaError(ctx))
	}
	return nil
}

// ValidateConfigJSON validates data as a configuration instance of the loaded
// modules and returns the first violation libyang reports.
func (s *Schema) ValidateConfigJSON(data []byte) error {
	if s == nil || s.ctx == nil {
		return ErrSchemaClosed
	}
	cData := C.CString(string(data))
	defer C.free(unsafe.Pointer(cData))

	var tree *C.struct_lyd_node
	parseErr := C.lyd_parse_data_mem(s.ctx, cData, C.LYD_JSON,
		C.uint32_t(schemaParseOptions), C.uint32_t(schemaValidateOptions), &tree)
	if tree != nil {
		C.lyd_free_all(tree)
	}
	if lyFailed(parseErr) {
		return fmt.Errorf("yangpub: %s", lastSchemaError(s.ctx))
	}
	return nil
}

// lastSchemaError renders libyang's most recent message, so a rejection never
// returns an empty reason.
func lastSchemaError(ctx *C.struct_ly_ctx) string {
	item := C.ly_err_last(ctx)
	if item == nil || item.msg == nil {
		return "libyang reported no message"
	}
	return C.GoString(item.msg)
}

// Close frees the context. A second call is a no-op.
func (s *Schema) Close() {
	if s == nil || s.ctx == nil {
		return
	}
	C.ly_ctx_destroy(s.ctx)
	s.ctx = nil
}
