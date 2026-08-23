// Package slackbot connects Slack mentions to Codex planning and work turns.
package slackbot

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/n-seiji/ebiii/internal/codex"
	"github.com/n-seiji/ebiii/internal/memory"
	"github.com/n-seiji/ebiii/internal/playbook"
	"github.com/n-seiji/ebiii/internal/prompt"
	"github.com/n-seiji/ebiii/internal/state"
	"github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
	"github.com/slack-go/slack/socketmode"
)

const (
	maxSlackMessageRunes = 3900
	claimFailureMessage  = "受付に失敗しました。お手数ですが、もう一度 mention してください。"
	failClosedMessage    = "作業指示を確定できなかったため、作業は開始していません。指示を明確にして、もう一度 mention してください。"
	planFailureMessage   = "⚠️ 方針の検討または投稿に失敗しました。もう一度 mention してください。"
	workFailureMessage   = "⚠️ 作業が完了したことを確認できませんでした。状況を確認し、新しい mention で依頼し直してください。"
	planningStatus       = "が方針を考えています…"
	workingStatus        = "が作業を進めています…"
	statusRetryDelay     = time.Second
	statusRefreshDelay   = 80 * time.Second
)

// SlackAPI is the subset of Slack Web API used by Bot.
type SlackAPI interface {
	PostMessage(ctx context.Context, channel, threadTS, text string) (string, error)
	SetStatus(ctx context.Context, channel, threadTS, status string) error
	AddReaction(ctx context.Context, channel, timestamp, name string) error
	RemoveReaction(ctx context.Context, channel, timestamp, name string) error
}

// Store is the persistent state used by Bot.
type Store interface {
	ClaimEvent(eventKey string) (bool, error)
	Transition(eventKey string, from, to state.State) error
	GetThread(threadKey string) (string, bool)
	SetThread(threadKey, threadID string) error
}

// Runner executes one Codex turn.
type Runner interface {
	Run(ctx context.Context, threadID, sandbox, cwd string, writableRoots []string, text string, onThreadStarted func(string) error) (*codex.TurnResult, error)
}

// Config contains paths, allowlists, and timeout settings needed by Bot.
type Config struct {
	AllowedUserIDs    []string
	AllowedChannelIDs []string
	EBIIIHome         string
	WorkspaceDir      string
	MemoryDir         string
	CodexTimeout      time.Duration
	BotUserID         string
	// WritableRoots are extra directories the work turn may write to.
	WritableRoots []string
}

// Bot handles Slack mentions.
type Bot struct {
	api       SlackAPI
	store     Store
	runner    Runner
	config    Config
	playbooks []playbook.Playbook

	allowedUsers    map[string]struct{}
	allowedChannels map[string]struct{}
	workMu          sync.Mutex
	memoryMu        sync.RWMutex
	threadMu        sync.Mutex
	threadLocks     map[string]*sync.Mutex
	sleep           func(context.Context, time.Duration) error
}

// New constructs a Bot.
func New(api SlackAPI, store Store, runner Runner, config Config, playbooks []playbook.Playbook) *Bot {
	config.WritableRoots = append([]string(nil), config.WritableRoots...)
	b := &Bot{
		api:             api,
		store:           store,
		runner:          runner,
		config:          config,
		playbooks:       append([]playbook.Playbook(nil), playbooks...),
		allowedUsers:    makeSet(config.AllowedUserIDs),
		allowedChannels: makeSet(config.AllowedChannelIDs),
		threadLocks:     make(map[string]*sync.Mutex),
		sleep:           sleepContext,
	}
	return b
}

