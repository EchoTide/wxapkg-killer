package main

import (
	"testing"

	"github.com/wux1an/wxapkg/wechat"
)

func TestFirstWxapkgItemEmpty(t *testing.T) {
	if _, err := firstWxapkgItem(nil); err == nil {
		t.Fatal("nil 切片应报错")
	}
	if _, err := firstWxapkgItem([]wechat.WxapkgItem{}); err == nil {
		t.Fatal("空切片应报错")
	}
}

func TestFirstWxapkgItemReturnsFirst(t *testing.T) {
	items := []wechat.WxapkgItem{{WxId: "wx111"}, {WxId: "wx222"}}
	got, err := firstWxapkgItem(items)
	if err != nil {
		t.Fatal(err)
	}
	if got.WxId != "wx111" {
		t.Fatalf("got %s", got.WxId)
	}
}
