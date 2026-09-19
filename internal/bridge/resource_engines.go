package bridge

import (
	"fmt"
	"strings"

	"github.com/IamAngusU/ContextBridge/internal/config"
	"github.com/IamAngusU/ContextBridge/internal/resourcepacks"
)

func resourcePackSettings(cfg config.Config) resourcepacks.Settings {
	enabled := cfg.Portable.Enabled != nil && *cfg.Portable.Enabled
	return resourcepacks.Settings{Enabled: enabled, ScanRoots: append([]string(nil), cfg.Portable.ScanRoots...), MaxPacks: cfg.Portable.MaxPacks}
}

func discoverResourcePacks(cfg config.Config) []resourcepacks.Pack {
	packs, _ := resourcepacks.Discover(resourcePackSettings(cfg))
	return packs
}

func resolveResourceEngine(engine config.Engine, packs []resourcepacks.Pack) (config.Engine, error) {
	if strings.TrimSpace(engine.ResourcePack) == "" {
		return engine, nil
	}
	endpoint, ok := resourcepacks.Resolve(packs, engine.ResourcePack, engine.Endpoint, engine.Type)
	if !ok {
		return engine, fmt.Errorf("portable resource %s endpoint %s is not present", engine.ResourcePack, engine.Endpoint)
	}
	engine.URL = endpoint.URL
	if engine.Model == "" {
		engine.Model = endpoint.Model
	}
	if len(engine.Capabilities) == 0 {
		engine.Capabilities = append([]string(nil), endpoint.Capabilities...)
	}
	return engine, nil
}
