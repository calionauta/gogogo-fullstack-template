// SCOPE:layer=feature,removal=feature — Todo MVC example (reference implementation)
// Package components registers all built-in UI skins at init time so
// the skin dispatcher can resolve them at runtime.
package components

import (
	_ "github.com/calionauta/gogogo-fullstack-template/web/skins/basecoat"
	_ "github.com/calionauta/gogogo-fullstack-template/web/skins/daisyui"
	_ "github.com/calionauta/gogogo-fullstack-template/web/skins/morpheus"
)
