package mobile

import (
	"math"
	"testing"

	"github.com/anhydrous99/satlink"
)

// outOfRange are indices outside a two-item list.
var outOfRange = []int{-1, 2, 3, math.MaxInt}

func TestStringListGetsItsItemsAndNothingOutOfRange(t *testing.T) {
	list := &StringList{items: []string{"RING", `+CMTI: "SM",3`}}

	if list.Len() != 2 || list.Get(0) != "RING" || list.Get(1) != `+CMTI: "SM",3` {
		t.Errorf("list holds %d items %q, %q", list.Len(), list.Get(0), list.Get(1))
	}
	for _, index := range outOfRange {
		if got := list.Get(index); got != "" {
			t.Errorf("Get(%d) = %q, want \"\"", index, got)
		}
	}
}

func TestSMSMessageListHandsOutCopiesAndNothingOutOfRange(t *testing.T) {
	list := &SMSMessageList{items: []SMSMessage{{Index: 1, Text: "hello"}, {Index: 2, Text: "again"}}}

	list.Get(0).Text = "changed"

	if list.Len() != 2 || list.Get(0).Text != "hello" || list.Get(1).Index != 2 {
		t.Errorf("list holds %d items, the first %+v", list.Len(), *list.Get(0))
	}
	for _, index := range outOfRange {
		if got := list.Get(index); got != nil {
			t.Errorf("Get(%d) = %+v, want nil", index, *got)
		}
	}
}

func TestKnownProductListHandsOutCopiesAndNothingOutOfRange(t *testing.T) {
	list := &KnownProductList{items: []KnownProduct{{ProductID: 1, Model: "one"}, {ProductID: 2, Model: "two"}}}

	list.Get(1).Model = "changed"

	if list.Len() != 2 || list.Get(1).Model != "two" {
		t.Errorf("list holds %d items, the second %+v", list.Len(), *list.Get(1))
	}
	for _, index := range outOfRange {
		if got := list.Get(index); got != nil {
			t.Errorf("Get(%d) = %+v, want nil", index, *got)
		}
	}
}

func TestZeroListsAreEmpty(t *testing.T) {
	if (&StringList{}).Len() != 0 || (&SMSMessageList{}).Len() != 0 || (&KnownProductList{}).Len() != 0 {
		t.Error("a zero list is not empty")
	}
}

func TestKnownProductsAreTheDriversProducts(t *testing.T) {
	want := satlink.KnownProducts()

	got := KnownProducts()

	if got.Len() != len(want) {
		t.Fatalf("%d products, want %d", got.Len(), len(want))
	}
	for i, product := range want {
		if entry := got.Get(i); entry.ProductID != int(product.ProductID) || entry.Model != product.Model {
			t.Errorf("product %d is %+v, want %+v", i, *entry, product)
		}
	}
}

func TestTheVendorIDIsIridiums(t *testing.T) {
	if got := IridiumUSBVendorID(); got != 0x1EDD {
		t.Errorf("IridiumUSBVendorID() = %#x, want 0x1edd", got)
	}
}
