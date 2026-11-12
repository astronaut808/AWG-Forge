package nodeagent

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/astronaut808/awg-forge/internal/app"
	"github.com/astronaut808/awg-forge/internal/config"
	"github.com/astronaut808/awg-forge/internal/controlapi"
)

func runClientConfigExport(ctx context.Context, service *app.Service, client *http.Client, connection config.NodeConnectionState, poll controlapi.OperationPoll) error {
	poll.WaitSeconds = 0
	var delivery controlapi.ClientConfigExportDelivery
	err := requestJSON(ctx, client, connection.ControllerURL, http.MethodPost, "/control/v1/node/client-config-exports/poll", "", poll, &delivery)
	if errors.Is(err, errNoOperation) {
		return nil
	}
	if err != nil {
		return err
	}
	if delivery.Validate() != nil || delivery.Authority != poll || !time.Now().Before(delivery.ExpiresAt) {
		return errors.New("invalid export delivery")
	}
	value, renderErr := service.RenderNodeClientConfig(ctx, connection, delivery)
	defer clear(value)
	result := controlapi.ClientConfigExportResult{ClientConfigExportLocator: delivery.ClientConfigExportLocator, Authority: poll, Status: "failed"}
	if renderErr == nil {
		result.Status = "ready"
		result.Config = string(value)
	}
	defer func() { result.Config = "" }()
	return requestJSON(ctx, client, connection.ControllerURL, http.MethodPut, "/control/v1/node/client-config-exports/result", "", result, nil)
}
