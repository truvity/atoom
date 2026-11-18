package common_test

import (
	"testing"

	"github.com/truvity/atoom/common"
)

func TestTheLibraryHasItsName(t *testing.T) {
	if common.Name != "atoom/common" {
		t.Fatalf("Name = %q", common.Name)
	}
}
