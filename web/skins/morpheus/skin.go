// Package morpheus implements the Morpheus web-component skin (Scope 3).
// Uses neo.* web components directly — no translation layer.
//
// The Morpheus bundle is vendorized (SHA-pinned) under
// web/resources/static/morpheus/ and served at /static/morpheus/*.
// The VENDOR_SHA in this directory records the exact commit from
// github.com/romshark/morpheus for CI verification.
package morpheus

import (
	"log"

	"github.com/a-h/templ"

	"github.com/calionauta/gogogo-fullstack-template/web/skins"
)

func init() {
	log.Println("skins: registering morpheus (web components)")
	skins.Register(skins.Skin{
		Name:   "morpheus",
		Assets: assets,
	})
}

// assets returns the <link>/<script> templ component for Morpheus
// assets. The bundle includes all 48+ custom elements, the morpheus
// CSS theme, and theming utilities.
func assets() templ.Component {
	return templ.Raw(`
<link rel="stylesheet" href="/static/morpheus/theme-default.css"/>
<link rel="stylesheet" href="/static/morpheus/morpheus.css"/>
<script src="/static/iconify-icon.min.js"></script>
<script defer type="module" src="/static/datastar.js"></script>
<script defer src="/static/morpheus/bundle.js"></script>
	`)
}
