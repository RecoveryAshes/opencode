package pluginruntime

import (
	"context"

	"github.com/RecoveryAshes/opencode/internal/config"
)

// ApplyConfigHook runs plugin config hooks against a cloned configuration and
// returns the mutated result.
func (runtime *Runtime) ApplyConfigHook(ctx context.Context) (config.Info, error) {
	if runtime == nil || runtime.Info == nil {
		return config.Info{}, nil
	}
	if !runtime.Enabled() {
		return cloneInfo(runtime.Info), nil
	}
	mutated := cloneInfo(runtime.Info)
	output, err := runtime.Trigger(ctx, "config.apply", nil, map[string]any{
		"config": map[string]any(mutated),
	})
	if err != nil {
		return nil, err
	}
	if cfg, ok := output["config"].(map[string]any); ok {
		return config.Info(cfg), nil
	}
	return mutated, nil
}

func cloneInfo(info config.Info) config.Info {
	return config.Info(cloneAnyMap(map[string]any(info)))
}
