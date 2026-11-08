package ipc

import (
	"reflect"
	"strings"
	"testing"

	"github.com/stubbedev/lys-lib/wmipc"
)

func jsonFields(t reflect.Type) map[string]reflect.Type {
	fields := make(map[string]reflect.Type, t.NumField())
	for f := range t.Fields() {
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		fields[name] = f.Type
	}
	return fields
}

func TestClientViewsDecodeWhatTheCompositorSends(t *testing.T) {
	for _, c := range []struct{ view, wire any }{
		{wmipc.Output{}, Output{}},
		{wmipc.Workspace{}, Workspace{}},
		{wmipc.Window{}, Window{}},
	} {
		view, wire := reflect.TypeOf(c.view), jsonFields(reflect.TypeOf(c.wire))
		for name, viewType := range jsonFields(view) {
			wireType, ok := wire[name]
			if !ok {
				t.Errorf("%s.%s is not on the wire", view.Name(), name)
				continue
			}
			if viewType.Kind() != wireType.Kind() {
				t.Errorf("%s.%s is %s, the compositor sends %s", view.Name(), name, viewType, wireType)
			}
		}
	}
}
