package button

import (
	"github.com/TicketsBot-cloud/gdl/objects/interaction"
	"github.com/TicketsBot-cloud/worker"
)

type ResponseAck struct{}

func (r ResponseAck) Type() ResponseType {
	return ResponseTypeAck
}

func (r ResponseAck) Build() interface{} {
	return interaction.NewResponseDeferredMessageUpdate()
}

func (r ResponseAck) HandleDeferred(interactionData interaction.InteractionMetadata, worker *worker.Context) error {
	return nil
}
