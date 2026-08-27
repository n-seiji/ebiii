package slackbot

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/n-seiji/ebiii/internal/codex"
	"github.com/n-seiji/ebiii/internal/memory"
	"github.com/n-seiji/ebiii/internal/state"
	"github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
)

type fakeStore struct {
	mu          sync.Mutex
	claim       bool
	claimErr    error
	claimCalls  int
	current     state.State
	transitions [][2]state.State
	threadIDs   map[string]string
	threadKeys  []string
}

func (s *fakeStore) ClaimEvent(string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.claimCalls++
	if s.claimErr == nil && s.claim {
		s.current = state.Received
	}
	return s.claim, s.claimErr
}

func (s *fakeStore) Transition(_ string, from, to state.State) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.current != from {
		return errors.New("unexpected source state")
	}
	s.current = to
	s.transitions = append(s.transitions, [2]state.State{from, to})
	return nil
}

func (s *fakeStore) GetThread(key string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.threadKeys = append(s.threadKeys, key)
	threadID, ok := s.threadIDs[key]
	return threadID, ok
}

func (s *fakeStore) SetThread(key, threadID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.threadIDs == nil {
		s.threadIDs = make(map[string]string)
	}
	s.threadIDs[key] = threadID
	return nil
}

type slackCall struct {
	kind string
	text string
}

type fakeSlack struct {
	mu             sync.Mutex
	calls          []slackCall
	postErrs       []error
	postTexts      []string
	threadMessages []ThreadMessage
	threadErr      error
	threadCalls    int
	threadLatest   string
}

func (s *fakeSlack) PostMessage(_ context.Context, _, _, text string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, slackCall{kind: "post", text: text})
	s.postTexts = append(s.postTexts, text)
	var err error
	if len(s.postErrs) > 0 {
		err = s.postErrs[0]
		s.postErrs = s.postErrs[1:]
	}
	return "reply-ts", err
}

func (s *fakeSlack) GetThreadMessages(_ context.Context, _, _, latest string) ([]ThreadMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.threadCalls++
	s.threadLatest = latest
	return append([]ThreadMessage(nil), s.threadMessages...), s.threadErr
}

func (s *fakeSlack) SetStatus(_ context.Context, _, _, status string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, slackCall{kind: "status", text: status})
	return nil
}

func (s *fakeSlack) AddReaction(_ context.Context, _, _, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, slackCall{kind: "add:" + name})
	return nil
}

func (s *fakeSlack) RemoveReaction(_ context.Context, _, _, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, slackCall{kind: "remove:" + name})
	return nil
}

type runnerResponse struct {
	result *codex.TurnResult
	err    error
}

type fakeRunner struct {
	mu        sync.Mutex
	responses []runnerResponse
	calls     int
	threadIDs []string
	sandboxes []string
	cwds      []string
	roots     [][]string
	prompts   []string
	onRun     func(call int)
}

func (r *fakeRunner) Run(_ context.Context, threadID, sandbox, cwd string, roots []string, prompt string, callback func(string) error) (*codex.TurnResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	if r.onRun != nil {
		r.onRun(r.calls)
	}
	r.threadIDs = append(r.threadIDs, threadID)
	r.sandboxes = append(r.sandboxes, sandbox)
	r.cwds = append(r.cwds, cwd)
	r.roots = append(r.roots, roots)
	r.prompts = append(r.prompts, prompt)
	if callback != nil {
		if err := callback("plan-thread"); err != nil {
			return nil, err
		}
	}
	response := r.responses[0]
	r.responses = r.responses[1:]
	return response.result, response.err
}

func newTestBot(t *testing.T, store *fakeStore, api *fakeSlack, runner *fakeRunner) *Bot {
	t.Helper()
	return New(api, store, runner, Config{
		AllowedUserIDs: []string{"U1"},
		WorkspaceDir:   "/repo/workspace",
		MemoryDir:      filepath.Join(t.TempDir(), "memory"),
		CodexTimeout:   time.Minute,
		BotUserID:      "UBOT",
	}, nil)
}

func mention() *slackevents.AppMentionEvent {
	return &slackevents.AppMentionEvent{
		User:      "U1",
		Channel:   "C1",
		TimeStamp: "100.1",
		Text:      "<@UBOT> do it",
	}
}

