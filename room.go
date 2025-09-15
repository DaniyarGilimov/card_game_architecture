package gamearchitecture

import (
	"context"
	"errors"
	"log"
	"strconv"

	gamemodel "github.com/daniyargilimov/card_game_model"
)

// NewRoom creating new room
func NewRoom(rManager *RoomManager, ri *gamemodel.RoomInfo, ctx context.Context, cancelFunc context.CancelFunc) *Room {
	if rManager.RoomsCount > 8999 {
		rManager.RoomsCount = 0
	}
	id := 1000 + rManager.RoomsCount

	if ri.RoomSize <= 0 { // Default room size if not provided
		ri.RoomSize = 6
	}

	ri.ID = id
	// Set default name only if not already set (e.g., by bot creation logic)
	if ri.Name == "" {
		ri.Name = "Room #" + strconv.Itoa(id)
	}

	broadcastChannel := make(chan []byte)
	broadcastExceptChannel := make(chan gamemodel.UserIdAndByte)
	unicastChannel := make(chan gamemodel.UserIdAndByte)

	Room := &Room{
		Ctx:           ctx,
		ContextCancel: cancelFunc,
		ID:            id,
		Service:       rManager.Services,
		PlayerConns:   []*PlayerConn{},
		RoomInfo:      ri,

		Join:                   make(chan *PlayerConn),
		Leave:                  make(chan *PlayerConn),
		BroadcastChannel:       broadcastChannel,
		BroadcastExceptChannel: broadcastExceptChannel,
		UnicastChannel:         unicastChannel,

		PingChannel: make(chan int),
	}

	Room.Game = rManager.NewGame(
		ctx,
		cancelFunc,
		ri,
		rManager.Services,
		broadcastChannel,
		broadcastExceptChannel,
		unicastChannel,
	)

	rManager.RoomsCount++

	return Room
}

func Run(r *Room, rManager *RoomManager) error {
	go r.Game.RunGameListener()
	go handleBroadcasts(r, rManager)

	for {
		select {
		case _, ok := <-r.PingChannel:
			if !ok {
				log.Print("error: room r.PingChannel is not ok")
				return nil
			}

		case c, ok := <-r.Join:
			if !ok {
				return nil
			}
			handleJoin(r, rManager, c)

		case leavePlayer, ok := <-r.Leave:
			if !ok {
				return nil
			}
			if err := handleLeave(r, rManager, leavePlayer); err != nil {
				return err
			}

		case <-r.Ctx.Done():
			return r.Ctx.Err()
		}
	}
}

