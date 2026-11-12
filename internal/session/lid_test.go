package session

import "testing"

func TestLidClosedIsLogindsManagerLidClosed(t *testing.T) {
	fake, _, lg, _ := openLogind(t, map[string]string{"XDG_SESSION_ID": string(own)})
	t.Cleanup(func() {
		if err := lg.Close(); err != nil {
			t.Error(err)
		}
	})
	for _, closed := range []bool{true, false, true} {
		fake.SetLidClosed(closed)
		got, err := lg.LidClosed()
		if err != nil || got != closed {
			t.Errorf("LidClosed() = %v, %v with logind reporting %v", got, err, closed)
		}
	}
}
