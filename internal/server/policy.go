package server

import (
	"slices"

	"github.com/stubbedev/lys-wm/internal/wire"
)

type objectClass uint8

const (
	classJournal objectClass = iota
	classRefused
	classInert
	classGone
)

type requestClass uint8

const (
	requestLatest requestClass = iota
	requestLatestKeyed
	requestAppend
	requestCount
	requestDropped
	requestCreate
	requestDestructor
)

type objectPolicy struct {
	class    objectClass
	retire   []string
	requests []requestClass
}

var refused = map[string]string{
	"ext_session_lock_v1":         "it holds the session lock",
	"ext_session_lock_surface_v1": "it holds the session lock",
	"wl_shell_surface":            "it uses wl_shell",
	"wp_security_context_v1":      "it holds a sandbox listener",
	"xwayland_surface_v1":         "it is Xwayland",
	"zxdg_exported_v1":            "it exports a window across clients",
	"zxdg_exported_v2":            "it exports a window across clients",
	"zxdg_imported_v1":            "it imports a window across clients",
	"zxdg_imported_v2":            "it imports a window across clients",
}

var retired = map[string][]string{
	"hyprland_toplevel_export_frame_v1":        {"failed"},
	"xx_hotkey_v1":                             {"revoked"},
	"hyprland_focus_grab_v1":                   {"cleared"},
	"ext_data_control_offer_v1":                {},
	"ext_data_control_source_v1":               {"cancelled"},
	"ext_foreign_toplevel_handle_v1":           {"closed"},
	"ext_image_copy_capture_cursor_session_v1": {},
	"ext_image_copy_capture_frame_v1":          {"failed"},
	"ext_image_copy_capture_session_v1":        {"stopped"},
	"ext_workspace_group_handle_v1":            {"removed"},
	"ext_workspace_handle_v1":                  {"removed"},
	"wl_data_offer":                            {},
	"wl_data_source":                           {"cancelled"},
	"xdg_activation_token_v1":                  {},
	"xdg_popup":                                {"popup_done"},
	"zwlr_data_control_offer_v1":               {},
	"zwlr_data_control_source_v1":              {"cancelled"},
	"zwlr_foreign_toplevel_handle_v1":          {"closed"},
	"zwlr_gamma_control_v1":                    {"failed"},
	"zwlr_output_configuration_head_v1":        {},
	"zwlr_output_configuration_v1":             {"cancelled"},
	"zwlr_output_head_v1":                      {"finished"},
	"zwlr_output_mode_v1":                      {"finished"},
	"zwlr_output_power_v1":                     {"failed"},
	"zwlr_screencopy_frame_v1":                 {"failed"},
	"zwp_input_method_keyboard_grab_v2":        {},
	"zwp_input_method_v2":                      {"unavailable"},
	"zwp_input_popup_surface_v2":               {},
	"zwp_linux_buffer_params_v1":               {"failed"},
	"zwp_primary_selection_offer_v1":           {},
	"zwp_primary_selection_source_v1":          {"cancelled"},
	"zwp_tablet_pad_dial_v2":                   {},
	"zwp_tablet_pad_group_v2":                  {},
	"zwp_tablet_pad_ring_v2":                   {},
	"zwp_tablet_pad_strip_v2":                  {},
	"zwp_tablet_pad_v2":                        {"removed"},
	"zwp_tablet_tool_v2":                       {"removed"},
	"zwp_tablet_v2":                            {"removed"},
}

var gone = map[string][]string{
	"wl_callback":                  {"done"},
	"wp_presentation_feedback":     {"discarded"},
	"wp_image_description_info_v1": {"done"},
}

var retireAlways = map[string][]string{
	"ext_data_control_device_v1":      {"selection", "primary_selection"},
	"wl_data_device":                  {"selection"},
	"zwlr_data_control_device_v1":     {"selection", "primary_selection"},
	"zwp_primary_selection_device_v1": {"selection"},
}

