#include <errno.h>
#include <fcntl.h>
#include <stdarg.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/mman.h>
#include <unistd.h>

#include <drm_fourcc.h>
#include <xf86drm.h>
#include <xf86drmMode.h>

#define CARDS 16
#define COLOR 0x00ff8000u

static void die(const char *format, ...) {
	va_list args;
	va_start(args, format);
	fputs("splash-handover: ", stderr);
	vfprintf(stderr, format, args);
	fputc('\n', stderr);
	va_end(args);
	exit(1);
}

static int open_vkms(char *path, size_t size) {
	for (int i = 0; i < CARDS; i++) {
		snprintf(path, size, "/dev/dri/card%d", i);
		int fd = open(path, O_RDWR | O_CLOEXEC);
		if (fd < 0) {
			continue;
		}
		drmVersionPtr version = drmGetVersion(fd);
		int vkms = version != NULL && strcmp(version->name, "vkms") == 0;
		drmFreeVersion(version);
		if (vkms) {
			return fd;
		}
		close(fd);
	}
	die("no vkms card among /dev/dri/card0..%d", CARDS - 1);
	return -1;
}

static uint32_t property(int fd, uint32_t object, uint32_t type, const char *name) {
	drmModeObjectPropertiesPtr props = drmModeObjectGetProperties(fd, object, type);
	if (props == NULL) {
		die("read the properties of object %u: %s", object, strerror(errno));
	}
	uint32_t id = 0;
	for (uint32_t i = 0; i < props->count_props && id == 0; i++) {
		drmModePropertyPtr p = drmModeGetProperty(fd, props->props[i]);
		if (p != NULL && strcmp(p->name, name) == 0) {
			id = p->prop_id;
		}
		drmModeFreeProperty(p);
	}
	drmModeFreeObjectProperties(props);
	if (id == 0) {
		die("object %u has no %s property", object, name);
	}
	return id;
}

static uint64_t value(int fd, uint32_t object, uint32_t type, const char *name) {
	uint32_t id = property(fd, object, type, name);
	drmModeObjectPropertiesPtr props = drmModeObjectGetProperties(fd, object, type);
	uint64_t v = 0;
	for (uint32_t i = 0; i < props->count_props; i++) {
		if (props->props[i] == id) {
			v = props->prop_values[i];
		}
	}
	drmModeFreeObjectProperties(props);
	return v;
}

static drmModeModeInfo *choose(drmModeConnectorPtr conn, const char *which) {
	drmModeModeInfo *preferred = NULL;
	for (int i = 0; i < conn->count_modes; i++) {
		if (conn->modes[i].type & DRM_MODE_TYPE_PREFERRED) {
			preferred = &conn->modes[i];
			break;
		}
	}
	if (preferred == NULL) {
		die("connector %u has no preferred mode", conn->connector_id);
	}
	if (strcmp(which, "preferred") == 0) {
		return preferred;
	}
	if (strcmp(which, "other") != 0) {
		die("mode %s is neither preferred nor other", which);
	}
	for (int i = 0; i < conn->count_modes; i++) {
		drmModeModeInfo *m = &conn->modes[i];
		if (m->hdisplay != preferred->hdisplay || m->vdisplay != preferred->vdisplay) {
			return m;
		}
	}
	die("connector %u has no mode of another size than %s", conn->connector_id, preferred->name);
	return NULL;
}

static uint32_t primary_plane(int fd, uint32_t crtc_index) {
	drmModePlaneResPtr planes = drmModeGetPlaneResources(fd);
	if (planes == NULL) {
		die("read the planes: %s", strerror(errno));
	}
	uint32_t found = 0;
	for (uint32_t i = 0; i < planes->count_planes && found == 0; i++) {
		drmModePlanePtr p = drmModeGetPlane(fd, planes->planes[i]);
		if (p != NULL && p->possible_crtcs & (1u << crtc_index) && value(fd, p->plane_id, DRM_MODE_OBJECT_PLANE, "type") == DRM_PLANE_TYPE_PRIMARY) {
			found = p->plane_id;
		}
		drmModeFreePlane(p);
	}
	drmModeFreePlaneResources(planes);
	if (found == 0) {
		die("no primary plane for CRTC index %u", crtc_index);
	}
	return found;
}

static uint32_t fill(int fd, uint32_t width, uint32_t height) {
	struct drm_mode_create_dumb create = {.width = width, .height = height, .bpp = 32};
	if (drmIoctl(fd, DRM_IOCTL_MODE_CREATE_DUMB, &create) != 0) {
		die("create a %ux%u dumb buffer: %s", width, height, strerror(errno));
	}
	struct drm_mode_map_dumb map = {.handle = create.handle};
	if (drmIoctl(fd, DRM_IOCTL_MODE_MAP_DUMB, &map) != 0) {
		die("map the dumb buffer: %s", strerror(errno));
	}
	uint8_t *pix = mmap(NULL, create.size, PROT_READ | PROT_WRITE, MAP_SHARED, fd, (off_t)map.offset);
	if (pix == MAP_FAILED) {
		die("mmap the dumb buffer: %s", strerror(errno));
	}
	for (uint32_t y = 0; y < height; y++) {
		uint32_t *row = (uint32_t *)(pix + (size_t)y * create.pitch);
		for (uint32_t x = 0; x < width; x++) {
			row[x] = COLOR;
		}
	}
	munmap(pix, create.size);
	uint32_t handles[4] = {create.handle}, pitches[4] = {create.pitch}, offsets[4] = {0};
	uint32_t fb;
	if (drmModeAddFB2(fd, width, height, DRM_FORMAT_XRGB8888, handles, pitches, offsets, &fb, 0) != 0) {
		die("add the framebuffer: %s", strerror(errno));
	}
	struct drm_mode_destroy_dumb destroy = {.handle = create.handle};
	if (drmIoctl(fd, DRM_IOCTL_MODE_DESTROY_DUMB, &destroy) != 0) {
		die("drop the dumb buffer's handle: %s", strerror(errno));
	}
	return fb;
}

