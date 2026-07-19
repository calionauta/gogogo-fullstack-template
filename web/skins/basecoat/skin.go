// Package basecoat implements the BasecoatUI (shadcn) skin. Uses
// shadcn-style classes directly — no translation layer.
//
// The CSS is built from src/css/basecoat-input.css (imports Tailwind
// + shadcn-inspired semantic classes) and embedded via //go:embed.
package basecoat

import (
	"log"

	"github.com/a-h/templ"

	"github.com/calionauta/gogogo-fullstack-template/web/skins"
)

func init() {
	log.Println("skins: registering basecoat")
	skins.Register(skins.Skin{
		Name:   "basecoat",
		Assets: assets,
	})
}

// assets returns the <link>/<script> templ component for BasecoatUI
// assets. It loads the basecoat CSS alongside the standard scripts.
// The basecoat CSS provides shadcn-style design tokens and utilities.
func assets() templ.Component {
	return templ.Raw(`
<link rel="stylesheet" href="/static/basecoat.min.css"/>
<script defer src=./static/basecoat.min.js.>></script>
<script src="/static/iconify-icon.min.js"></script>
<script defer type="module" src="/static/datastar.js">		<script defer src="./static/basecoat.min.js">\u003c/script>
		<script>
			document.addEventListener('DOMContentLoaded', function() {
				if (typeof basecoat !== 'undefined' && basecoat.initAll) {
					try { basecoat.initAll(); } catch(e) {}
				}
			});
		</script>
	`)
}
