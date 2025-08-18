package gamearchitecture

import (
	"encoding/json"
	"errors"
	"log"
	"sync"
	"time"

	gamemodel "github.com/daniyargilimov/card_game_model"

	model "github.com/daniyargilimov/card_api_model"

	"github.com/gorilla/websocket"
)

func NewPlayer(token string, rManager *RoomManager, tournamentID int) (*gamemodel.Player, error) {
	id, err := rManager.Services.ParseToken(token)
	if err != nil {
		return nil, err
	}
	userResult, err := rManager.Repo.GetUserByID(id)
	if err != nil {
		return nil, err
	}

	userResult.Inventory.PlayerID = userResult.UserID

	player := &gamemodel.Player{
		Token:     token,
		PushToken: userResult.PushToken,
		Name:      userResult.Username,
		PlayerID:  userResult.UserID,
	} //The playerId will be taken from mongodb

	return player, nil
}

func GetTablePlace(order []int, relPlayer *gamemodel.Player, placeIndex, roomSize int) (int, error) {
	playerIndex := 0
	for i := 0; i < len(order); i++ {
		if order[i] == relPlayer.PlayerID {
			playerIndex = i
		}
	}
	pl := 0
	for i := 0; i < 7; i++ {
		if playerIndex > 6 {
			playerIndex = 0
		}
		if pl > 6 {
			break
		}
		if playerIndex == placeIndex {
			return pl, nil
		}
		pl++
		playerIndex++
	}
	return 0, errors.New("not found")
}

func GetTableID(order []int, relPlayer *gamemodel.Player, place, roomSize int) (int, error) {
	for i := 0; i < len(order); i++ {
		if order[i] == relPlayer.PlayerID {
			return (i + place) % 6, nil
		}
	}
	return 0, errors.New("not found")
}

// NewPlayerConn used to create player connection
func NewPlayerConn(user *model.User, ws *websocket.Conn, room *Room) *PlayerConn { //CheckedFunction

	pc := &PlayerConn{
		WS: ws,
		Player: &gamemodel.Player{
			Token:       user.Token,
			PushToken:   user.PushToken,
			Name:        user.Username,
			PlayerID:    user.UserID,
			Inventory:   user.Inventory,
			RuntimeData: &gamemodel.PlayerRuntimeData{},
		},
		Token:        user.Token,
		Ch:           make(chan []byte),
		LastActivity: time.Now(),
	}

	pc.Mu = new(sync.Mutex)
	return pc
}

// NewBotPlayerConn creates a player connection for a bot.
func NewBotPlayerConn(botPlayer *gamemodel.Player, room *Room, botAI gamemodel.BotAI) *PlayerConn {
	if botPlayer == nil || !botPlayer.IsBot {
		log.Print("Error: NewBotPlayerConn called with non-bot player or nil player")
		return nil
	}
	return &PlayerConn{
		Player:       botPlayer,
		BotAI:        botAI,
		Ch:           make(chan []byte, 256), // Buffered channel for game updates to bot
		Mu:           new(sync.Mutex),
		LastActivity: time.Now(), // Initialize last activity
	}
}

// PlayerConnectionHandler handles both reading and writing for a player connection
func PlayerConnectionHandler(pc *PlayerConn, r *Room) {
	// Start writer in a goroutine
	go PlayerWriter(pc, r)
	if pc.BotAI != nil {
		return // Don't run the WebSocket reader loop for bots
	}

	// Set websocket read limits and pong handling
	pc.WS.SetReadLimit(MaxMessageSize)
	pc.WS.SetReadDeadline(time.Now().Add(PongWait))
	pc.WS.SetPongHandler(func(string) error {
		pc.WS.SetReadDeadline(time.Now().Add(PongWait))
		return nil
	})
	pc.LastActivity = time.Now()

	// Start reader (listener)
	for {
		select {
		case <-r.Ctx.Done():
			cleanupConnection(pc, r)
			return
		default:
			pc.WS.SetReadDeadline(time.Now().Add(PongWait))
			_, message, err := pc.WS.ReadMessage()
			if err != nil {
				cleanupConnection(pc, r)
				return
			}

			var si gamemodel.StatusInstruction
			if err := json.Unmarshal(message, &si); err != nil {
				// logrus.Warnf("Player %d sent invalid JSON: %s", pc.PlayerID, string(message))
				continue
			}

			switch si.Status {
			case "PLAYER_LEAVE":
				cleanupConnection(pc, r)

				// here we generate by ourself cz, frontend may send any player id and kick other players in frontend for others
				instruction := instPlayerLeft(pc.Player.PlayerID)
				select {
				case r.BroadcastChannel <- instruction:
				case <-r.Ctx.Done():
				case <-time.After(2 * time.Second):
				}
				return
			case "UTIL_MESSAGE", "UTIL_THROW":
				select {
				case r.BroadcastChannel <- message:
				case <-r.Ctx.Done():
				case <-time.After(2 * time.Second):
				}
			default:
				ok := r.Game.SendRoomToGameInst(message, r.Ctx) // Send the message to the game instance
				if !ok {
					// TODO: game can be dead
				}
			}
		}
	}
}