static void add(drmModeAtomicReqPtr req, int fd, uint32_t object, uint32_t type, const char *name, uint64_t v) {
	if (drmModeAtomicAddProperty(req, object, property(fd, object, type, name), v) < 0) {
		die("add %s of object %u to the commit", name, object);
	}
}

static void mark(const char *message) {
	int fd = open("/dev/kmsg", O_WRONLY | O_CLOEXEC);
	if (fd < 0 || write(fd, message, strlen(message)) < 0 || close(fd) != 0) {
		die("write to /dev/kmsg: %s", strerror(errno));
	}
}

int main(int argc, char **argv) {
	if (argc != 2) {
		die("usage: splash-handover preferred|other");
	}
	char path[32];
	int fd = open_vkms(path, sizeof(path));
	if (drmSetClientCap(fd, DRM_CLIENT_CAP_UNIVERSAL_PLANES, 1) != 0 || drmSetClientCap(fd, DRM_CLIENT_CAP_ATOMIC, 1) != 0) {
		die("%s refuses atomic KMS: %s", path, strerror(errno));
	}
	if (drmSetMaster(fd) != 0) {
		die("become DRM master of %s: %s", path, strerror(errno));
	}
	drmModeResPtr res = drmModeGetResources(fd);
	if (res == NULL) {
		die("read the resources of %s: %s", path, strerror(errno));
	}
	drmModeConnectorPtr conn = NULL;
	for (int i = 0; i < res->count_connectors && conn == NULL; i++) {
		conn = drmModeGetConnector(fd, res->connectors[i]);
		if (conn != NULL && (conn->connection != DRM_MODE_CONNECTED || conn->count_modes == 0)) {
			drmModeFreeConnector(conn);
			conn = NULL;
		}
	}
	if (conn == NULL || conn->count_encoders == 0) {
		die("%s has no connected connector with an encoder", path);
	}
	drmModeEncoderPtr enc = drmModeGetEncoder(fd, conn->encoders[0]);
	if (enc == NULL) {
		die("read encoder %u: %s", conn->encoders[0], strerror(errno));
	}
	int index = -1;
	for (int i = 0; i < res->count_crtcs && index < 0; i++) {
		if (enc->possible_crtcs & (1u << i)) {
			index = i;
		}
	}
	if (index < 0) {
		die("encoder %u drives no CRTC", enc->encoder_id);
	}
	uint32_t crtc = res->crtcs[index];
	uint32_t plane = primary_plane(fd, (uint32_t)index);
	drmModeModeInfo *mode = choose(conn, argv[1]);
	uint32_t blob;
	if (drmModeCreatePropertyBlob(fd, mode, sizeof(*mode), &blob) != 0) {
		die("create the mode blob: %s", strerror(errno));
	}
	uint32_t fb = fill(fd, mode->hdisplay, mode->vdisplay);
	drmModeAtomicReqPtr req = drmModeAtomicAlloc();
	add(req, fd, conn->connector_id, DRM_MODE_OBJECT_CONNECTOR, "CRTC_ID", crtc);
	add(req, fd, crtc, DRM_MODE_OBJECT_CRTC, "MODE_ID", blob);
	add(req, fd, crtc, DRM_MODE_OBJECT_CRTC, "ACTIVE", 1);
	add(req, fd, plane, DRM_MODE_OBJECT_PLANE, "FB_ID", fb);
	add(req, fd, plane, DRM_MODE_OBJECT_PLANE, "CRTC_ID", crtc);
	add(req, fd, plane, DRM_MODE_OBJECT_PLANE, "SRC_X", 0);
	add(req, fd, plane, DRM_MODE_OBJECT_PLANE, "SRC_Y", 0);
	add(req, fd, plane, DRM_MODE_OBJECT_PLANE, "SRC_W", (uint64_t)mode->hdisplay << 16);
	add(req, fd, plane, DRM_MODE_OBJECT_PLANE, "SRC_H", (uint64_t)mode->vdisplay << 16);
	add(req, fd, plane, DRM_MODE_OBJECT_PLANE, "CRTC_X", 0);
	add(req, fd, plane, DRM_MODE_OBJECT_PLANE, "CRTC_Y", 0);
	add(req, fd, plane, DRM_MODE_OBJECT_PLANE, "CRTC_W", mode->hdisplay);
	add(req, fd, plane, DRM_MODE_OBJECT_PLANE, "CRTC_H", mode->vdisplay);
	if (drmModeAtomicCommit(fd, req, DRM_MODE_ATOMIC_ALLOW_MODESET, NULL) != 0) {
		die("light %s on CRTC %u: %s", mode->name, crtc, strerror(errno));
	}
	drmModeAtomicFree(req);
	if (drmModeCloseFB(fd, fb) != 0) {
		die("close framebuffer %u: %s", fb, strerror(errno));
	}
	char message[128];
	snprintf(message, sizeof(message), "splash-handover: lit %s on CRTC %u of %s\n", mode->name, crtc, path);
	mark(message);
	fputs(message, stdout);
	drmModeFreeEncoder(enc);
	drmModeFreeConnector(conn);
	drmModeFreeResources(res);
	if (close(fd) != 0) {
		die("close %s: %s", path, strerror(errno));
	}
	return 0;
}