func TestAllowlistRejectsUserAndChannel(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*Bot)
		event     *slackevents.AppMentionEvent
	}{
		{
			name: "user",
			event: &slackevents.AppMentionEvent{
				User: "U2", Channel: "C1", TimeStamp: "1", Text: "<@UBOT> work",
			},
		},
		{
			name: "channel",
			configure: func(bot *Bot) {
				bot.allowedChannels = makeSet([]string{"C2"})
			},
			event: mention(),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &fakeStore{claim: true}
			api := &fakeSlack{}
			bot := newTestBot(t, store, api, &fakeRunner{})
			if test.configure != nil {
				test.configure(bot)
			}
			bot.HandleMention(context.Background(), test.event)
			if store.claimCalls != 0 {
				t.Fatalf("ClaimEvent called %d times, want 0", store.claimCalls)
			}
			if got := strings.Join(api.postTexts, "|"); got != "403 forbidden. @seiji に確認してください。" {
				t.Fatalf("posts = %q, want forbidden response", got)
			}
		})
	}
}

func TestAllowedWorkflowMentionIsHandled(t *testing.T) {
	store := &fakeStore{claim: true}
	api := &fakeSlack{}
	runner := &fakeRunner{responses: []runnerResponse{{result: &codex.TurnResult{
		Completed: true,
		Messages:  []string{"## 方針\nDone.\n## 作業指示\nNONE"},
	}}}}
	bot := newTestBot(t, store, api, runner)
	bot.config.AllowWorkflows = true
	event := mention()
	event.User = "UWORKFLOW"
	event.BotID = "BWORKFLOW"

	bot.handleMention(context.Background(), event, "Wf0BSM19MCDT")

	if runner.calls != 1 {
		t.Fatalf("runner calls = %d, want 1", runner.calls)
	}
}

func TestBotMentionWithoutWorkflowIDIsForbidden(t *testing.T) {
	store := &fakeStore{claim: true}
	api := &fakeSlack{}
	bot := newTestBot(t, store, api, &fakeRunner{})
	bot.config.AllowWorkflows = true
	event := mention()
	event.User = "UOTHERBOT"
	event.BotID = "BOTHER"

	bot.handleMention(context.Background(), event, "")

	if store.claimCalls != 0 {
		t.Fatalf("ClaimEvent called %d times, want 0", store.claimCalls)
	}
	if got := strings.Join(api.postTexts, "|"); got != "403 forbidden. @seiji に確認してください。" {
		t.Fatalf("posts = %q, want forbidden response", got)
	}
}

func TestUnauthorizedBareMentionIsForbidden(t *testing.T) {
	store := &fakeStore{claim: true}
	api := &fakeSlack{}
	bot := newTestBot(t, store, api, &fakeRunner{})
	event := mention()
	event.User = "UDENIED"
	event.Text = "<@UBOT>"

	bot.HandleMention(context.Background(), event)

	if got := strings.Join(api.postTexts, "|"); got != "403 forbidden. @seiji に確認してください。" {
		t.Fatalf("posts = %q, want forbidden response", got)
	}
}

func TestWorkflowAuthorizationBoundaries(t *testing.T) {
	tests := []struct {
		name       string
		enabled    bool
		workflowID string
		channel    string
	}{
		{name: "disabled", workflowID: "Wf0BSM19MCDT", channel: "C1"},
		{name: "wrong prefix", enabled: true, workflowID: "Fx0BSM19MCDT", channel: "C1"},
		{name: "invalid characters", enabled: true, workflowID: "WfBAD-id", channel: "C1"},
		{name: "disallowed channel", enabled: true, workflowID: "Wf0BSM19MCDT", channel: "C2"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &fakeStore{claim: true}
			api := &fakeSlack{}
			bot := newTestBot(t, store, api, &fakeRunner{})
			bot.config.AllowWorkflows = test.enabled
			bot.allowedChannels = makeSet([]string{"C1"})
			event := mention()
			event.User = "UWORKFLOW"
			event.BotID = "BWORKFLOW"
			event.Channel = test.channel

			bot.handleMention(context.Background(), event, test.workflowID)

			if store.claimCalls != 0 {
				t.Fatalf("ClaimEvent called %d times, want 0", store.claimCalls)
			}
			if got := strings.Join(api.postTexts, "|"); got != "403 forbidden. @seiji に確認してください。" {
				t.Fatalf("posts = %q, want forbidden response", got)
			}
		})
	}
}

