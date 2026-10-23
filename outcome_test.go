// SPDX-License-Identifier: MIT
// Copyright (c) 2025-2026 eshu-hq

package confluence

import (
	"reflect"
	"strings"
	"testing"

	sdk "github.com/eshu-hq/eshu/sdk/go/collector"
)

// TestWireStructsCoverEverySDKField fails when a new SDK release adds a field
// to Result or Generation that the wire copies in outcome.go would silently
// drop. The only field the copies add on purpose is trusted_deletion.
func TestWireStructsCoverEverySDKField(t *testing.T) {
	t.Parallel()

	assertCovers(t, reflect.TypeOf(sdk.Result{}), reflect.TypeOf(wireResult{}), nil)
	assertCovers(t, reflect.TypeOf(sdk.Generation{}), reflect.TypeOf(wireGeneration{}), nil)
	extra := fieldTags(reflect.TypeOf(wireGeneration{}))
	for tag := range fieldTags(reflect.TypeOf(sdk.Generation{})) {
		delete(extra, tag)
	}
	if len(extra) != 1 || extra["trusted_deletion,omitempty"] == "" {
		t.Errorf("wireGeneration extras = %v, want exactly trusted_deletion", extra)
	}
}

func fieldTags(typ reflect.Type) map[string]string {
	tags := map[string]string{}
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		tags[field.Tag.Get("json")] = field.Name
	}
	return tags
}

func assertCovers(t *testing.T, sdkType, wireType reflect.Type, skip map[string]bool) {
	t.Helper()
	wire := fieldTags(wireType)
	for tag, name := range fieldTags(sdkType) {
		if skip[tag] {
			continue
		}
		if _, ok := wire[tag]; !ok {
			t.Errorf("%s.%s (json %q) is missing from %s", sdkType.Name(), name, tag, wireType.Name())
		}
	}
	if strings.TrimSpace(sdkType.Name()) == "" {
		t.Fatal("unnamed type")
	}
}
