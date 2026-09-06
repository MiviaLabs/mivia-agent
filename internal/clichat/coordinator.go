package clichat

import (
	"context"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/agentmsg"
	"github.com/MiviaLabs/mivia-agent/internal/coordinator"
)

// chatCoordinator is this package's consumer-side view of a coordinator: the
// messaging, question/ask, and mailbox members the post_message, run_messages,
// send_to_task, and ask-flow paths use. The full coordinator carries spawn,
// join, cancel, and recovery members this package never touches through these
// paths; they depend on the subset, not the fat interface. The real
// coordinator type satisfies it.
type chatCoordinator interface {
	ConsumeMessageQuota(runID, taskID string, max int) error
	RefundMessageQuota(runID, taskID string)
	PostTaskMessage(ctx context.Context, runID, taskID string, msg agentmsg.Message) error
	ParkQuestion(runID, taskID, messageID string, maxWait ...time.Duration) (answerCh <-chan string, unpark func(), err error)
	ParkedQuestions(runID string) []coordinator.ParkedQuestion
	DeliverAnswer(runID, taskID, inReplyTo, body string) bool
	TransitionToAwaitingInput(ctx context.Context, runID, taskID string) error
	TransitionFromAwaitingInput(ctx context.Context, runID, taskID, newStatus string) error
	ListRunMessages(ctx context.Context, runID, taskID string) ([]coordinator.MessageSummary, error)
	LoadMessageBody(ctx context.Context, contentRef string) (agentmsg.Message, error)
	SendToTask(ctx context.Context, h *coordinator.RunHandle, taskID string, msg agentmsg.Message) (delivered bool, err error)
	MailboxSend(h *coordinator.RunHandle, taskID string, msg agentmsg.Message) (delivered bool, err error)
	HandleForRun(runID string) *coordinator.RunHandle
	FindLiveTaskByRole(ctx context.Context, runID, role string) (taskID string, ok bool, err error)
	SpawnReferralFromAsk(ctx context.Context, runID, toRole string, ask agentmsg.Message, meta ...coordinator.ReferralSpawnMeta) (taskID string, err error)
	TryRegisterAsk(runID, askerTaskID, askerRole, askID string, ancestors []string, maxAsks int) bool
	AsksUsedByTask(runID, taskID string) int
	ReferralSpawnsUsed(runID string) int
	TryIncReferralSpawn(runID string, max int) bool
	DecReferralSpawn(runID string)
	AskLookup(askID string) (askerTaskID string, ok bool)
	AskChainInfo(parentAskID, toRole string) (depth int, cycle bool, ancestors []string)
	IsAskAnswered(askID string) bool
	CloseAsk(askID string)
	ClaimAskAnswer(askID string) (askerTaskID string, err error)
	UnclaimAskAnswer(askID, askerTaskID string)
	SealAskAnswer(askID string) bool
}