func TestForbiddenResponseMentionsConfiguredAdmin(t *testing.T) {
	store := &fakeStore{claim: true}
	api := &fakeSlack{}
	bot := newTestBot(t, store, api, &fakeRunner{})
	bot.config.AdminUserID = "UADMIN"
	event := mention()
	event.User = "UDENIED"

	bot.HandleMention(context.Background(), event)

	if got := strings.Join(api.postTexts, "|"); got != "403 forbidden. <@UADMIN> に確認してください。" {
		t.Fatalf("posts = %q, want configured admin mention", got)
	}
}

func TestWorkflowIDFromPayload(t *testing.T) {
	payload := json.RawMessage(`{"event":{"type":"app_mention","workflow_id":"Wf0BSM19MCDT"}}`)
	if got := workflowIDFromPayload(payload); got != "Wf0BSM19MCDT" {
		t.Fatalf("workflowIDFromPayload() = %q, want Wf0BSM19MCDT", got)
	}
}

func TestDuplicateEventIsSkipped(t *testing.T) {
	store := &fakeStore{claim: false}
	runner := &fakeRunner{}
	api := &fakeSlack{}
	newTestBot(t, store, api, runner).HandleMention(context.Background(), mention())
	if runner.calls != 0 || len(api.calls) != 0 {
		t.Fatalf("duplicate event caused runner=%d Slack calls=%d", runner.calls, len(api.calls))
	}
}

func TestFailClosedTransitionsToDoneWithCheckmark(t *testing.T) {
	tests := []struct {
		name   string
		result *codex.TurnResult
		want   string
	}{
		{
			name:   "parse failure",
			result: &codex.TurnResult{Completed: true, Messages: []string{"unstructured plan"}},
			want:   "unstructured plan",
		},
		{
			name:   "empty messages",
			result: &codex.TurnResult{Completed: true},
			want:   failClosedMessage,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &fakeStore{claim: true}
			api := &fakeSlack{}
			runner := &fakeRunner{responses: []runnerResponse{{result: test.result}}}
			newTestBot(t, store, api, runner).HandleMention(context.Background(), mention())
			if store.current != state.Done {
				t.Fatalf("state = %q, want done", store.current)
			}
			assertTransitions(t, store.transitions, [][2]state.State{
				{state.Received, state.Planning},
				{state.Planning, state.Done},
			})
			if !strings.Contains(strings.Join(api.postTexts, ""), test.want) {
				t.Errorf("posts %q do not contain %q", api.postTexts, test.want)
			}
			assertFinalReactionOrder(t, api.calls, "white_check_mark")
		})
	}
}

func TestNoneTransitionsToDoneWithCheckmark(t *testing.T) {
	store := &fakeStore{claim: true}
	api := &fakeSlack{}
	runner := &fakeRunner{responses: []runnerResponse{{result: &codex.TurnResult{
		Completed: true,
		Messages:  []string{"## 方針\nNo work needed.\n## 作業指示\nNONE"},
	}}}}
	newTestBot(t, store, api, runner).HandleMention(context.Background(), mention())
	assertTransitions(t, store.transitions, [][2]state.State{
		{state.Received, state.Planning},
		{state.Planning, state.PlanPosted},
		{state.PlanPosted, state.Done},
	})
	assertStatusSequence(t, api.calls, []string{planningStatus, ""})
	assertFinalReactionOrder(t, api.calls, "white_check_mark")
}

func TestPlanWorkDone(t *testing.T) {
	store := &fakeStore{claim: true}
	api := &fakeSlack{}
	runner := &fakeRunner{responses: []runnerResponse{
		{result: &codex.TurnResult{
			Completed: true,
			Messages:  []string{"## 方針\nImplement safely.\n## 作業指示\nChange the file."},
		}},
		{result: &codex.TurnResult{Completed: true, Messages: []string{"Work completed."}}},
	}}
	runner.onRun = func(call int) {
		if call == 2 && len(api.postTexts) != 0 {
			t.Fatalf("posts before work started = %q, want none", api.postTexts)
		}
	}
	newTestBot(t, store, api, runner).HandleMention(context.Background(), mention())
	assertTransitions(t, store.transitions, [][2]state.State{
		{state.Received, state.Planning},
		{state.Planning, state.PlanPosted},
		{state.PlanPosted, state.Working},
		{state.Working, state.Done},
	})
	if runner.calls != 2 {
		t.Fatalf("runner calls = %d, want 2", runner.calls)
	}
	if got := strings.Join(api.postTexts, "|"); got != "Work completed." {
		t.Fatalf("posts = %q", got)
	}
	if runner.roots[0] != nil || runner.roots[1] != nil {
		t.Fatalf("writable roots = %v, want no writable roots for either turn", runner.roots)
	}
	assertStatusSequence(t, api.calls, []string{planningStatus, workingStatus, ""})
	assertFinalReactionOrder(t, api.calls, "white_check_mark")
}

