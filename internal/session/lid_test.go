package session

import (
	"strings"
	"testing"

	"github.com/stubbedev/lys-lib/dbustest"
	"github.com/stubbedev/lys-lib/dbusx"
	"github.com/stubbedev/lys-lib/logind"
)

func TestLidClosedIsLogindsManagerLidClosed(t *testing.T) {
	for _, closed := range []bool{true, false} {
		bus := dbustest.Start(t)
		service := bus.Conn(t)
		if _, err := dbusx.Export(service, logind.ManagerPath, dbusx.Interface{Name: logind.ManagerInterface, Methods: struct{}{}, Properties: dbusx.Getters{"LidClosed": func() any { return closed }}}); err != nil {
			t.Fatal(err)
		}
		if _, err := dbusx.Own(service, logind.BusName); err != nil {
			t.Fatal(err)
		}
		got, err := (&Logind{conn: bus.Conn(t)}).LidClosed()
		if err != nil || got != closed {
			t.Errorf("LidClosed() = %v, %v with logind reporting %v", got, err, closed)
		}
	}
}

func TestALidStateLogindDoesNotGiveIsAnError(t *testing.T) {
	_, _, lg, _ := openLogind(t, map[string]string{"XDG_SESSION_ID": string(own)})
	t.Cleanup(func() {
		if err := lg.Close(); err != nil {
			t.Error(err)
		}
	})
	closed, err := lg.LidClosed()
	if err == nil || closed || !strings.Contains(err.Error(), "Manager.LidClosed") {
		t.Fatalf("LidClosed() = %v, %v from a logind without the property, want an error naming it", closed, err)
	}
}
