package seat

import sw "github.com/stubbedev/lys-wm/internal/protocol/server/wayland"

type HandoverState struct {
	Capabilities     sw.WlSeatCapability
	EverCapabilities sw.WlSeatCapability
}

func (s *Seat) HandoverState() HandoverState {
	return HandoverState{Capabilities: s.caps, EverCapabilities: s.everCaps}
}

func (s *Seat) AdoptHandover(st HandoverState) error {
	s.everCaps |= st.EverCapabilities
	s.inherited = st.Capabilities &^ s.caps
	return s.updateCapabilities()
}