func TestPlanSessionsAreSharedBySlackThread(t *testing.T) {
	store := &fakeStore{claim: true}
	runner := &fakeRunner{responses: []runnerResponse{
		{result: &codex.TurnResult{Completed: true, Messages: []string{"## 方針\nDone.\n## 作業指示\nNONE"}}},
		{result: &codex.TurnResult{Completed: true, Messages: []string{"## 方針\nDone.\n## 作業指示\nNONE"}}},
	}}
	bot := newTestBot(t, store, &fakeSlack{}, runner)
	bot.allowedUsers["U2"] = struct{}{}
	bot.HandleMention(context.Background(), mention())
	bot.HandleMention(context.Background(), &slackevents.AppMentionEvent{
		User: "U2", Channel: "C1", TimeStamp: "200.2", ThreadTimeStamp: "100.1", Text: "<@UBOT> do it",
	})

	want := []string{"v3:C1:100.1", "v3:C1:100.1"}
	if !reflect.DeepEqual(store.threadKeys, want) {
		t.Fatalf("thread keys = %v, want %v", store.threadKeys, want)
	}
	if len(store.threadIDs) != 1 {
		t.Fatalf("stored thread keys = %v, want one shared session", store.threadIDs)
	}
	if got := runner.threadIDs; !reflect.DeepEqual(got, []string{"", "plan-thread"}) {
		t.Fatalf("runner thread IDs = %v, want second user to resume shared session", got)
	}
}

func TestFirstThreadMentionReceivesEarlierSlackMessages(t *testing.T) {
	store := &fakeStore{claim: true}
	api := &fakeSlack{threadMessages: []ThreadMessage{
		{AuthorID: "U2", Timestamp: "100.1", Text: "root context"},
		{AuthorID: "U3", Timestamp: "200.2", Text: "important detail"},
		{AuthorID: "U1", Timestamp: "300.3", Text: "<@UBOT> current request"},
	}}
	runner := &fakeRunner{responses: []runnerResponse{{result: &codex.TurnResult{
		Completed: true,
		Messages:  []string{"## 方針\nDone.\n## 作業指示\nNONE"},
	}}}}
	event := mention()
	event.TimeStamp = "300.3"
	event.ThreadTimeStamp = "100.1"
	event.Text = "<@UBOT> current request"

	newTestBot(t, store, api, runner).HandleMention(context.Background(), event)

	if api.threadCalls != 1 || api.threadLatest != "300.3" {
		t.Fatalf("thread fetch calls = %d, latest = %q, want one call through current mention", api.threadCalls, api.threadLatest)
	}
	if len(runner.prompts) != 1 {
		t.Fatalf("runner prompts = %d, want 1", len(runner.prompts))
	}
	got := runner.prompts[0]
	for _, want := range []string{"<slack_thread>", "root context", "important detail", "<user_message>\ncurrent request"} {
		if !strings.Contains(got, want) {
			t.Errorf("plan prompt does not contain %q", want)
		}
	}
	if strings.Count(got, "<@UBOT> current request") != 0 {
		t.Error("current mention was duplicated into Slack thread context")
	}
}

func TestExistingPlanSessionSkipsSlackThreadFetch(t *testing.T) {
	store := &fakeStore{
		claim:     true,
		threadIDs: map[string]string{"v3:C1:100.1": "existing-thread"},
	}
	api := &fakeSlack{threadErr: errors.New("must not be called")}
	runner := &fakeRunner{responses: []runnerResponse{{result: &codex.TurnResult{
		Completed: true,
		Messages:  []string{"## 方針\nDone.\n## 作業指示\nNONE"},
	}}}}
	event := mention()
	event.TimeStamp = "200.2"
	event.ThreadTimeStamp = "100.1"

	newTestBot(t, store, api, runner).HandleMention(context.Background(), event)

	if api.threadCalls != 0 {
		t.Fatalf("thread fetch calls = %d, want 0", api.threadCalls)
	}
	if len(runner.threadIDs) != 1 || runner.threadIDs[0] != "existing-thread" {
		t.Fatalf("runner thread IDs = %v, want existing session", runner.threadIDs)
	}
	if strings.Contains(runner.prompts[0], "<slack_thread>") {
		t.Error("existing session prompt unexpectedly contains Slack thread context")
	}
}