var dropped = map[string][]string{
	"hyprland_toplevel_export_manager_v1":      {"capture_toplevel", "capture_toplevel_with_wlr_toplevel_handle"},
	"ext_data_control_device_v1":               {"set_selection", "set_primary_selection"},
	"ext_data_control_manager_v1":              {"create_data_source"},
	"ext_foreign_toplevel_list_v1":             {"stop"},
	"ext_image_copy_capture_manager_v1":        {"create_session", "create_pointer_cursor_session"},
	"ext_workspace_manager_v1":                 {"commit", "stop"},
	"wl_data_device":                           {"start_drag", "set_selection"},
	"wl_data_device_manager":                   {"create_data_source"},
	"wl_display":                               {"sync"},
	"wl_fixes":                                 {"destroy_registry", "ack_global_remove"},
	"wl_pointer":                               {"set_cursor"},
	"wl_shm":                                   {"create_pool"},
	"wp_security_context_manager_v1":           {"create_listener"},
	"wl_shm_pool":                              {"create_buffer", "resize"},
	"wl_subsurface":                            {"place_above", "place_below"},
	"wl_surface":                               {"attach", "damage", "frame", "set_opaque_region", "set_input_region", "commit", "set_buffer_transform", "set_buffer_scale", "damage_buffer", "offset", "get_release"},
	"wp_commit_timer_v1":                       {"set_timestamp"},
	"wp_cursor_shape_device_v1":                {"set_shape"},
	"wp_fifo_v1":                               {"set_barrier", "wait_barrier"},
	"wp_image_description_v1":                  {"get_information"},
	"wp_linux_drm_syncobj_surface_v1":          {"set_acquire_point", "set_release_point"},
	"wp_pointer_warp_v1":                       {"warp_pointer"},
	"wp_presentation":                          {"feedback"},
	"wp_single_pixel_buffer_manager_v1":        {"create_u32_rgba_buffer"},
	"xdg_activation_v1":                        {"get_activation_token", "activate"},
	"xx_hotkey_manager_v1":                     {"create_hotkey"},
	"xdg_surface":                              {"get_popup", "ack_configure"},
	"xdg_system_bell_v1":                       {"ring"},
	"xdg_toplevel":                             {"show_window_menu", "move", "resize", "set_minimized"},
	"xdg_wm_base":                              {"pong"},
	"zwlr_data_control_device_v1":              {"set_selection", "set_primary_selection"},
	"zwlr_data_control_manager_v1":             {"create_data_source"},
	"zwlr_foreign_toplevel_manager_v1":         {"stop"},
	"zwlr_gamma_control_manager_v1":            {"get_gamma_control"},
	"zwlr_layer_surface_v1":                    {"get_popup", "ack_configure"},
	"zwlr_output_manager_v1":                   {"create_configuration", "stop"},
	"zwlr_output_power_manager_v1":             {"get_output_power"},
	"zwlr_screencopy_manager_v1":               {"capture_output", "capture_output_region"},
	"zwlr_virtual_pointer_v1":                  {"motion", "motion_absolute", "button", "axis", "frame", "axis_source", "axis_stop", "axis_discrete"},
	"zwp_input_method_manager_v2":              {"get_input_method"},
	"zwp_linux_dmabuf_v1":                      {"create_params"},
	"zwp_primary_selection_device_manager_v1":  {"create_source"},
	"zwp_primary_selection_device_v1":          {"set_selection"},
	"zwp_tablet_tool_v2":                       {"set_cursor"},
	"zwp_text_input_v1":                        {"reset", "invoke_action"},
	"zwp_virtual_keyboard_v1":                  {"key", "modifiers"},
	"ext_workspace_group_handle_v1":            {"create_workspace"},
	"ext_image_copy_capture_session_v1":        {"create_frame"},
	"ext_image_copy_capture_cursor_session_v1": {"get_capture_session"},
	"zwlr_output_configuration_v1":             {"enable_head", "disable_head", "apply", "test"},
}

var appended = map[string][]string{
	"wl_region":            {"add", "subtract"},
	"xdg_toplevel_icon_v1": {"add_buffer"},
}

var keyed = map[string][]string{
	"xdg_toplevel_tag_manager_v1":     {"set_toplevel_tag", "set_toplevel_description"},
	"hyprland_ctm_control_manager_v1": {"set_ctm_for_output"},
	"xdg_toplevel_icon_manager_v1":    {"set_icon"},
}

var counted = map[string][]string{
	"zwp_text_input_v3": {"commit"},
}

var dropEvents = map[string][]string{
	"wl_registry":             {"global"},
	"wl_shm":                  {"format"},
	"wl_surface":              {"enter"},
	"wp_image_description_v1": {"ready", "ready2"},
	"zwp_linux_dmabuf_v1":     {"format", "modifier"},
}

func policyOf(iface *wire.Interface) *objectPolicy {
	p := &objectPolicy{requests: make([]requestClass, len(iface.Requests))}
	name := iface.Name
	switch {
	case refused[name] != "":
		p.class = classRefused
	case retired[name] != nil:
		p.class, p.retire = classInert, retired[name]
	case gone[name] != nil:
		p.class, p.retire = classGone, gone[name]
	default:
		p.retire = retireAlways[name]
	}
	for i, spec := range iface.Requests {
		p.requests[i] = classify(name, spec)
	}
	return p
}

func classify(name string, spec wire.MessageSpec) requestClass {
	has := func(table map[string][]string) bool { return slices.Contains(table[name], spec.Name) }
	switch {
	case has(dropped):
		return requestDropped
	case spec.Destructor:
		return requestDestructor
	case has(appended):
		return requestAppend
	case has(keyed):
		return requestLatestKeyed
	case has(counted):
		return requestCount
	}
	for _, a := range spec.Args {
		if a.Type == wire.ArgNewID {
			return requestCreate
		}
	}
	return requestLatest
}
