package drm

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"

	kdrm "github.com/stubbedev/lys-lib/drm"
	"github.com/stubbedev/lys-lib/edid"

	"github.com/stubbedev/lys-wm/internal/backend"
)

var _ backend.HDRDisplay = (*Output)(nil)

const (
	propHDRMetadata = "HDR_OUTPUT_METADATA"
	propColorspace  = "Colorspace"
	propMaxBPC      = "max bpc"
	colorspaceHDR   = "BT2020_RGB"
	hdrBPC          = 10

	metadataSize     = 32
	metadataStatic1  = 0
	eotfST2084       = 2
	chromaUnits      = 50000
	minLuminanceUnit = 10000
)

type signal struct {
	meta       backend.HDRMetadata
	blob       uint32
	colorspace uint64
}

func clampU16(v float64) uint16 {
	return uint16(min(max(math.Round(v), 0), math.MaxUint16))
}

func metadataBytes(m backend.HDRMetadata) []byte {
	b := make([]byte, 0, metadataSize)
	b = binary.NativeEndian.AppendUint32(b, metadataStatic1)
	b = append(b, eotfST2084, metadataStatic1)
	for _, c := range [4]edid.Chromaticity{m.Red, m.Green, m.Blue, m.White} {
		b = binary.NativeEndian.AppendUint16(b, clampU16(c.X*chromaUnits))
		b = binary.NativeEndian.AppendUint16(b, clampU16(c.Y*chromaUnits))
	}
	for _, v := range [4]uint16{clampU16(m.MaxLuminance), clampU16(m.MinLuminance * minLuminanceUnit), clampU16(m.MaxCLL), clampU16(m.MaxFALL)} {
		b = binary.NativeEndian.AppendUint16(b, v)
	}
	return append(b, make([]byte, metadataSize-len(b))...)
}

func (o *Output) HDRCapabilities() (edid.HDR, error) {
	if _, err := o.bt2020(); err != nil {
		return edid.HDR{}, err
	}
	if _, ok := o.connector.props[propHDRMetadata]; !ok {
		return edid.HDR{}, fmt.Errorf("drm: %s: %w: the connector has no %s property", o.Name(), backend.ErrHDRUnsupported, propHDRMetadata)
	}
	switch {
	case o.colorErr != nil:
		return edid.HDR{}, fmt.Errorf("drm: %s: %w: %w", o.Name(), backend.ErrHDRUnsupported, o.colorErr)
	case !o.colorEDID.ST2084:
		return edid.HDR{}, fmt.Errorf("drm: %s: %w: its EDID lists no SMPTE ST 2084 EOTF", o.Name(), backend.ErrHDRUnsupported)
	case !o.colorEDID.BT2020:
		return edid.HDR{}, fmt.Errorf("drm: %s: %w: its EDID lists no BT.2020 RGB colorimetry", o.Name(), backend.ErrHDRUnsupported)
	}
	return o.colorEDID, nil
}

func (o *Output) bt2020() (uint64, error) {
	p, ok := o.connector.props[propColorspace]
	if !ok {
		return 0, fmt.Errorf("drm: %s: %w: the connector has no %s property", o.Name(), backend.ErrHDRUnsupported, propColorspace)
	}
	info, err := o.b.dev.PropertyInfo(p.ID)
	if err != nil {
		return 0, fmt.Errorf("drm: the %s property of %s: %w", propColorspace, o.Name(), err)
	}
	v, ok := info.Enums[colorspaceHDR]
	if !ok {
		return 0, fmt.Errorf("drm: %s: %w: its %s property has no %s", o.Name(), backend.ErrHDRUnsupported, propColorspace, colorspaceHDR)
	}
	return v, nil
}

func (o *Output) newSignal(m backend.HDRMetadata) (signal, error) {
	if !m.Active {
		return signal{}, nil
	}
	if _, err := o.HDRCapabilities(); err != nil {
		return signal{}, err
	}
	colorspace, err := o.bt2020()
	if err != nil {
		return signal{}, err
	}
	blob, err := o.b.dev.CreateBlob(metadataBytes(m))
	if err != nil {
		return signal{}, fmt.Errorf("drm: the %s of %s: %w", propHDRMetadata, o.Name(), err)
	}
	return signal{meta: m, blob: blob, colorspace: colorspace}, nil
}

func (o *Output) maxBPC() uint64 {
	if (o.depth == backend.Depth10 || o.signal.meta.Active) && !o.bpcRefused {
		return max(o.bpcFound, hdrBPC)
	}
	return o.bpcFound
}

func (o *Output) bpcChanges() bool {
	_, ok := o.connector.props[propMaxBPC]
	return ok && o.maxBPC() != o.bpc
}

func (o *Output) bpcLeft() error {
	if _, ok := o.connector.props[propMaxBPC]; !ok {
		return nil
	}
	props, err := o.b.dev.Properties(o.conn.ID, kdrm.ObjectConnector)
	if err != nil {
		return fmt.Errorf("drm: read the max bpc another master left on %s: %w", o.Name(), err)
	}
	o.bpc = props[propMaxBPC].Value
	return nil
}

func (o *Output) addSignal(r *kdrm.Request) {
	if o.bpcChanges() {
		o.setConnectors(r, propMaxBPC, o.maxBPC())
	}
	var colorspace uint64
	if o.signal.meta.Active {
		colorspace = o.signal.colorspace
	}
	o.setConnectors(r, propColorspace, colorspace)
	o.setConnectors(r, propHDRMetadata, uint64(o.signal.blob))
}

func (o *Output) releaseSignal() error {
	blob := o.signal.blob
	o.signal.blob, o.signal.meta = 0, backend.HDRMetadata{}
	if blob == 0 {
		return nil
	}
	return o.b.dev.DestroyBlob(blob)
}

func (o *Output) dropSignal(s signal) error {
	if s.blob == 0 {
		return nil
	}
	return o.b.dev.DestroyBlob(s.blob)
}

var errNoEDID = errors.New("the connector has no EDID")
