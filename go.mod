module github.com/yagi-agent/yagi-discord-bot

go 1.25.6

require (
	github.com/bwmarrin/discordgo v0.29.0
	github.com/yagi-agent/yagi v0.0.34
)

require (
	github.com/gorilla/websocket v1.5.3 // indirect
	github.com/sashabaranov/go-openai v1.41.2 // indirect
	golang.org/x/crypto v0.48.0 // indirect
	golang.org/x/sys v0.41.0 // indirect
)

replace github.com/yagi-agent/yagi => ../yagi