// handleBroadcasts
func handleBroadcasts(r *Room, roomManager *RoomManager) {
	for {
		select {
		case userIdAndMsgUnicast, ok := <-r.UnicastChannel:
			if !ok {
				// BroadcastChannel closed, likely room is shutting down.
				return
			}

			r.PlayerConnsLock.RLock()
			if len(r.PlayerConns) == 0 {
				r.PlayerConnsLock.RUnlock()
				continue
			}
			// Create a snapshot of players to iterate over.
			// This avoids holding the lock during potentially blocking send operations.
			playersToBroadcast := make([]*PlayerConn, len(r.PlayerConns))
			copy(playersToBroadcast, r.PlayerConns)
			r.PlayerConnsLock.RUnlock()

			for _, pc := range playersToBroadcast {
				if pc.Player.PlayerID == userIdAndMsgUnicast.UserID {
					go SendState(userIdAndMsgUnicast.Byte, pc, playersToBroadcast, r, roomManager)
					break
				}
			}

		case userIdAndMsgExcept, ok := <-r.BroadcastExceptChannel:
			if !ok {
				// BroadcastChannel closed, likely room is shutting down.
				return
			}

			r.PlayerConnsLock.RLock()
			if len(r.PlayerConns) == 0 {
				r.PlayerConnsLock.RUnlock()
				continue
			}
			// Create a snapshot of players to iterate over.
			// This avoids holding the lock during potentially blocking send operations.
			playersToBroadcast := make([]*PlayerConn, len(r.PlayerConns))
			copy(playersToBroadcast, r.PlayerConns)
			r.PlayerConnsLock.RUnlock()

			playersInGame := make([]*gamemodel.Player, len(r.Game.GetPlayers()))
			copy(playersInGame, r.Game.GetPlayers())

			for _, pc := range playersToBroadcast {
				if pc.Player.PlayerID == userIdAndMsgExcept.UserID {
					continue
				}

				needToSendState := false
				for _, player := range playersInGame {
					if player.PlayerID == pc.Player.PlayerID {
						needToSendState = true
						break
					}
				}
				// SendState (from player.go) sends to pc.Ch.
				// It's important that SendState is non-blocking or handles closed channels gracefully.
				// The existing SendState in socket/game_backbone/player.go uses a buffered channel
				// and has a recover, which is good.
				if needToSendState {
					go SendState(userIdAndMsgExcept.Byte, pc, playersToBroadcast, r, roomManager) // Send in a goroutine to prevent one slow player blocking others.
				}
			}
		case msg, ok := <-r.BroadcastChannel:
			if !ok {
				// BroadcastChannel closed, likely room is shutting down.
				return
			}

			r.PlayerConnsLock.RLock()
			if len(r.PlayerConns) == 0 {
				r.PlayerConnsLock.RUnlock()
				continue
			}
			// Create a snapshot of players to iterate over.
			// This avoids holding the lock during potentially blocking send operations.
			playersToBroadcast := make([]*PlayerConn, len(r.PlayerConns))
			copy(playersToBroadcast, r.PlayerConns)
			r.PlayerConnsLock.RUnlock()

			playersInGame := make([]*gamemodel.Player, len(r.Game.GetPlayers()))
			copy(playersInGame, r.Game.GetPlayers())

			for _, pc := range playersToBroadcast {
				needToSendState := false
				for _, player := range playersInGame {
					if player.PlayerID == pc.Player.PlayerID {
						needToSendState = true
						break
					}
				}
				// SendState (from player.go) sends to pc.Ch.
				// It's important that SendState is non-blocking or handles closed channels gracefully.
				// The existing SendState in socket/game_backbone/player.go uses a buffered channel
				// and has a recover, which is good.
				if needToSendState {
					go SendState(msg, pc, playersToBroadcast, r, roomManager) // Send in a goroutine to prevent one slow player blocking others.
				}
			}
		case <-r.Ctx.Done():
			return
		}
	}
}

func Join(ctx context.Context, r *Room, playerConn *PlayerConn, rManager *RoomManager) {
	defer func() {
		if rec := recover(); rec != nil {
			playerConn.WS.Close()
		}
	}()

	select {
	case r.Join <- playerConn:
	case <-r.Ctx.Done():
	case <-ctx.Done():
		playerConn.WS.Close()
	}
}

func handleJoin(r *Room, rManager *RoomManager, c *PlayerConn) {
	// PlayerConnectionHandler will start PlayerWriter.
	// If it's a bot, PlayerConnectionHandler returns early after starting PlayerWriter.
	go PlayerConnectionHandler(c, r, rManager)

	// TODO: handle bot AI initialization
	if c.BotAI != nil {
		// Initialize the bot's AI. This might start the bot's own decision-making goroutine.
		c.BotAI.Initialize(c.Player, r.RoomInfo, r.Game.SendRoomToGameInst)
	}

	r.PlayerConnsLock.Lock()
	r.PlayerConns = append(r.PlayerConns, c)
	r.PlayerConnsLock.Unlock()

	ok := r.Game.SendRoomToGamePlayerJoin(c.Player, r.Ctx)
	if !ok {
		// TODO: game can be dead
	}
}

func handleLeave(r *Room, rManager *RoomManager, p *PlayerConn) error {
	// Removing from playerConns
	r.PlayerConnsLock.Lock()
	i := 0
	for _, spc := range r.PlayerConns {
		if spc.Player.PlayerID == p.Player.PlayerID {
			break
		}
		i++
	}
	if len(r.PlayerConns) == i {
		r.PlayerConnsLock.Unlock()
		return errors.New("removeFromPlayersConn: not found in players conn")
	}

	copy(r.PlayerConns[i:], r.PlayerConns[i+1:]) // Shift a[i+1:] left one index.

	if len(r.PlayerConns)-1 >= 0 {
		r.PlayerConns[len(r.PlayerConns)-1] = nil
		r.PlayerConns = r.PlayerConns[:len(r.PlayerConns)-1]
	}
	r.PlayerConnsLock.Unlock()

	r.Game.SendRoomToGamePlayerLeave(p.Player, r.Ctx)

	return nil
}
