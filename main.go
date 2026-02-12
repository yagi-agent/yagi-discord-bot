package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/bwmarrin/discordgo"
	openai "github.com/sashabaranov/go-openai"
	"github.com/yagi-agent/yagi/engine"
	"github.com/yagi-agent/yagi/provider"
)

type contextKey string

const ctxKeyUserID contextKey = "userID"

const (
	maxSessionMessages = 100
	sessionExpiry      = 30 * time.Minute
)

type userSession struct {
	mu       sync.Mutex
	messages []openai.ChatCompletionMessage
	lastUsed time.Time
}

type sessionStore struct {
	mu       sync.Mutex
	sessions map[string]*userSession
	dataDir  string
}

func newSessionStore(dataDir string) *sessionStore {
	return &sessionStore{
		sessions: make(map[string]*userSession),
		dataDir:  dataDir,
	}
}

func (s *sessionStore) get(userID string) *userSession {
	s.mu.Lock()
	defer s.mu.Unlock()

	sess, ok := s.sessions[userID]
	if !ok {
		sess = &userSession{}
		msgs, err := loadSession(s.dataDir, userID)
		if err != nil {
			log.Printf("failed to load session for %s: %v", userID, err)
		} else {
			sess.messages = msgs
		}
		s.sessions[userID] = sess
	}
	sess.lastUsed = time.Now()
	return sess
}

func (s *sessionStore) gc() {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	for id, sess := range s.sessions {
		if now.Sub(sess.lastUsed) > sessionExpiry {
			delete(s.sessions, id)
		}
	}
}

func sessionFilePath(dataDir, userID string) string {
	h := sha256.Sum256([]byte(userID))
	name := fmt.Sprintf("%x.json", h[:16])
	return filepath.Join(dataDir, "sessions", name)
}

type sessionData struct {
	UserID    string                         `json:"user_id"`
	UpdatedAt string                         `json:"updated_at"`
	Messages  []openai.ChatCompletionMessage `json:"messages"`
}

func saveSession(dataDir, userID string, messages []openai.ChatCompletionMessage) error {
	dir := filepath.Join(dataDir, "sessions")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}

	filtered := make([]openai.ChatCompletionMessage, 0, len(messages))
	for _, m := range messages {
		if m.Role == openai.ChatMessageRoleSystem {
			continue
		}
		filtered = append(filtered, m)
	}
	if len(filtered) == 0 {
		return nil
	}

	filtered = truncateMessages(filtered, maxSessionMessages)

	sd := sessionData{
		UserID:    userID,
		UpdatedAt: time.Now().UTC().Format(time.RFC3339),
		Messages:  filtered,
	}

	data, err := json.MarshalIndent(sd, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(sessionFilePath(dataDir, userID), data, 0600)
}

func loadSession(dataDir, userID string) ([]openai.ChatCompletionMessage, error) {
	data, err := os.ReadFile(sessionFilePath(dataDir, userID))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var sd sessionData
	if err := json.Unmarshal(data, &sd); err != nil {
		return nil, err
	}
	return sd.Messages, nil
}

func truncateMessages(msgs []openai.ChatCompletionMessage, max int) []openai.ChatCompletionMessage {
	if len(msgs) <= max {
		return msgs
	}
	msgs = msgs[len(msgs)-max:]
	for len(msgs) > 0 && msgs[0].Role != openai.ChatMessageRoleUser {
		msgs = msgs[1:]
	}
	return msgs
}

type memoryStore struct {
	mu      sync.Mutex
	dataDir string
}

func newMemoryStore(dataDir string) *memoryStore {
	return &memoryStore{dataDir: dataDir}
}

func (ms *memoryStore) path(userID string) string {
	return filepath.Join(ms.dataDir, "memory", userID+".json")
}

func (ms *memoryStore) load(userID string) (map[string]string, error) {
	data, err := os.ReadFile(ms.path(userID))
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]string{}, nil
		}
		return nil, err
	}
	var m map[string]string
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return m, nil
}

func (ms *memoryStore) save(userID string, data map[string]string) error {
	dir := filepath.Join(ms.dataDir, "memory")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(ms.path(userID), b, 0600)
}

func (ms *memoryStore) set(userID, key, value string) error {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	m, err := ms.load(userID)
	if err != nil {
		return err
	}
	m[key] = value
	return ms.save(userID, m)
}

func (ms *memoryStore) get(userID, key string) (string, error) {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	m, err := ms.load(userID)
	if err != nil {
		return "", err
	}
	return m[key], nil
}

func (ms *memoryStore) delete(userID, key string) error {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	m, err := ms.load(userID)
	if err != nil {
		return err
	}
	delete(m, key)
	return ms.save(userID, m)
}

