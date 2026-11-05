package drm

import (
	"fmt"

	kdrm "github.com/stubbedev/lys-lib/drm"

	"github.com/stubbedev/lys-wm/internal/backend"
	"github.com/stubbedev/lys-wm/internal/wm"
)

var _ backend.ContentTyped = (*Output)(nil)

const propContentType = "content type"

var contentTypeEnums = [...]string{wm.ContentNone: "No Data", wm.ContentPhoto: "Photo", wm.ContentVideo: "Cinema", wm.ContentGame: "Game"}

func (o *Output) SetContentType(t wm.ContentType) {
	if _, ok := o.connector.props[propContentType]; !ok || t == o.contentType || int(t) >= len(contentTypeEnums) {
		return
	}
	o.contentType, o.contentDirty = t, true
}

func (o *Output) addContentType(r *kdrm.Request) {
	p, ok := o.connector.props[propContentType]
	if !ok || !o.contentDirty && o.kms == kmsFlip {
		return
	}
	info, err := o.b.dev.PropertyInfo(p.ID)
	if err != nil {
		o.contentDirty = false
		o.b.report(fmt.Errorf("drm: the %s property of %s: %w", propContentType, o.Name(), err))
		return
	}
	if v, ok := info.Enums[contentTypeEnums[o.contentType]]; ok {
		o.connector.set(r, propContentType, v)
	}
}