func TestThreadFetchFailureDoesNotStartCodex(t *testing.T) {
	store := &fakeStore{claim: true}
	api := &fakeSlack{threadErr: errors.New("Slack unavailable")}
	runner := &fakeRunner{}
	event := mention()
	event.TimeStamp = "200.2"
	event.ThreadTimeStamp = "100.1"

	newTestBot(t, store, api, runner).HandleMention(context.Background(), event)

	if runner.calls != 0 {
		t.Fatalf("runner calls = %d, want 0", runner.calls)
	}
	if store.current != state.Failed {
		t.Fatalf("state = %q, want failed", store.current)
	}
	if !strings.Contains(strings.Join(api.postTexts, "|"), threadFailureMessage) {
		t.Fatalf("posts = %q, want thread failure message", api.postTexts)
	}
	assertFinalReactionOrder(t, api.calls, "x")
}

func TestFormatThreadContextCapsRunesAndKeepsEnds(t *testing.T) {
	got := formatThreadContext([]ThreadMessage{{
		AuthorID: "U1", Timestamp: "100.1", Text: strings.Repeat("前", maxThreadContextRunes) + "末尾",
	}}, "200.2")
	if len([]rune(got)) != maxThreadContextRunes {
		t.Fatalf("context runes = %d, want %d", len([]rune(got)), maxThreadContextRunes)
	}
	for _, want := range []string{"[100.1 / U1]", "中間を省略", "末尾"} {
		if !strings.Contains(got, want) {
			t.Errorf("thread context does not contain %q", want)
		}
	}
}

