package task

import (
	"slices"
	"testing"
)

func TestUIPaths(t *testing.T) {
	ui := []string{
		"web/src/components/Cart.tsx",
		"app/views/carts/show.html.erb",
		"src/App.vue",
		"styles/main.scss",
		"web/src/pages/index.astro",
		"web/src/components/cart-total.ts",
		"web/src/i18n/locales/es/cart.json",
		"public/logo.png",
		"app/src/main/res/layout/activity_main.xml",
		"app/src/main/res/values-es/strings.xml",
		"ios/Base.lproj/Main.storyboard",
		`templates\email.txt`,
		"icons/cart.svg",
	}
	notUI := []string{
		"internal/cart/cart.go",
		"cart/cart_test.go",
		"web/src/components/Cart.test.tsx",
		"web/src/components/__tests__/Cart.tsx",
		"src/cart.spec.ts",
		"migrations/0007_cart.sql",
		"README.md",
		"api/handlers.py",
		"res/raw/data.bin",
	}
	got := UIPaths(slices.Concat(ui, notUI))
	if !slices.Equal(got, ui) {
		t.Errorf("UIPaths = %v\nwant %v", got, ui)
	}
}
