package drm

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	lgpu "github.com/stubbedev/lys-lib/gpu"
)

func TestACardWithoutAVulkanDriverSaysSo(t *testing.T) {
	for _, cause := range []error{lgpu.ErrNoVulkan, lgpu.ErrNoDevice} {
		err := vulkanOpenError("/dev/dri/card1", fmt.Errorf("vulkan: %w: lavapipe drives no DRM node", cause))
		if !errors.Is(err, ErrNoVulkan) || !errors.Is(err, cause) {
			t.Errorf("opening Vulkan failing with %v gives %v, want it to wrap ErrNoVulkan and the cause", cause, err)
		}
		if !strings.Contains(err.Error(), "/dev/dri/card1") {
			t.Errorf("%v does not name the card", err)
		}
	}
}

func TestAVulkanDriverThatFailsOtherwiseIsNotMissing(t *testing.T) {
	cause := errors.New("vkCreateDevice: out of memory")
	err := vulkanOpenError("/dev/dri/card1", cause)
	if errors.Is(err, ErrNoVulkan) || !errors.Is(err, cause) || !strings.Contains(err.Error(), "/dev/dri/card1") {
		t.Errorf("opening Vulkan failing with %v gives %v, want the cause on the card without ErrNoVulkan", cause, err)
	}
}