// HandleMention filters and processes one already-acknowledged app mention.
func (b *Bot) HandleMention(ctx context.Context, event *slackevents.AppMentionEvent) {
	if event == nil || event.BotID != "" || event.Edited != nil || event.User == b.config.BotUserID {
		return
	}
	message := stripBotMention(event.Text, b.config.BotUserID)
	if message == "" {
		return
	}
	if _, ok := b.allowedUsers[event.User]; !ok {
		log.Printf("slackbot: rejecting user %q", event.User)
		return
	}
	if len(b.allowedChannels) > 0 {
		if _, ok := b.allowedChannels[event.Channel]; !ok {
			log.Printf("slackbot: rejecting channel %q", event.Channel)
			return
		}
	}

	eventKey := event.Channel + ":" + event.TimeStamp
	threadTS := event.ThreadTimeStamp
	if threadTS == "" {
		threadTS = event.TimeStamp
	}
	threadKey := event.Channel + ":" + threadTS

	claimed, err := b.store.ClaimEvent(eventKey)
	if err != nil {
		log.Printf("slackbot: claim %q: %v", eventKey, err)
		if postErr := b.post(ctx, event.Channel, threadTS, claimFailureMessage); postErr != nil {
			log.Printf("slackbot: post claim failure: %v", postErr)
		}
		return
	}
	if !claimed {
		return
	}
	b.addReaction(ctx, event.Channel, event.TimeStamp, "eyes")

	if err := b.store.Transition(eventKey, state.Received, state.Planning); err != nil {
		log.Printf("slackbot: start planning %q: %v", eventKey, err)
		if postErr := b.post(ctx, event.Channel, threadTS, planFailureMessage); postErr != nil {
			log.Printf("slackbot: post planning transition failure %q: %v", eventKey, postErr)
		}
		b.finalReaction(ctx, event.Channel, event.TimeStamp, false)
		return
	}
	b.setStatus(ctx, event.Channel, threadTS, planningStatus)
	defer b.clearStatus(ctx, event.Channel, threadTS)

	lock := b.threadLock(threadKey)
	lock.Lock()
	threadID, _ := b.store.GetThread(threadKey)
	b.memoryMu.RLock()
	memoryContent, memErr := memory.Read(b.config.MemoryDir)
	b.memoryMu.RUnlock()
	if memErr != nil {
		log.Printf("slackbot: read memory: %v", memErr)
	}
	planPrompt := prompt.BuildPlanPrompt(memoryContent, b.playbooks, message)
	planResult, runErr := b.runTurn(ctx, threadID, "read-only", b.config.EBIIIHome, nil, planPrompt, func(id string) error {
		if err := b.store.SetThread(threadKey, id); err != nil {
			return fmt.Errorf("persist plan thread: %w", err)
		}
		return nil
	})
	lock.Unlock()

	if runErr != nil || planResult == nil || !planResult.Completed {
		if runErr != nil {
			log.Printf("slackbot: plan turn %q: %v", eventKey, runErr)
		} else if planResult != nil {
			log.Printf("slackbot: plan turn %q incomplete: %s", eventKey, planResult.Err)
		}
		b.fail(ctx, eventKey, state.Planning, state.Failed, event.Channel, threadTS, event.TimeStamp, planFailureMessage)
		return
	}
	if len(planResult.Messages) == 0 {
		b.finishFailClosed(ctx, eventKey, event.Channel, threadTS, event.TimeStamp, failClosedMessage)
		return
	}

	planText := planResult.Messages[len(planResult.Messages)-1]
	policy, instruction, err := codex.ParsePlan(planText)
	if err != nil {
		log.Printf("slackbot: parse plan %q: %v", eventKey, err)
		b.finishFailClosed(ctx, eventKey, event.Channel, threadTS, event.TimeStamp, planText+"\n\n"+failClosedMessage)
		return
	}
	if err := b.post(ctx, event.Channel, threadTS, policy); err != nil {
		log.Printf("slackbot: post policy %q: %v", eventKey, err)
		b.fail(ctx, eventKey, state.Planning, state.Failed, event.Channel, threadTS, event.TimeStamp, planFailureMessage)
		return
	}
	if err := b.store.Transition(eventKey, state.Planning, state.PlanPosted); err != nil {
		log.Printf("slackbot: persist posted plan %q: %v", eventKey, err)
		b.fail(ctx, eventKey, state.Planning, state.Failed, event.Channel, threadTS, event.TimeStamp, planFailureMessage)
		return
	}
	if instruction == "" {
		if err := b.store.Transition(eventKey, state.PlanPosted, state.Done); err != nil {
			log.Printf("slackbot: finish NONE %q: %v", eventKey, err)
			b.fail(ctx, eventKey, state.PlanPosted, state.Failed, event.Channel, threadTS, event.TimeStamp, planFailureMessage)
			return
		}
		b.finalReaction(ctx, event.Channel, event.TimeStamp, true)
		return
	}
	if err := b.store.Transition(eventKey, state.PlanPosted, state.Working); err != nil {
		log.Printf("slackbot: start work %q: %v", eventKey, err)
		b.fail(ctx, eventKey, state.PlanPosted, state.Failed, event.Channel, threadTS, event.TimeStamp, planFailureMessage)
		return
	}
	// Posting the plan clears Slack's status automatically. Keep reapplying the
	// work status so a delayed auto-clear cannot erase it and Slack's two-minute
	// status timeout cannot expire during a long-running work turn.
	stopWorkingStatus := b.keepStatus(ctx, event.Channel, threadTS, workingStatus)
	defer stopWorkingStatus()

	// workMu serializes work turns and the memory append that follows them.
	// memoryMu is only held around the memory access itself, so a plan turn can
	// read memory while a work turn runs; when both are needed the order is
	// always workMu then memoryMu. The memory directory is intentionally not a
	// writable root: the agent proposes memory entries through the output
	// contract and the bot writes them.
	b.workMu.Lock()
	workPrompt := prompt.BuildWorkPrompt(instruction)
	workResult, workErr := b.runTurn(ctx, "", "workspace-write", b.config.WorkspaceDir, b.config.WritableRoots, workPrompt, nil)
	var resultText, memoryEntry string
	if workErr == nil && workResult != nil && workResult.Completed && len(workResult.Messages) > 0 {
		resultText, memoryEntry = codex.SplitMemoryAppend(workResult.Messages[len(workResult.Messages)-1])
		if memoryEntry != "" {
			b.memoryMu.Lock()
			written, err := memory.Append(b.config.MemoryDir, memoryEntry)
			b.memoryMu.Unlock()
			if err != nil {
				log.Printf("slackbot: append memory %q: %v", eventKey, err)
				memoryEntry = ""
			} else {
				memoryEntry = written
			}
		}
	}
	b.workMu.Unlock()

	if workErr != nil || workResult == nil || !workResult.Completed || len(workResult.Messages) == 0 {
		if workErr != nil {
			log.Printf("slackbot: work turn %q: %v", eventKey, workErr)
		}
		b.fail(ctx, eventKey, state.Working, state.Interrupted, event.Channel, threadTS, event.TimeStamp, workFailureMessage)
		return
	}
	if resultText == "" {
		resultText = "作業が完了しました。"
	}
	if memoryEntry != "" {
		resultText += "\n\n📝 メモリに追記しました:\n" + memoryEntry
	}
	if err := b.post(ctx, event.Channel, threadTS, resultText); err != nil {
		log.Printf("slackbot: post work result %q: %v", eventKey, err)
		b.fail(ctx, eventKey, state.Working, state.Interrupted, event.Channel, threadTS, event.TimeStamp, workFailureMessage)
		return
	}
	if err := b.store.Transition(eventKey, state.Working, state.Done); err != nil {
		log.Printf("slackbot: finish work %q: %v", eventKey, err)
		b.fail(ctx, eventKey, state.Working, state.Interrupted, event.Channel, threadTS, event.TimeStamp, workFailureMessage)
		return
	}
	b.finalReaction(ctx, event.Channel, event.TimeStamp, true)
}

