package tickets

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/TicketsBot-cloud/common/permission"
	"github.com/TicketsBot-cloud/common/sentry"
	"github.com/TicketsBot-cloud/database"
	"github.com/TicketsBot-cloud/gdl/objects/channel"
	"github.com/TicketsBot-cloud/gdl/objects/channel/embed"
	"github.com/TicketsBot-cloud/gdl/objects/interaction"
	"github.com/TicketsBot-cloud/gdl/rest"
	"github.com/TicketsBot-cloud/worker/bot/command"
	cmdcontext "github.com/TicketsBot-cloud/worker/bot/command/context"
	"github.com/TicketsBot-cloud/worker/bot/command/registry"
	"github.com/TicketsBot-cloud/worker/bot/constants"
	"github.com/TicketsBot-cloud/worker/bot/customisation"
	"github.com/TicketsBot-cloud/worker/bot/dbclient"
	"github.com/TicketsBot-cloud/worker/bot/logic"
	"github.com/TicketsBot-cloud/worker/bot/redis"
	"github.com/TicketsBot-cloud/worker/bot/utils"
	"github.com/TicketsBot-cloud/worker/i18n"
)

type SwitchPanelCommand struct {
}

func (c SwitchPanelCommand) Properties() registry.Properties {
	return registry.Properties{
		Name:            "switchpanel",
		Description:     i18n.HelpSwitchPanel,
		Type:            interaction.ApplicationCommandTypeChatInput,
		PermissionLevel: permission.Support,
		Category:        command.Tickets,
		InteractionOnly: true,
		Arguments: command.Arguments(
			command.NewRequiredAutocompleteableArgument("panel", "Ticket panel to switch the ticket to", interaction.OptionTypeInteger, i18n.MessageInvalidUser, c.AutoCompleteHandler), // TODO: Fix invalid message
		),
		Timeout: constants.TimeoutOpenTicket,
	}
}

func (c SwitchPanelCommand) GetExecutor() interface{} {
	return c.Execute
}

