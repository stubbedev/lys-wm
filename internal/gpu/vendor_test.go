package gpu

import (
	"errors"
	"os"
	"testing"
)

func TestNvidiaVendorFile(t *testing.T) {
	file := vendorDirs[1] + "/" + nvidiaVendor
	installed := func(p string) bool { return p == file }
	unset := func(string) string { return "" }
	for _, tc := range []struct {
		name    string
		drivers []string
		env     map[string]string
		exists  func(string) bool
		want    string
	}{
		{name: "every card is NVIDIA", drivers: []string{"nvidia"}, exists: installed, want: file},
		{name: "two NVIDIA cards", drivers: []string{"nvidia", "nvidia"}, exists: installed, want: file},
		{name: "a hybrid machine needs Mesa too", drivers: []string{"nvidia", "i915"}, exists: installed},
		{name: "a Mesa machine", drivers: []string{"amdgpu"}, exists: installed},
		{name: "no cards known", exists: installed},
		{name: "the vendor file is missing", drivers: []string{"nvidia"}, exists: func(string) bool { return false }},
		{name: "the user chose vendors", drivers: []string{"nvidia"}, exists: installed, env: map[string]string{vendorFilesEnv: "/mine.json"}},
		{name: "the user chose a directory", drivers: []string{"nvidia"}, exists: installed, env: map[string]string{vendorDirsEnv: "/mine"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			getenv := unset
			if tc.env != nil {
				getenv = func(k string) string { return tc.env[k] }
			}
			got, ok := nvidiaVendorFile(tc.drivers, getenv, tc.exists)
			if got != tc.want || ok != (tc.want != "") {
				t.Errorf("= %q %v, want %q", got, ok, tc.want)
			}
		})
	}
}

func TestTheVendorListIsGoneBeforeAnyChildCouldInheritIt(t *testing.T) {
	broken := errors.New("no EGL")
	for _, want := range []error{nil, broken} {
		seen := ""
		err := withVendorFile("/vendor.json", func() error {
			seen = os.Getenv(vendorFilesEnv)
			return want
		})
		if !errors.Is(err, want) {
			t.Errorf("error = %v, want %v", err, want)
		}
		if seen != "/vendor.json" {
			t.Errorf("open saw %q", seen)
		}
		if _, set := os.LookupEnv(vendorFilesEnv); set {
			t.Error("the vendor list outlived open")
		}
	}
}

func TestAMachineWithMesaKeepsEveryVendor(t *testing.T) {
	if err := WithOwnVendorOnly([]string{"nvidia", "i915"}, func() error {
		if _, set := os.LookupEnv(vendorFilesEnv); set {
			t.Error("a hybrid machine was limited to one vendor")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