// PlayerWriter pumps messages from the hub to the websocket connection.
func PlayerWriter(pc *PlayerConn, r *Room) {
	ticker := time.NewTicker(PingPeriod)
	defer func() {
		ticker.Stop()
		cleanupConnection(pc, r)
	}()

	for {
		select {
		case message, ok := <-pc.Ch:
			if !ok {
				if pc.BotAI != nil {
					pc.BotAI.Shutdown()
					return
				}
				writeClose(pc, websocket.CloseNormalClosure, "channel closed")
				return
			}

			msgCopy := append([]byte(nil), message...)

			if pc.BotAI != nil {
				// Send the message to the bot's AI for processing.
				withSleep := true
				if r.Game.IsFastForward() {
					withSleep = false
				}

				go pc.BotAI.ReceiveGameUpdate(msgCopy, withSleep)

				// Check if the message is one that should cause the bot to "disconnect" (be removed)
				var inst gamemodel.PlayerLeftInstruction
				if err := json.Unmarshal(msgCopy, &inst); err == nil {
					if inst.Instruction == "UTIL_AFK" || inst.Instruction == "ROOM_MONEY_LESS" {
						return // Exit PlayerWriter, defer will call cleanupConnection
					}
				}
			} else {
				pc.WS.SetWriteDeadline(time.Now().Add(WriteWait))

				pc.Mu.Lock()
				err := pc.WS.WriteMessage(websocket.TextMessage, msgCopy)
				pc.Mu.Unlock()

				if err != nil {
					return
				}

				var inst gamemodel.PlayerLeftInstruction
				if err := json.Unmarshal(msgCopy, &inst); err == nil {
					if inst.Instruction == "UTIL_AFK" || inst.Instruction == "ROOM_MONEY_LESS" {
						// For human players, close the WebSocket and let cleanupConnection handle room leave.
						writeClose(pc, websocket.CloseNormalClosure, inst.Instruction)
						return
					}
				}
			}

		case <-ticker.C:
			if pc.BotAI != nil {
				// Bots don't need websocket pings. Their activity can be managed by their AI loop.
				continue
			}
			pc.WS.SetWriteDeadline(time.Now().Add(WriteWait))
			if err := pc.WS.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}

		case <-r.Ctx.Done():
			return
		}
	}
}

func cleanupConnection(pc *PlayerConn, room *Room) {
	pc.CleanupOnce.Do(func() {

		if pc.BotAI == nil && pc.WS != nil {
			// Best effort to close the WebSocket connection.
			// Closing an already closed connection is usually a no-op or returns an error that can be ignored here.
			_ = pc.WS.Close()
		}

		// Attempt to send the player to the room's leave channel.
		// Use a timeout to prevent blocking indefinitely if the room is not processing leave events.
		select {
		case room.Leave <- pc:
			// log.Printf("cleanupConnection: Sent player %d to room.Leave", pc.Player.PlayerID)
		case <-time.After(2 * time.Second): // Consider making this timeout configurable or part of utils
			// log.Printf("cleanupConnection: Timeout sending player %d to room.Leave", pc.Player.PlayerID)
		}

		close(pc.Ch) // Close the player's outbound message channel. This signals the PlayerWriter to stop.
		if pc.BotAI != nil {
			pc.BotAI.Shutdown()
		}
		// log.Printf("cleanupConnection: Closed channel for player %d", pc.Player.PlayerID)
	})
}

func instPlayerLeft(PlayerID int) []byte {
	pli := gamemodel.PlayerLeftInstruction{Instruction: "PLAYER_LEAVE"}
	pli.Data.ID = PlayerID
	b, _ := json.Marshal(pli)
	return b
}

func writeClose(pc *PlayerConn, code int, reason string) {
	pc.Mu.Lock()
	defer pc.Mu.Unlock()
	_ = pc.WS.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(code, reason))
}

func SendState(b []byte, pc *PlayerConn) {
	defer func() {
		recover()
	}()

	z := make([]byte, len(b))
	copy(z, b)
	pc.Ch <- z
}
