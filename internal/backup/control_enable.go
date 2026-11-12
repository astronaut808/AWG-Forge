package backup

import (
	"context"

	"github.com/astronaut808/awg-forge/internal/app"
	"github.com/astronaut808/awg-forge/internal/config"
)

// PrepareControlEnable creates the encrypted recovery backup required before
// the caller enables the controller's control server.
func PrepareControlEnable(ctx context.Context, cfg config.Config, service *app.Service, token, password string) (Archive, *app.ControlEnableReceipt, error) {
	var archive Archive
	receipt, err := service.PrepareControlEnable(ctx, token, func(ctx context.Context, state config.State) error {
		var err error
		archive, err = createFromState(ctx, cfg, state, password, Options{})
		return err
	})
	if err != nil {
		return Archive{}, nil, err
	}
	return archive, receipt, nil
}
