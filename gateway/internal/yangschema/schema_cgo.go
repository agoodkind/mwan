// Package yangschema validates JSON configuration documents with libyang.
package yangschema

/*
#cgo pkg-config: libyang
#include <stdlib.h>
#include <libyang/libyang.h>
*/
import "C"

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"runtime"
	"strings"
	"unsafe"

	"goodkind.io/mwan/internal/yangpub/schema"
)

// ErrSchemaClosed means the schema was already closed. A closed handle is
// rejected rather than handed back to libyang as a freed context.
var ErrSchemaClosed = errors.New("yangschema: schema is closed")

// libyang rejects undefined nodes and state nodes.
// libyang validates only modules with data in the document.
const (
	schemaParseOptions    = C.LYD_PARSE_STRICT | C.LYD_PARSE_NO_STATE
	schemaValidateOptions = C.LYD_VALIDATE_PRESENT | C.LYD_VALIDATE_NO_STATE
)

const revisionSeparator = "@"

// Schema is a libyang context containing the loaded YANG modules.
type Schema struct {
	ctx *C.struct_ly_ctx
	// Close does not remove directories supplied to LoadSchema.
	tempDir string
}

// LoadSchema loads modules using schemaDir as the search directory.
// The caller must call [Schema.Close] to free the context.
func LoadSchema(schemaDir string) (*Schema, error) {
	ctx, err := newContext(schemaDir)
	if err != nil {
		return nil, err
	}
	return &Schema{ctx: ctx, tempDir: ""}, nil
}

func newContext(schemaDir string) (*C.struct_ly_ctx, error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	defer storeErrorsWithoutPrinting()()

	cSearchDir := C.CString(schemaDir)
	defer C.free(unsafe.Pointer(cSearchDir))

	var ctx *C.struct_ly_ctx
	newErr := C.ly_ctx_new(cSearchDir, C.uint16_t(C.LY_CTX_NO_YANGLIBRARY), &ctx)
	if lyFailed(newErr) {
		return nil, fmt.Errorf("yangschema: ly_ctx_new %s: libyang code %d", schemaDir, int(newErr))
	}
	for _, module := range schema.Modules() {
		if err := loadSchemaModule(ctx, module); err != nil {
			C.ly_ctx_destroy(ctx)
			return nil, err
		}
	}
	return ctx, nil
}

// Call the returned function before unlocking the operating system thread.
func storeErrorsWithoutPrinting() func() {
	options := (*C.uint32_t)(C.malloc(C.size_t(unsafe.Sizeof(C.uint32_t(0)))))
	*options = C.LY_LOSTORE_LAST
	previous := C.ly_temp_log_options(options)
	return func() {
		C.ly_temp_log_options(previous)
		C.free(unsafe.Pointer(options))
	}
}

func lyFailed(result C.LY_ERR) bool {
	return result != C.LY_SUCCESS
}

func moduleName(file string) (string, error) {
	name, _, found := strings.Cut(file, revisionSeparator)
	if !found || name == "" {
		err := fmt.Errorf("yangschema: module file %s has no name before its revision", file)
		slog.Error("yangschema: module name parse failed", "file", file, "err", err)
		return "", err
	}
	return name, nil
}

// loadSchemaModule loads one module from the context's search directory,
// handing libyang a NULL-terminated array of feature names or NULL when the
// module needs none.
func loadSchemaModule(ctx *C.struct_ly_ctx, module schema.Module) error {
	name, err := moduleName(module.File)
	if err != nil {
		return err
	}
	// The load and the read of its error record must run on one operating
	// system thread; see ValidateConfigJSON.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	cName := C.CString(name)
	defer C.free(unsafe.Pointer(cName))

	var cFeatures **C.char
	if len(module.Features) > 0 {
		slotSize := C.size_t(unsafe.Sizeof((*C.char)(nil)))
		block := C.malloc(slotSize * C.size_t(len(module.Features)+1))
		defer C.free(block)
		slots := unsafe.Slice((**C.char)(block), len(module.Features)+1)
		for i, feature := range module.Features {
			slots[i] = C.CString(feature)
			defer C.free(unsafe.Pointer(slots[i]))
		}
		slots[len(module.Features)] = nil
		cFeatures = (**C.char)(block)
	}
	if loaded := C.ly_ctx_load_module(ctx, cName, nil, cFeatures); loaded == nil {
		return fmt.Errorf("yangschema: load module %s: %s", name, lastSchemaError(ctx))
	}
	return nil
}

// ValidateConfigJSON validates data as a configuration instance of the loaded
// modules and returns the first violation libyang reports.
func (s *Schema) ValidateConfigJSON(data []byte) error {
	if s == nil || s.ctx == nil {
		return ErrSchemaClosed
	}
	// libyang keeps its error record per operating system thread, and the
	// parse and the read of its message are two cgo calls. The goroutine is
	// pinned to one thread across both, because a goroutine moved between
	// them reads an empty record and reports a rejection with no cause.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	defer storeErrorsWithoutPrinting()()

	cData := C.CString(string(data))
	defer C.free(unsafe.Pointer(cData))

	var tree *C.struct_lyd_node
	parseErr := C.lyd_parse_data_mem(s.ctx, cData, C.LYD_JSON,
		C.uint32_t(schemaParseOptions), C.uint32_t(schemaValidateOptions), &tree)
	if tree != nil {
		C.lyd_free_all(tree)
	}
	if lyFailed(parseErr) {
		return fmt.Errorf("yangschema: %s", lastSchemaError(s.ctx))
	}
	return nil
}

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
	if s.tempDir == "" {
		return
	}
	if err := os.RemoveAll(s.tempDir); err != nil {
		slog.Warn("yangschema: remove the temporary schema directory failed",
			"dir", s.tempDir, "err", err)
	}
	s.tempDir = ""
}
