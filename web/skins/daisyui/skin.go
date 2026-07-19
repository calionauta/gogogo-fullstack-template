// Package daisyui implements the default DaisyUI skin. Uses DaisyUI v5
// classes directly — no translation layer.
//
// The CSS assets are built by Tailwind v4 from src/css/input.css
// (which imports DaisyUI as a Tailwind plugin) and embedded via
// //go:embed in web/resources.
package daisyui

import (
	"log"

	"github.com/a-h/templ"

	"github.com/calionauta/gogogo-fullstack-template/web/skins"
)

func init() {
	log.Println("skins: registering daisyui (core default)")
	skins.Register(skins.Skin{
		Name:   "daisyui",
		Assets: assets,
	})
}

// assets returns the <link>/<script> templ component for DaisyUI assets.
// The main CSS (app.min.css) is the Tailwind v4 + DaisyUI v5 build output.
// The baseline app.css provides project-specific overrides.
func assets() templ.Component {
	return templ.Raw(`
<link rel="stylesheet" href="/static/app.min.css"/>
<link rel="stylesheet" href="/static/app.css"/>
<script src="/static/iconify-icon.min.js"></script>
<script defer type="module" src="/static/datastar.js"></script>
	`)
}