func (SwitchPanelCommand) Execute(ctx *cmdcontext.SlashCommandContext, panelId int) {
	// Get ticket struct
	ticket, err := dbclient.Client.Tickets.GetByChannelAndGuild(ctx, ctx.ChannelId(), ctx.GuildId())
	if err != nil {
		ctx.HandleError(err)
		return
	}

	// Verify this is a ticket channel
	if ticket.UserId == 0 || ticket.ChannelId == nil {
		ctx.Reply(customisation.Red, i18n.Error, i18n.MessageNotATicketChannel)
		return
	}

	// Check ratelimit
	ratelimitCtx, cancel := context.WithTimeout(ctx, time.Second*3)
	defer cancel()

	allowed, err := redis.TakeRenameRatelimit(ratelimitCtx, ctx.ChannelId())
	if err != nil {
		ctx.HandleError(err)
		return
	}

	if !allowed {
		ctx.Reply(customisation.Red, i18n.TitleRename, i18n.MessageRenameRatelimited)
		return
	}

	// Try to move ticket to new category
	newPanel, err := dbclient.Client.Panel.GetById(ctx, panelId)
	if err != nil {
		ctx.HandleError(err)
		return
	}

	// Verify panel is from same guild
	if newPanel.PanelId == 0 || newPanel.GuildId != ctx.GuildId() {
		ctx.Reply(customisation.Red, i18n.Error, i18n.MessageSwitchPanelInvalidPanel)
		return
	}

	originalPanelId := ticket.PanelId
	var oldPanel *database.Panel
	if originalPanelId != nil {
		tmp, err := dbclient.Client.Panel.GetById(ctx, *originalPanelId)
		if err == nil && tmp.PanelId != 0 {
			oldPanel = &tmp
		}
	}

	// Get ticket claimer
	claimer, err := dbclient.Client.TicketClaims.Get(ctx, ticket.GuildId, ticket.Id)
	if err != nil {
		ctx.HandleError(err)
		return
	}

	// Generate old and new channel names
	var oldChannelName string
	if oldPanel != nil {
		oldChannelName, _ = logic.GenerateChannelName(ctx.Context, ctx.Worker(), oldPanel, ticket.GuildId, ticket.Id, ticket.UserId, utils.NilIfZero(claimer))
	}
	newChannelName, err := logic.GenerateChannelName(ctx.Context, ctx.Worker(), &newPanel, ticket.GuildId, ticket.Id, ticket.UserId, utils.NilIfZero(claimer))
	if err != nil {
		ctx.HandleError(err)
		return
	}

	// Fetch current channel name
	currentChannel, err := ctx.Worker().GetChannel(*ticket.ChannelId)
	if err != nil {
		ctx.HandleError(err)
		return
	}

	// Only update channel name if it matches the old panel's generated name (or if no old panel)
	shouldUpdateName := false
	if oldPanel == nil || currentChannel.Name == oldChannelName {
		shouldUpdateName = true
	}

	// Update panel assigned to ticket in database
	if err := dbclient.Client.Tickets.SetPanelId(ctx, ctx.GuildId(), ticket.Id, panelId); err != nil {
		ctx.HandleError(err)
		return
	}

	// Update welcome message
	if ticket.WelcomeMessageId != nil {
		msg, err := ctx.Worker().GetChannelMessage(ctx.ChannelId(), *ticket.WelcomeMessageId)

		// Error is likely to be due to message being deleted, we want to continue further even if it is
		if err == nil {
			var subject string

			embeds := utils.PtrElems(msg.Embeds) // TODO: Fix types
			if len(embeds) == 0 {
				embeds = make([]*embed.Embed, 1)
				subject = "No subject given"
			} else {
				subject = embeds[0].Title // TODO: Store subjects in database
			}

			embeds[0], err = logic.BuildWelcomeMessageEmbed(ctx.Context, ctx, ticket, subject, &newPanel, nil)
			if err != nil {
				ctx.HandleError(err)
				return
			}

			for i := 1; i < len(embeds); i++ {
				embeds[i].Color = embeds[0].Color
			}

			editData := rest.EditMessageData{
				Content:    msg.Content,
				Embeds:     embeds,
				Flags:      uint(msg.Flags),
				Components: msg.Components,
			}

			if _, err = ctx.Worker().EditMessage(ctx.ChannelId(), *ticket.WelcomeMessageId, editData); err != nil {
				ctx.HandleWarning(err)
			}
		}
	}

	// If the ticket is a thread, we cannot update the permissions (possibly remove a small amount of  members in the
	// future), or the parent channel (user may not have access to it. can you even move threads anyway?)
	if ticket.IsThread {
		settings, err := ctx.Settings()
		if err != nil {
			ctx.HandleError(err)
			return
		}

		data := rest.ModifyChannelData{}
		if shouldUpdateName {
			data.Name = newChannelName
		}

		if _, err := ctx.Worker().ModifyChannel(*ticket.ChannelId, data); err != nil {
			ctx.HandleError(err)
			return
		}

		ctx.ReplyRaw(customisation.Green, "Success", fmt.Sprintf("This ticket has been switched to the panel **%s**.\n\nNote: As this is a thread, the permissions could not be bulk updated.", newPanel.Title))

		// Modify join message
		if ticket.JoinMessageId != nil && settings.TicketNotificationChannel != nil {
			threadStaff, err := logic.GetStaffInThread(ctx.Context, ctx.Worker(), ticket, *ticket.ChannelId)
			if err != nil {
				sentry.ErrorWithContext(err, ctx.ToErrorContext()) // Only log
				return
			}

			msg := logic.BuildJoinThreadMessage(ctx.Context, ctx.Worker(), ctx.GuildId(), ticket.UserId, newChannelName, ticket.Id, &newPanel, threadStaff, ctx.PremiumTier())
			if _, err := ctx.Worker().EditMessage(*settings.TicketNotificationChannel, *ticket.JoinMessageId, msg.IntoEditMessageData()); err != nil {
				sentry.ErrorWithContext(err, ctx.ToErrorContext()) // Only log
				return
			}
		}

		return
	}

	// Append additional ticket members to overwrites
	members, err := dbclient.Client.TicketMembers.Get(ctx, ctx.GuildId(), ticket.Id)
	if err != nil {
		ctx.HandleError(err)
		return
	}

	// Calculate new channel permissions
	var overwrites []channel.PermissionOverwrite
	if claimer == 0 {
		overwrites, err = logic.CreateOverwrites(ctx.Context, ctx, ticket.UserId, &newPanel, members...)
		if err != nil {
			ctx.HandleError(err)
			return
		}
	} else {
		overwrites, err = logic.GenerateClaimedOverwrites(ctx.Context, ctx.Worker(), ticket, claimer)
		if err != nil {
			ctx.HandleError(err)
			return
		}

		// GenerateClaimedOverwrites returns nil if the permissions are the same as an unclaimed ticket
		// so if this is the case, we still need to calculate permissions
		if overwrites == nil {
			overwrites, err = logic.CreateOverwrites(ctx.Context, ctx, ticket.UserId, &newPanel, members...)
		}
	}

	// Update channel permissions
	data := rest.ModifyChannelData{
		PermissionOverwrites: overwrites,
		ParentId:             newPanel.TargetCategory,
		Topic:                newPanel.Title,
	}
	if shouldUpdateName {
		data.Name = newChannelName
	}

	if _, err = ctx.Worker().ModifyChannel(*ticket.ChannelId, data); err != nil {
		ctx.HandleError(err)
		return
	}

	ctx.ReplyPermanent(customisation.Green, i18n.TitlePanelSwitched, i18n.MessageSwitchPanelSuccess, newPanel.Title, ctx.UserId())
}

func (SwitchPanelCommand) AutoCompleteHandler(data interaction.ApplicationCommandAutoCompleteInteraction, value string) []interaction.ApplicationCommandOptionChoice {
	if data.GuildId.Value == 0 {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second*3) // TODO: Propagate context
	defer cancel()

	panels, err := dbclient.Client.Panel.GetByGuild(ctx, data.GuildId.Value)
	if err != nil {
		sentry.Error(err) // TODO: Context
		return nil
	}

	if value == "" {
		if len(panels) > 25 {
			return panelsToChoices(panels[:25])
		} else {
			return panelsToChoices(panels)
		}
	} else {
		var filtered []database.Panel
		for _, panel := range panels {
			if strings.Contains(strings.ToLower(panel.Title), strings.ToLower(value)) {
				filtered = append(filtered, panel)
			}

			if len(filtered) == 25 {
				break
			}
		}

		return panelsToChoices(filtered)
	}
}

func panelsToChoices(panels []database.Panel) []interaction.ApplicationCommandOptionChoice {
	choices := make([]interaction.ApplicationCommandOptionChoice, len(panels))
	for i, panel := range panels {
		choices[i] = interaction.ApplicationCommandOptionChoice{
			Name:  panel.Title,
			Value: panel.PanelId,
		}
	}

	return choices
}
