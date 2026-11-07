package wm

import "fmt"

type ContentType uint8

const (
	ContentNone ContentType = iota
	ContentPhoto
	ContentVideo
	ContentGame
)

var contentTypeNames = []string{ContentNone: "none", ContentPhoto: "photo", ContentVideo: "video", ContentGame: "game"}

func (t ContentType) String() string {
	return enumName(contentTypeNames, t, "ContentType")
}

func (t ContentType) MarshalText() ([]byte, error) {
	return []byte(t.String()), nil
}

func (t *ContentType) UnmarshalText(b []byte) error {
	ct, ok := parseEnum[ContentType](contentTypeNames, string(b))
	if !ok {
		return fmt.Errorf("wm: content type %q is none of %v", b, contentTypeNames)
	}
	*t = ct
	return nil
}

type ContentSource uint8

const (
	ContentFromClient ContentSource = iota
	ContentFromRule
)

var contentSourceNames = []string{ContentFromClient: "client", ContentFromRule: "rule"}

func (s ContentSource) String() string {
	return enumName(contentSourceNames, s, "ContentSource")
}

func (s ContentSource) MarshalText() ([]byte, error) {
	return []byte(s.String()), nil
}

func (s *ContentSource) UnmarshalText(b []byte) error {
	v, ok := parseEnum[ContentSource](contentSourceNames, string(b))
	if !ok {
		return fmt.Errorf("wm: content source %q is none of %v", b, contentSourceNames)
	}
	*s = v
	return nil
}
