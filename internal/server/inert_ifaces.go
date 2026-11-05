package server

import (
	sedc "github.com/stubbedev/lys-wm/internal/protocol/server/extdatacontrol"
	sftl "github.com/stubbedev/lys-wm/internal/protocol/server/extforeigntoplevellist"
	sicc "github.com/stubbedev/lys-wm/internal/protocol/server/extimagecopycapture"
	sws "github.com/stubbedev/lys-wm/internal/protocol/server/extworkspace"
	shfg "github.com/stubbedev/lys-wm/internal/protocol/server/hyprlandfocusgrab"
	sim "github.com/stubbedev/lys-wm/internal/protocol/server/inputmethod"
	sld "github.com/stubbedev/lys-wm/internal/protocol/server/linuxdmabuf"
	sps "github.com/stubbedev/lys-wm/internal/protocol/server/primaryselection"
	stb "github.com/stubbedev/lys-wm/internal/protocol/server/tablet"
	sw "github.com/stubbedev/lys-wm/internal/protocol/server/wayland"
	swdc "github.com/stubbedev/lys-wm/internal/protocol/server/wlrdatacontrol"
	swft "github.com/stubbedev/lys-wm/internal/protocol/server/wlrforeigntoplevel"
	swgc "github.com/stubbedev/lys-wm/internal/protocol/server/wlrgammacontrol"
	swom "github.com/stubbedev/lys-wm/internal/protocol/server/wlroutputmanagement"
	swop "github.com/stubbedev/lys-wm/internal/protocol/server/wlroutputpower"
	swsc "github.com/stubbedev/lys-wm/internal/protocol/server/wlrscreencopy"
	sxa "github.com/stubbedev/lys-wm/internal/protocol/server/xdgactivation"
	sxdg "github.com/stubbedev/lys-wm/internal/protocol/server/xdgshell"
	sxh "github.com/stubbedev/lys-wm/internal/protocol/server/xxhotkey"
	"github.com/stubbedev/lys-wm/internal/wire"
)

var inertInterfaces = func() map[string]*wire.Interface {
	out := map[string]*wire.Interface{}
	for _, iface := range []*wire.Interface{
		sedc.ExtDataControlOfferV1Interface, sedc.ExtDataControlSourceV1Interface,
		sftl.ExtForeignToplevelHandleV1Interface,
		sicc.ExtImageCopyCaptureCursorSessionV1Interface, sicc.ExtImageCopyCaptureFrameV1Interface, sicc.ExtImageCopyCaptureSessionV1Interface,
		sws.ExtWorkspaceGroupHandleV1Interface, sws.ExtWorkspaceHandleV1Interface,
		shfg.HyprlandFocusGrabV1Interface,
		sw.WlDataOfferInterface, sw.WlDataSourceInterface,
		sxa.XdgActivationTokenV1Interface,
		sxdg.XdgPopupInterface, sxdg.XdgSurfaceInterface,
		sxh.XxHotkeyManagerV1Interface, sxh.XxHotkeyV1Interface,
		swdc.ZwlrDataControlOfferV1Interface, swdc.ZwlrDataControlSourceV1Interface,
		swft.ZwlrForeignToplevelHandleV1Interface,
		swgc.ZwlrGammaControlV1Interface,
		swom.ZwlrOutputConfigurationHeadV1Interface, swom.ZwlrOutputConfigurationV1Interface, swom.ZwlrOutputHeadV1Interface, swom.ZwlrOutputModeV1Interface,
		swop.ZwlrOutputPowerV1Interface,
		swsc.ZwlrScreencopyFrameV1Interface,
		sim.ZwpInputMethodKeyboardGrabV2Interface, sim.ZwpInputMethodV2Interface, sim.ZwpInputPopupSurfaceV2Interface,
		sld.ZwpLinuxBufferParamsV1Interface,
		sps.ZwpPrimarySelectionOfferV1Interface, sps.ZwpPrimarySelectionSourceV1Interface,
		stb.ZwpTabletPadDialV2Interface, stb.ZwpTabletPadGroupV2Interface, stb.ZwpTabletPadRingV2Interface, stb.ZwpTabletPadStripV2Interface, stb.ZwpTabletPadV2Interface, stb.ZwpTabletToolV2Interface, stb.ZwpTabletV2Interface,
	} {
		out[iface.Name] = iface
	}
	return out
}()
