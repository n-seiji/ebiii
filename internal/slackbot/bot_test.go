package slackbot

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/n-seiji/ebiii/internal/codex"
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
	threadID    string
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

func (s *fakeStore) GetThread(string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.threadID, s.threadID != ""
}

func (s *fakeStore) SetThread(_, threadID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.threadID = threadID
	return nil
}

type slackCall struct {
	kind string
	text string
}

type fakeSlack struct {
	mu        sync.Mutex
	calls     []slackCall
	postErrs  []error
	postTexts []string
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
}

func (r *fakeRunner) Run(_ context.Context, _ string, _ string, _ string, _ []string, _ string, callback func(string) error) (*codex.TurnResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	if callback != nil {
		if err := callback("plan-thread"); err != nil {
			return nil, err
		}
	}
	response := r.responses[0]
	r.responses = r.responses[1:]
	return response.result, response.err
}

func newTestBot(store *fakeStore, api *fakeSlack, runner *fakeRunner) *Bot {
	return New(api, store, runner, Config{
		AllowedUserIDs: []string{"U1"},
		EBIIIHome:      "/repo",
		WorkspaceDir:   "/repo/workspace",
		MemoryDir:      "/repo/memory",
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
			bot := newTestBot(store, &fakeSlack{}, &fakeRunner{})
			if test.configure != nil {
				test.configure(bot)
			}
			bot.HandleMention(context.Background(), test.event)
			if store.claimCalls != 0 {
				t.Fatalf("ClaimEvent called %d times, want 0", store.claimCalls)
			}
		})
	}
}

func TestDuplicateEventIsSkipped(t *testing.T) {
	store := &fakeStore{claim: false}
	runner := &fakeRunner{}
	api := &fakeSlack{}
	newTestBot(store, api, runner).HandleMention(context.Background(), mention())
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
			newTestBot(store, api, runner).HandleMention(context.Background(), mention())
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
	newTestBot(store, api, runner).HandleMention(context.Background(), mention())
	assertTransitions(t, store.transitions, [][2]state.State{
		{state.Received, state.Planning},
		{state.Planning, state.PlanPosted},
		{state.PlanPosted, state.Done},
	})
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
	newTestBot(store, api, runner).HandleMention(context.Background(), mention())
	assertTransitions(t, store.transitions, [][2]state.State{
		{state.Received, state.Planning},
		{state.Planning, state.PlanPosted},
		{state.PlanPosted, state.Working},
		{state.Working, state.Done},
	})
	if runner.calls != 2 {
		t.Fatalf("runner calls = %d, want 2", runner.calls)
	}
	if got := strings.Join(api.postTexts, "|"); got != "Implement safely.|Work completed." {
		t.Fatalf("posts = %q", got)
	}
	assertFinalReactionOrder(t, api.calls, "white_check_mark")
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
	newTestBot(store, api, runner).HandleMention(context.Background(), mention())
	if store.current != state.Interrupted {
		t.Fatalf("state = %q, want interrupted", store.current)
	}
	assertFinalReactionOrder(t, api.calls, "x")
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
	bot := newTestBot(&fakeStore{}, api, &fakeRunner{})
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
	bot := newTestBot(&fakeStore{}, api, &fakeRunner{})
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