func (ms *memoryStore) list(userID string) (map[string]string, error) {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	return ms.load(userID)
}

func (ms *memoryStore) asMarkdown(userID string) string {
	m, err := ms.load(userID)
	if err != nil || len(m) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("\n---\n## Learned Information\n")
	for k, v := range m {
		sb.WriteString("- ")
		sb.WriteString(k)
		sb.WriteString(": ")
		sb.WriteString(v)
		sb.WriteString("\n")
	}
	return sb.String()
}

func splitMessage(content string, limit int) []string {
	if len(content) <= limit {
		return []string{content}
	}

	var parts []string
	for len(content) > 0 {
		if len(content) <= limit {
			parts = append(parts, content)
			break
		}

		cut := limit
		if idx := strings.LastIndex(content[:cut], "\n"); idx > 0 {
			cut = idx + 1
		}

		parts = append(parts, content[:cut])
		content = content[cut:]
	}
	return parts
}

func main() {
	defaultDataDir := ""
	if home, err := os.UserHomeDir(); err == nil {
		defaultDataDir = filepath.Join(home, ".config", "yagi-discord-bot")
	}

	token := flag.String("token", os.Getenv("DISCORD_BOT_TOKEN"), "Discord bot token")
	modelFlag := flag.String("model", os.Getenv("YAGI_MODEL"), "Provider/model (e.g. openai/gpt-4.1-nano)")
	apiKey := flag.String("key", "", "API key (overrides environment variable)")
	prefix := flag.String("prefix", "!", "Command prefix")
	identityFile := flag.String("identity", "", "Path to identity file (default: <data>/IDENTITY.md)")
	dataDir := flag.String("data", defaultDataDir, "Data directory for session storage")
	flag.Parse()

	if *token == "" {
		log.Fatal("Discord bot token is required: set DISCORD_BOT_TOKEN or use -token")
	}

	// Clear the environment variable after reading the token for security
	os.Setenv("DISCORD_BOT_TOKEN", "")

	if *modelFlag == "" {
		*modelFlag = "openai/gpt-4.1-nano"
	}

	providerName, modelName, ok := strings.Cut(*modelFlag, "/")
	if !ok {
		log.Fatalf("Invalid model format: %s (use provider/model)", *modelFlag)
	}

	p := provider.Find(providerName, provider.DefaultProviders)
	if p == nil {
		log.Fatalf("Unknown provider: %s", providerName)
	}

	key := *apiKey
	if key == "" && p.EnvKey != "" {
		key = os.Getenv(p.EnvKey)
	}

	client := provider.NewClient(p, key)

	idPath := *identityFile
	if idPath == "" {
		idPath = filepath.Join(*dataDir, "IDENTITY.md")
	}
	var systemPrompt string
	if data, err := os.ReadFile(idPath); err == nil {
		systemPrompt = string(data)
	} else if !os.IsNotExist(err) {
		log.Printf("Warning: failed to read identity file: %v", err)
	}

	mem := newMemoryStore(*dataDir)

	eng := engine.New(engine.Config{
		Client: client,
		Model:  modelName,
		SystemMessage: func(skill string) string {
			return systemPrompt
		},
	})

	eng.RegisterTool("saveMemoryEntry", "Save information to memory. Use this when user wants to remember something.", json.RawMessage(`{
		"type": "object",
		"properties": {
			"key": {
				"type": "string",
				"description": "A short identifier for what to remember (e.g., 'user_name', 'favorite_language')"
			},
			"value": {
				"type": "string",
				"description": "The information to remember"
			}
		},
		"required": ["key", "value"]
	}`), func(ctx context.Context, args string) (string, error) {
		userID := ctx.Value(ctxKeyUserID).(string)
		var req struct {
			Key   string `json:"key"`
			Value string `json:"value"`
		}
		if err := json.Unmarshal([]byte(args), &req); err != nil {
			return "", err
		}
		if err := mem.set(userID, req.Key, req.Value); err != nil {
			return "", err
		}
		return "Saved", nil
	}, true)

	eng.RegisterTool("getMemoryEntry", "Retrieve information from memory.", json.RawMessage(`{
		"type": "object",
		"properties": {
			"key": {
				"type": "string",
				"description": "The identifier of the information to recall"
			}
		},
		"required": ["key"]
	}`), func(ctx context.Context, args string) (string, error) {
		userID := ctx.Value(ctxKeyUserID).(string)
		var req struct {
			Key string `json:"key"`
		}
		if err := json.Unmarshal([]byte(args), &req); err != nil {
			return "", err
		}
		return mem.get(userID, req.Key)
	}, true)

	eng.RegisterTool("deleteMemoryEntry", "Delete information from memory.", json.RawMessage(`{
		"type": "object",
		"properties": {
			"key": {
				"type": "string",
				"description": "The identifier of the information to forget"
			}
		},
		"required": ["key"]
	}`), func(ctx context.Context, args string) (string, error) {
		userID := ctx.Value(ctxKeyUserID).(string)
		var req struct {
			Key string `json:"key"`
		}
		if err := json.Unmarshal([]byte(args), &req); err != nil {
			return "", err
		}
		if err := mem.delete(userID, req.Key); err != nil {
			return "", err
		}
		return "Deleted", nil
	}, true)

	eng.RegisterTool("listMemoryEntries", "List all saved information.", json.RawMessage(`{
		"type": "object",
		"properties": {}
	}`), func(ctx context.Context, args string) (string, error) {
		userID := ctx.Value(ctxKeyUserID).(string)
		m, err := mem.list(userID)
		if err != nil {
			return "", err
		}
		if len(m) == 0 {
			return "{}", nil
		}
		b, err := json.Marshal(m)
		if err != nil {
			return "", err
		}
		return string(b), nil
	}, true)

	store := newSessionStore(*dataDir)

	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			store.gc()
		}
	}()

	dg, err := discordgo.New("Bot " + *token)
	if err != nil {
		log.Fatalf("Failed to create Discord session: %v", err)
	}

	dg.AddHandler(func(s *discordgo.Session, m *discordgo.MessageCreate) {
		if m.Author.ID == s.State.User.ID {
			return
		}

		content := m.Content

		ch, err := s.State.Channel(m.ChannelID)
		if err != nil {
			ch, err = s.Channel(m.ChannelID)
			if err != nil {
				return
			}
		}
		isDM := ch.Type == discordgo.ChannelTypeDM

		if !isDM {
			mentioned := false
			for _, mention := range m.Mentions {
				if mention.ID == s.State.User.ID {
					mentioned = true
					content = strings.ReplaceAll(content, "<@"+s.State.User.ID+">", "")
					content = strings.ReplaceAll(content, "<@!"+s.State.User.ID+">", "")
					content = strings.TrimSpace(content)
					break
				}
			}

			if !mentioned && !strings.HasPrefix(content, *prefix) {
				return
			}

			if !mentioned {
				content = strings.TrimPrefix(content, *prefix)
				content = strings.TrimSpace(content)
			}
		}

		if content == "" {
			return
		}

		s.ChannelTyping(m.ChannelID)

		sess := store.get(m.Author.ID)
		sess.mu.Lock()
		defer sess.mu.Unlock()

		sess.messages = append(sess.messages, engine.UserMessage(content)...)

		chatMsgs := sess.messages
		if memMd := mem.asMarkdown(m.Author.ID); memMd != "" {
			sysContent := systemPrompt + memMd
			chatMsgs = append([]openai.ChatCompletionMessage{{
				Role:    openai.ChatMessageRoleSystem,
				Content: sysContent,
			}}, chatMsgs...)
		}

		ctx := context.WithValue(context.Background(), ctxKeyUserID, m.Author.ID)
		reply, updatedMsgs, err := eng.Chat(ctx, chatMsgs, engine.ChatOptions{})
		if err != nil {
			log.Printf("engine error: %v", err)
			s.ChannelMessageSend(m.ChannelID, "エラーが発生しました: "+err.Error())
			return
		}
		filtered := updatedMsgs
		if len(filtered) > 0 && filtered[0].Role == openai.ChatMessageRoleSystem {
			filtered = filtered[1:]
		}
		sess.messages = filtered

		if err := saveSession(store.dataDir, m.Author.ID, sess.messages); err != nil {
			log.Printf("failed to save session for %s: %v", m.Author.ID, err)
		}

		if reply == "" {
			reply = "(応答なし)"
		}

		const discordLimit = 2000
		for _, part := range splitMessage(reply, discordLimit) {
			if _, err := s.ChannelMessageSendReply(m.ChannelID, part, m.Reference()); err != nil {
				log.Printf("send error: %v", err)
			}
		}
	})

	dg.Identify.Intents = discordgo.IntentsGuildMessages | discordgo.IntentsDirectMessages | discordgo.IntentsMessageContent

	if err := dg.Open(); err != nil {
		log.Fatalf("Failed to open Discord connection: %v", err)
	}
	defer dg.Close()

	log.Println("yagi-discord-bot is running. Press Ctrl+C to stop.")

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig

	log.Println("Shutting down...")
}
