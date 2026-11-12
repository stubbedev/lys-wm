package drm

import (
	"errors"
	"fmt"
	"image"

	kdrm "github.com/stubbedev/lys-lib/drm"
	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-wm/internal/backend"
)

type kernelDevice interface {
	FD() int
	Path() string
	Number() (uint64, error)
	Cap(capability uint64) (uint64, error)
	Resources() (kdrm.Resources, error)
	Connector(id uint32) (kdrm.Connector, error)
	Encoder(id uint32) (kdrm.Encoder, error)
	Planes() ([]uint32, error)
	Plane(id uint32) (kdrm.Plane, error)
	Properties(object, kind uint32) (map[string]kdrm.Property, error)
	PropertyInfo(id uint32) (kdrm.PropertyInfo, error)
	Blob(id uint32) ([]byte, error)
	CreateBlob(data []byte) (uint32, error)
	DestroyBlob(id uint32) error
	CreateDumb(size image.Point) (kdrm.Dumb, error)
	DestroyDumb(b kdrm.Dumb) error
	AddFB2(fb kdrm.FB) (uint32, error)
	PrimeFDToHandle(fd int) (uint32, error)
	CloseHandle(handle uint32) error
	RemoveFB(id uint32) error
	CloseFB(id uint32) error
	Commit(r *kdrm.Request, flags uint32, userData uint64) error
	CRTCSequence(crtc uint32) (kdrm.Vblank, error)
}

type device struct {
	kernel kernelDevice
}

func (d device) lost(err error) error {
	if !errors.Is(err, unix.ENODEV) || d.answers() {
		return err
	}
	return fmt.Errorf("%w: drm: %s no longer answers: %w", backend.ErrDeviceLost, d.kernel.Path(), err)
}

func (d device) answers() bool {
	_, err := d.kernel.Cap(kdrm.CapAddFB2Modifiers)
	return !errors.Is(err, unix.ENODEV)
}

func (d device) readEvents(buf []byte) (int, error) {
	n, err := unix.Read(d.kernel.FD(), buf)
	switch {
	case err == nil, errors.Is(err, unix.EAGAIN), errors.Is(err, unix.EINTR):
		return n, err
	case errors.Is(err, unix.ENODEV):
		return n, fmt.Errorf("%w: drm: read events from %s: %w", backend.ErrDeviceLost, d.kernel.Path(), err)
	}
	return n, backend.Broken("drm: read events from %s: %w", d.kernel.Path(), err)
}

func (d device) FD() int {
	return d.kernel.FD()
}

func (d device) Path() string {
	return d.kernel.Path()
}

func (d device) Number() (uint64, error) {
	n, err := d.kernel.Number()
	return n, d.lost(err)
}

func (d device) Cap(capability uint64) (uint64, error) {
	v, err := d.kernel.Cap(capability)
	return v, d.lost(err)
}

func (d device) Resources() (kdrm.Resources, error) {
	r, err := d.kernel.Resources()
	return r, d.lost(err)
}

func (d device) Connector(id uint32) (kdrm.Connector, error) {
	c, err := d.kernel.Connector(id)
	return c, d.lost(err)
}

func (d device) Encoder(id uint32) (kdrm.Encoder, error) {
	e, err := d.kernel.Encoder(id)
	return e, d.lost(err)
}

func (d device) Planes() ([]uint32, error) {
	p, err := d.kernel.Planes()
	return p, d.lost(err)
}

func (d device) Plane(id uint32) (kdrm.Plane, error) {
	p, err := d.kernel.Plane(id)
	return p, d.lost(err)
}

func (d device) Properties(object, kind uint32) (map[string]kdrm.Property, error) {
	p, err := d.kernel.Properties(object, kind)
	return p, d.lost(err)
}

func (d device) PropertyInfo(id uint32) (kdrm.PropertyInfo, error) {
	p, err := d.kernel.PropertyInfo(id)
	return p, d.lost(err)
}

func (d device) Blob(id uint32) ([]byte, error) {
	b, err := d.kernel.Blob(id)
	return b, d.lost(err)
}

func (d device) CreateBlob(data []byte) (uint32, error) {
	id, err := d.kernel.CreateBlob(data)
	return id, d.lost(err)
}

func (d device) DestroyBlob(id uint32) error {
	return d.lost(d.kernel.DestroyBlob(id))
}

func (d device) CreateDumb(size image.Point) (kdrm.Dumb, error) {
	b, err := d.kernel.CreateDumb(size)
	return b, d.lost(err)
}

func (d device) DestroyDumb(b kdrm.Dumb) error {
	return d.lost(d.kernel.DestroyDumb(b))
}

func (d device) AddFB2(fb kdrm.FB) (uint32, error) {
	id, err := d.kernel.AddFB2(fb)
	return id, d.lost(err)
}

func (d device) PrimeFDToHandle(fd int) (uint32, error) {
	h, err := d.kernel.PrimeFDToHandle(fd)
	return h, d.lost(err)
}

func (d device) CloseHandle(handle uint32) error {
	return d.lost(d.kernel.CloseHandle(handle))
}

func (d device) RemoveFB(id uint32) error {
	return d.lost(d.kernel.RemoveFB(id))
}

func (d device) CloseFB(id uint32) error {
	return d.lost(d.kernel.CloseFB(id))
}

func (d device) Commit(r *kdrm.Request, flags uint32, userData uint64) error {
	return d.lost(d.kernel.Commit(r, flags, userData))
}

func (d device) CRTCSequence(crtc uint32) (kdrm.Vblank, error) {
	v, err := d.kernel.CRTCSequence(crtc)
	return v, d.lost(err)
}