func (b *Bot) runTurn(ctx context.Context, threadID, sandbox, cwd string, roots []string, text string, callback func(string) error) (*codex.TurnResult, error) {
	turnCtx, cancel := context.WithTimeout(ctx, b.config.CodexTimeout)
	defer cancel()
	return b.runner.Run(turnCtx, threadID, sandbox, cwd, roots, text, callback)
}

func (b *Bot) finishFailClosed(ctx context.Context, eventKey, channel, threadTS, timestamp, text string) {
	if err := b.post(ctx, channel, threadTS, text); err != nil {
		log.Printf("slackbot: post fail-closed plan %q: %v", eventKey, err)
		b.fail(ctx, eventKey, state.Planning, state.Failed, channel, threadTS, timestamp, planFailureMessage)
		return
	}
	if err := b.store.Transition(eventKey, state.Planning, state.Done); err != nil {
		log.Printf("slackbot: finish fail-closed %q: %v", eventKey, err)
		b.fail(ctx, eventKey, state.Planning, state.Failed, channel, threadTS, timestamp, planFailureMessage)
		return
	}
	b.finalReaction(ctx, channel, timestamp, true)
}

func (b *Bot) fail(ctx context.Context, eventKey string, from, to state.State, channel, threadTS, timestamp, message string) {
	if err := b.store.Transition(eventKey, from, to); err != nil {
		log.Printf("slackbot: transition %q to %s: %v", eventKey, to, err)
	}
	if err := b.post(ctx, channel, threadTS, message); err != nil {
		log.Printf("slackbot: post failure %q: %v", eventKey, err)
	}
	b.finalReaction(ctx, channel, timestamp, false)
}

