package wm

import (
	"encoding/json"
	"testing"
)

func TestContentTypesRoundTripThroughJSON(t *testing.T) {
	for _, ct := range []ContentType{ContentNone, ContentPhoto, ContentVideo, ContentGame} {
		raw, err := json.Marshal(ct)
		if err != nil {
			t.Fatal(err)
		}
		var back ContentType
		if err := json.Unmarshal(raw, &back); err != nil {
			t.Fatalf("decode %s: %v", raw, err)
		}
		if back != ct {
			t.Fatalf("%s decoded to %v, want %v", raw, back, ct)
		}
	}
	var ct ContentType
	if err := json.Unmarshal([]byte(`"movie"`), &ct); err == nil {
		t.Fatalf("movie decoded to %v, want an error", ct)
	}
}
