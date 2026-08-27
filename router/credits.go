// SCOPE:layer=feature,removal=plugin — Wire the AI credits + BYOK feature
// into the router. To remove: delete this file + the block in router.go that
// calls registerCredits + delete features/credits/ + config Credits fields
// + go.mod require/replace.
//
// registerCredits builds the credits service from config and returns it so
// router.Init can RegisterRoutes. Returns nil (and no-ops) when
// CREDITS_ENABLED=false or the lib fails to init — the binary boots
// billing-free either way.
package router

import (
	"log/slog"

	"github.com/pocketbase/pocketbase/core"

	"github.com/calionauta/gogogo-fullstack-template/config"
	"github.com/calionauta/gogogo-fullstack-template/features/credits"
)

func registerCredits(cfg *config.Config) *credits.Service {
	if !cfg.Credits.Enabled {
		return nil
	}
	svc, err := credits.New(cfg)
	if err != nil {
		slog.Error("credits: init disabled", "err", err)
		return nil
	}
	return svc
}

// registerCreditsRoutes builds the credits service (if enabled) and mounts
// its routes. One-liner so router.Init stays under the funlen ceiling.
func registerCreditsRoutes(cfg *config.Config, se *core.ServeEvent) {
	if svc := registerCredits(cfg); svc != nil {
		svc.RegisterRoutes(se)
	}
}
