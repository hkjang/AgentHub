package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/hkjang/AgentHub/internal/runtimetype"
)

// ErrRuntimeTypeDisabled separates an administrator's deliberate choice from a
// malformed Agent definition. API callers can therefore receive a stable error
// code and a sentence that tells them where the choice is controlled.
var ErrRuntimeTypeDisabled = errors.New("runtime type disabled")

type RuntimeTypeDisabled struct {
	RuntimeType string
}

func (e RuntimeTypeDisabled) Error() string {
	return fmt.Sprintf("%s 런타임은 관리자가 신규 사용을 중지했습니다. 관리자 ▸ 시스템 설정 ▸ Runtime Agents에서 다시 사용할 수 있습니다.", runtimetype.Describe(e.RuntimeType).Label)
}

func (e RuntimeTypeDisabled) Is(target error) bool { return target == ErrRuntimeTypeDisabled }

// RuntimeAgentSettings reads the site-wide runtime catalogue policy. No row is
// the upgrade path from older versions and intentionally means everything is
// enabled.
func (s *Store) RuntimeAgentSettings(ctx context.Context) (runtimetype.Settings, error) {
	var settings runtimetype.Settings
	if err := s.Setting(ctx, runtimetype.SettingKey, &settings); err != nil {
		if errors.Is(err, ErrNotFound) {
			return settings, nil
		}
		return settings, err
	}
	if err := settings.Validate(); err != nil {
		return settings, fmt.Errorf("invalid runtime Agent settings: %w", err)
	}
	return settings, nil
}

// RuntimeTypeEnabled is the creation-time guard used by every creation path,
// including the console, REST API and GitOps import.
func (s *Store) RuntimeTypeEnabled(ctx context.Context, runtimeType string) (bool, error) {
	settings, err := s.RuntimeAgentSettings(ctx)
	if err != nil {
		return false, err
	}
	return settings.Enabled(runtimeType), nil
}