func TestKeepStatusRefreshesUntilStopped(t *testing.T) {
	api := &fakeSlack{}
	bot := newTestBot(t, &fakeStore{}, api, &fakeRunner{})
	sleepCalls := make(chan time.Duration)
	releaseSleep := make(chan struct{})
	bot.sleep = func(ctx context.Context, duration time.Duration) error {
		select {
		case sleepCalls <- duration:
		case <-ctx.Done():
			return ctx.Err()
		}
		select {
		case <-releaseSleep:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	stop := bot.keepStatus(context.Background(), "C1", "100.1", workingStatus)
	if got := receiveDuration(t, sleepCalls); got != statusRefreshDelay {
		t.Fatalf("first status delay = %v, want %v", got, statusRefreshDelay)
	}
	releaseSleep <- struct{}{}
	if got := receiveDuration(t, sleepCalls); got != statusRefreshDelay {
		t.Fatalf("second status delay = %v, want %v", got, statusRefreshDelay)
	}
	stop()

	assertStatusSequence(t, api.calls, []string{workingStatus, workingStatus})
}

func TestWorkTurnReceivesWritableRootsAndPlanTurnDoesNot(t *testing.T) {
	store := &fakeStore{claim: true}
	api := &fakeSlack{}
	runner := &fakeRunner{responses: []runnerResponse{
		{result: &codex.TurnResult{
			Completed: true,
			Messages:  []string{"## 方針\nDo work.\n## 作業指示\nMake a change."},
		}},
		{result: &codex.TurnResult{Completed: true, Messages: []string{"Work completed."}}},
	}}
	roots := []string{"/extra/one", "/extra/two"}
	bot := New(api, store, runner, Config{
		AllowedUserIDs: []string{"U1"},
		WorkspaceDir:   "/repo/workspace",
		MemoryDir:      filepath.Join(t.TempDir(), "memory"),
		CodexTimeout:   time.Minute,
		BotUserID:      "UBOT",
		WritableRoots:  roots,
	}, nil)
	// New must copy the slice, so later mutation by the caller is not observed.
	roots[0] = "/mutated"

	bot.HandleMention(context.Background(), mention())
	if runner.calls != 2 {
		t.Fatalf("runner calls = %d, want 2", runner.calls)
	}
	if runner.roots[0] != nil {
		t.Errorf("plan turn writable roots = %v, want nil", runner.roots[0])
	}
	if got := runner.cwds[0]; got != "/repo/workspace" {
		t.Errorf("plan turn cwd = %q, want isolated workspace", got)
	}
	if got := runner.sandboxes[0]; got != "read-only-network" {
		t.Errorf("plan turn sandbox = %q, want read-only-network", got)
	}
	want := []string{"/extra/one", "/extra/two"}
	if !reflect.DeepEqual(runner.roots[1], want) {
		t.Errorf("work turn writable roots = %v, want %v", runner.roots[1], want)
	}
	if got := runner.cwds[1]; got != "/repo/workspace" {
		t.Errorf("work turn cwd = %q, want isolated workspace", got)
	}
}

func TestPlanPromptInjectsMemoryContent(t *testing.T) {
	store := &fakeStore{claim: true}
	api := &fakeSlack{}
	runner := &fakeRunner{responses: []runnerResponse{{result: &codex.TurnResult{
		Completed: true,
		Messages:  []string{"## 方針\nNo work needed.\n## 作業指示\nNONE"},
	}}}}
	bot := newTestBot(t, store, api, runner)
	entries := []struct {
		scope memory.Scope
		text  string
	}{
		{scope: memory.ScopeGlobal, text: "全体の学び"},
		{scope: memory.ScopeChannel, text: "チャンネルの慣習"},
	}
	for _, entry := range entries {
		if _, err := memory.AppendScoped(bot.config.MemoryDir, entry.scope, "C1", entry.text); err != nil {
			t.Fatalf("append %s memory: %v", entry.scope, err)
		}
	}
	bot.HandleMention(context.Background(), mention())
	if len(runner.prompts) != 1 {
		t.Fatalf("runner prompts = %d, want 1", len(runner.prompts))
	}
	prompt := runner.prompts[0]
	for _, want := range []string{
		"<global_memory>", "全体の学び", "</global_memory>",
		"<channel_memory>", "チャンネルの慣習", "</channel_memory>",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("plan prompt does not contain %q", want)
		}
	}
	if strings.Contains(prompt, "user_memory") {
		t.Error("plan prompt must omit user memory")
	}
	if strings.Contains(prompt, "MEMORY.md") {
		t.Error("plan prompt should inject memory content, not the file path")
	}
}

func TestWorkMemoryAppendIsWrittenByBot(t *testing.T) {
	store := &fakeStore{claim: true}
	api := &fakeSlack{}
	runner := &fakeRunner{responses: []runnerResponse{
		{result: &codex.TurnResult{
			Completed: true,
			Messages:  []string{"## 方針\nDo work.\n## 作業指示\nMake a change."},
		}},
		{result: &codex.TurnResult{
			Completed: true,
			Messages: []string{strings.Join([]string{
				"Work completed.",
				"## 全体メモリ追記", "ビルドは mise run build を使う",
				"## チャンネルメモリ追記", "動作確認用チャンネル",
			}, "\n")},
		}},
	}}
	bot := newTestBot(t, store, api, runner)
	bot.HandleMention(context.Background(), mention())
	if store.current != state.Done {
		t.Fatalf("state = %q, want done", store.current)
	}
	files := []struct {
		path string
		want string
	}{
		{path: filepath.Join(bot.config.MemoryDir, "MEMORY.md"), want: "ビルドは mise run build を使う"},
		{path: filepath.Join(bot.config.MemoryDir, "channels", "C1", "MEMORY.md"), want: "動作確認用チャンネル"},
	}
	for _, file := range files {
		data, err := os.ReadFile(file.path)
		if err != nil {
			t.Fatalf("read memory %s: %v", file.path, err)
		}
		if !strings.Contains(string(data), file.want) {
			t.Fatalf("memory file %s = %q, want %q", file.path, data, file.want)
		}
	}
	posts := strings.Join(api.postTexts, "|")
	if !strings.Contains(posts, "Work completed.") || !strings.Contains(posts, "全体・チャンネルメモリを更新しました") {
		t.Fatalf("posts = %q, want work result and memory notification", posts)
	}
	for _, private := range []string{"ビルドは mise run build を使う", "動作確認用チャンネル"} {
		if strings.Contains(posts, private) {
			t.Fatalf("posts = %q, should not expose memory content %q", posts, private)
		}
	}
}

func TestUserMemoryOutputIsNotAccepted(t *testing.T) {
	store := &fakeStore{claim: true}
	api := &fakeSlack{}
	runner := &fakeRunner{responses: []runnerResponse{
		{result: &codex.TurnResult{
			Completed: true,
			Messages:  []string{"## 方針\nDo work.\n## 作業指示\nMake a change."},
		}},
		{result: &codex.TurnResult{Completed: true, Messages: []string{
			"Work completed.\n## ユーザーメモリ追記\nprivate memory",
		}}},
	}}
	bot := newTestBot(t, store, api, runner)
	bot.HandleMention(context.Background(), mention())

	posts := strings.Join(api.postTexts, "|")
	if !strings.Contains(posts, "Work completed.") || strings.Contains(posts, "メモリを更新しました") || strings.Contains(posts, "## ユーザーメモリ追記") || strings.Contains(posts, "private memory") {
		t.Fatalf("posts = %q, want work result", posts)
	}
	for _, path := range []string{
		filepath.Join(bot.config.MemoryDir, "MEMORY.md"),
		filepath.Join(bot.config.MemoryDir, "users", "U1", "MEMORY.md"),
		filepath.Join(bot.config.MemoryDir, "channels", "C1", "MEMORY.md"),
	} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("user memory output should not be written to %s, stat error = %v", path, err)
		}
	}
}