// All done outcomes, including fail-closed and NONE, receive ✅. Only failed
// and interrupted outcomes receive ❌.
func (b *Bot) finalReaction(ctx context.Context, channel, timestamp string, success bool) {
	name := "x"
	if success {
		name = "white_check_mark"
	}
	// Add the terminal reaction first so a transient API failure cannot leave
	// the message with no status reaction.
	b.addReaction(ctx, channel, timestamp, name)
	if err := b.api.RemoveReaction(ctx, channel, timestamp, "eyes"); err != nil {
		log.Printf("slackbot: remove eyes reaction: %v", err)
	}
}

func (b *Bot) addReaction(ctx context.Context, channel, timestamp, name string) {
	if err := b.api.AddReaction(ctx, channel, timestamp, name); err != nil {
		log.Printf("slackbot: add %s reaction: %v", name, err)
	}
}

func (b *Bot) setStatus(ctx context.Context, channel, threadTS, status string) {
	if err := b.api.SetStatus(ctx, channel, threadTS, status); err != nil {
		log.Printf("slackbot: set thread status %q: %v", status, err)
	}
}

func (b *Bot) keepStatus(ctx context.Context, channel, threadTS, status string) func() {
	statusCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	b.setStatus(ctx, channel, threadTS, status)
	go func() {
		defer close(done)
		// Slack clears the previous status when it processes the plan reply.
		// Reapply once shortly afterward to recover from that asynchronous clear.
		if err := b.sleep(statusCtx, statusRetryDelay); err != nil {
			return
		}
		b.setStatus(statusCtx, channel, threadTS, status)
		for {
			if err := b.sleep(statusCtx, statusRefreshDelay); err != nil {
				return
			}
			b.setStatus(statusCtx, channel, threadTS, status)
		}
	}()

	var stopOnce sync.Once
	return func() {
		stopOnce.Do(func() {
			cancel()
			<-done
		})
	}
}

func (b *Bot) clearStatus(ctx context.Context, channel, threadTS string) {
	// Shutdown cancellation should not leave a stale loading indicator behind.
	clearCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	b.setStatus(clearCtx, channel, threadTS, "")
}

func (b *Bot) post(ctx context.Context, channel, threadTS, text string) error {
	for _, chunk := range splitMessage(text, maxSlackMessageRunes) {
		if _, err := b.api.PostMessage(ctx, channel, threadTS, chunk); err != nil {
			if !retryable(err) {
				return fmt.Errorf("post Slack message: %w", err)
			}
			if rateLimited, ok := errors.AsType[*slack.RateLimitedError](err); ok {
				if err := b.sleep(ctx, rateLimited.RetryAfter); err != nil {
					return fmt.Errorf("wait for Slack retry: %w", err)
				}
			}
			if _, retryErr := b.api.PostMessage(ctx, channel, threadTS, chunk); retryErr != nil {
				return fmt.Errorf("retry Slack message: %w", retryErr)
			}
		}
	}
	return nil
}

