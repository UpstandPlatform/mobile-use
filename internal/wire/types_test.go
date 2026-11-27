package wire

import (
	"reflect"
	"testing"
)

func TestAppLaunchNeedsOnlyAppsCapability(t *testing.T) {
	for _, kind := range []string{"app_launch", "app_terminate"} {
		capabilities, err := (Operation{ID: "operation-1", Kind: kind, AppID: "com.instagram.android"}).Required()
		if err != nil {
			t.Fatalf("%s capability validation: %v", kind, err)
		}
		if !reflect.DeepEqual(capabilities, []Capability{Apps}) {
			t.Errorf("%s capabilities = %v, want only Apps", kind, capabilities)
		}
	}
}