func TestWorkFailureIsInterrupted(t *testing.T) {
	store := &fakeStore{claim: true}
	api := &fakeSlack{}
	runner := &fakeRunner{responses: []runnerResponse{
		{result: &codex.TurnResult{
			Completed: true,
			Messages:  []string{"## 方針\nDo work.\n## 作業指示\nMake a change."},
		}},
		{result: &codex.TurnResult{Completed: false, Err: "failed"}},
	}}
	newTestBot(t, store, api, runner).HandleMention(context.Background(), mention())
	if store.current != state.Interrupted {
		t.Fatalf("state = %q, want interrupted", store.current)
	}
	assertFinalReactionOrder(t, api.calls, "x")
}

// blockingInstruction marks the work prompt that gatedRunner holds open.
const blockingInstruction = "BLOCKWORK"

// looseStore accepts any transition so concurrent events do not interfere.
type looseStore struct {
	mu      sync.Mutex
	threads map[string]string
}

func (s *looseStore) ClaimEvent(string) (bool, error) { return true, nil }

func (s *looseStore) Transition(string, state.State, state.State) error { return nil }

func (s *looseStore) GetThread(key string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, ok := s.threads[key]
	return id, ok
}

func (s *looseStore) SetThread(key, threadID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.threads[key] = threadID
	return nil
}

// gatedRunner holds the work turn open until release is closed so a test can
// observe what other turns may run concurrently.
type gatedRunner struct {
	workStarted chan struct{}
	release     chan struct{}
	releaseOnce sync.Once
}

func (r *gatedRunner) Run(_ context.Context, _, _, _ string, _ []string, prompt string, callback func(string) error) (*codex.TurnResult, error) {
	if callback != nil {
		if err := callback("plan-thread"); err != nil {
			return nil, err
		}
	}
	if strings.Contains(prompt, blockingInstruction) {
		close(r.workStarted)
		<-r.release
		return &codex.TurnResult{Completed: true, Messages: []string{"Work completed."}}, nil
	}
	if strings.Contains(prompt, "second") {
		return &codex.TurnResult{
			Completed: true,
			Messages:  []string{"## 方針\nNothing to do.\n## 作業指示\nNONE"},
		}, nil
	}
	return &codex.TurnResult{
		Completed: true,
		Messages:  []string{"## 方針\nWork on it.\n## 作業指示\n" + blockingInstruction},
	}, nil
}

func (r *gatedRunner) releaseWork() {
	r.releaseOnce.Do(func() { close(r.release) })
}