func (b *Bot) threadLock(key string) *sync.Mutex {
	b.threadMu.Lock()
	defer b.threadMu.Unlock()
	lock := b.threadLocks[key]
	if lock == nil {
		lock = &sync.Mutex{}
		b.threadLocks[key] = lock
	}
	return lock
}

func splitMessage(text string, limit int) []string {
	runes := []rune(text)
	if len(runes) == 0 {
		return []string{""}
	}
	chunks := make([]string, 0, (len(runes)+limit-1)/limit)
	for len(runes) > 0 {
		n := min(len(runes), limit)
		chunks = append(chunks, string(runes[:n]))
		runes = runes[n:]
	}
	return chunks
}

func retryable(err error) bool {
	if _, ok := errors.AsType[*slack.RateLimitedError](err); ok {
		return true
	}
	var status interface{ HTTPStatusCode() int }
	return errors.As(err, &status) && (status.HTTPStatusCode() == 429 || status.HTTPStatusCode() >= 500)
}

func stripBotMention(text, botUserID string) string {
	if botUserID != "" {
		text = strings.ReplaceAll(text, "<@"+botUserID+">", "")
	}
	return strings.TrimSpace(text)
}

func makeSet(items []string) map[string]struct{} {
	set := make(map[string]struct{}, len(items))
	for _, item := range items {
		set[item] = struct{}{}
	}
	return set
}

func sleepContext(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type webAPI struct {
	client *slack.Client
}

func (w *webAPI) PostMessage(ctx context.Context, channel, threadTS, text string) (string, error) {
	_, timestamp, err := w.client.PostMessageContext(ctx, channel, slack.MsgOptionText(text, false), slack.MsgOptionTS(threadTS))
	return timestamp, err
}

func (w *webAPI) SetStatus(ctx context.Context, channel, threadTS, status string) error {
	return w.client.SetAssistantThreadsStatusContext(ctx, slack.AssistantThreadsSetStatusParameters{
		ChannelID: channel,
		ThreadTS:  threadTS,
		Status:    status,
	})
}

func (w *webAPI) AddReaction(ctx context.Context, channel, timestamp, name string) error {
	return w.client.AddReactionContext(ctx, name, slack.ItemRef{Channel: channel, Timestamp: timestamp})
}

func (w *webAPI) RemoveReaction(ctx context.Context, channel, timestamp, name string) error {
	return w.client.RemoveReactionContext(ctx, name, slack.ItemRef{Channel: channel, Timestamp: timestamp})
}

// RunSocketMode connects a Bot to Slack Socket Mode. It acknowledges every
// envelope before dispatching app mentions to separate goroutines.
func RunSocketMode(acceptCtx, turnCtx context.Context, botToken, appToken string, bot *Bot, wg *sync.WaitGroup) error {
	client := slack.New(botToken, slack.OptionAppLevelToken(appToken))
	auth, err := client.AuthTestContext(acceptCtx)
	if err != nil {
		return fmt.Errorf("authenticate Slack bot: %w", err)
	}
	bot.api = &webAPI{client: client}
	bot.config.BotUserID = auth.UserID
	socketClient := socketmode.New(client)
	runErr := make(chan error, 1)
	go func() {
		runErr <- socketClient.RunContext(acceptCtx)
	}()

	for {
		select {
		case <-acceptCtx.Done():
			return nil
		case err := <-runErr:
			if acceptCtx.Err() != nil {
				return nil
			}
			return fmt.Errorf("run Slack Socket Mode: %w", err)
		case event, ok := <-socketClient.Events:
			if !ok {
				return errors.New("Slack Socket Mode event channel closed")
			}
			if event.Request != nil {
				socketClient.Ack(*event.Request)
			}
			if event.Type != socketmode.EventTypeEventsAPI {
				continue
			}
			apiEvent, ok := event.Data.(slackevents.EventsAPIEvent)
			if !ok {
				continue
			}
			mention, ok := apiEvent.InnerEvent.Data.(*slackevents.AppMentionEvent)
			if !ok {
				continue
			}
			wg.Go(func() {
				bot.HandleMention(turnCtx, mention)
			})
		}
	}
}
