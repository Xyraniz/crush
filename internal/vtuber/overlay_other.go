//go:build !windows

package vtuber

import "github.com/pkg/browser"

func openOverlay(url string) (func(), error) {
	return func() {}, browser.OpenURL(url + "?browser=1")
}