func TestPlanTurnRunsWhileWorkTurnIsBlocked(t *testing.T) {
	runner := &gatedRunner{
		workStarted: make(chan struct{}),
		release:     make(chan struct{}),
	}
	bot := New(&fakeSlack{}, &looseStore{threads: map[string]string{}}, runner, Config{
		AllowedUserIDs: []string{"U1"},
		WorkspaceDir:   "/repo/workspace",
		MemoryDir:      filepath.Join(t.TempDir(), "memory"),
		CodexTimeout:   time.Minute,
		BotUserID:      "UBOT",
	}, nil)

	ctx := context.Background()
	workDone := make(chan struct{})
	go func() {
		defer close(workDone)
		bot.HandleMention(ctx, mention())
	}()
	defer func() {
		runner.releaseWork()
		<-workDone
	}()

	select {
	case <-runner.workStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("work turn did not start")
	}

	planDone := make(chan struct{})
	go func() {
		defer close(planDone)
		bot.HandleMention(ctx, &slackevents.AppMentionEvent{
			User: "U1", Channel: "C1", TimeStamp: "200.2", Text: "<@UBOT> second",
		})
	}()
	select {
	case <-planDone:
	case <-time.After(5 * time.Second):
		t.Fatal("plan turn did not finish while a work turn was blocked")
	}
}

func TestSplitMessageUsesRuneBoundaries(t *testing.T) {
	text := strings.Repeat("a", 3899) + "日" + "本"
	chunks := splitMessage(text, maxSlackMessageRunes)
	if len(chunks) != 2 {
		t.Fatalf("chunks = %d, want 2", len(chunks))
	}
	if len([]rune(chunks[0])) != 3900 || chunks[0][len(chunks[0])-3:] != "日" {
		t.Fatalf("first chunk has %d runes and suffix %q", len([]rune(chunks[0])), chunks[0][len(chunks[0])-3:])
	}
	if chunks[1] != "本" || strings.Join(chunks, "") != text {
		t.Fatalf("split did not preserve UTF-8 text")
	}
}

func TestPostSplitsLongMessage(t *testing.T) {
	api := &fakeSlack{}
	bot := newTestBot(t, &fakeStore{}, api, &fakeRunner{})
	text := strings.Repeat("界", 3901)
	if err := bot.post(context.Background(), "C1", "1", text); err != nil {
		t.Fatalf("post() error = %v", err)
	}
	if len(api.postTexts) != 2 {
		t.Fatalf("post attempts = %d, want 2", len(api.postTexts))
	}
	if len([]rune(api.postTexts[0])) != 3900 || api.postTexts[1] != "界" {
		t.Fatalf("post chunks have rune lengths %d and %d", len([]rune(api.postTexts[0])), len([]rune(api.postTexts[1])))
	}
}

func TestPostRetriesRateLimitAfterDelay(t *testing.T) {
	api := &fakeSlack{postErrs: []error{&slack.RateLimitedError{RetryAfter: 3 * time.Second}, nil}}
	bot := newTestBot(t, &fakeStore{}, api, &fakeRunner{})
	var waited time.Duration
	bot.sleep = func(_ context.Context, duration time.Duration) error {
		waited = duration
		return nil
	}
	if err := bot.post(context.Background(), "C1", "1", "hello"); err != nil {
		t.Fatalf("post() error = %v", err)
	}
	if waited != 3*time.Second {
		t.Fatalf("waited = %v, want 3s", waited)
	}
	if len(api.postTexts) != 2 || api.postTexts[0] != "hello" || api.postTexts[1] != "hello" {
		t.Fatalf("post attempts = %q", api.postTexts)
	}
}

func assertTransitions(t *testing.T, got, want [][2]state.State) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("transitions = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("transition %d = %v, want %v", i, got[i], want[i])
		}
	}
}

func assertFinalReactionOrder(t *testing.T, calls []slackCall, final string) {
	t.Helper()
	addIndex, removeIndex := -1, -1
	for i, call := range calls {
		if call.kind == "add:"+final {
			addIndex = i
		}
		if call.kind == "remove:eyes" {
			removeIndex = i
		}
	}
	if addIndex < 0 || removeIndex < 0 || addIndex >= removeIndex {
		t.Fatalf("reaction calls = %v, want add:%s before remove:eyes", calls, final)
	}
}

func assertStatusSequence(t *testing.T, calls []slackCall, want []string) {
	t.Helper()
	var got []string
	for _, call := range calls {
		if call.kind == "status" {
			got = append(got, call.text)
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("status sequence = %q, want %q", got, want)
	}
}

func receiveDuration(t *testing.T, calls <-chan time.Duration) time.Duration {
	t.Helper()
	select {
	case duration := <-calls:
		return duration
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for status refresh")
		return 0
	}
}
