package gamearchitecture

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/rand"
	"strconv"
	"sync"
	"time"

	gamemodel "github.com/daniyargilimov/card_game_model"

	"github.com/gorilla/websocket"
	"github.com/sirupsen/logrus"
)

// !important
//  ROOM_MONEY_LESS, UTIL_AFK must be the same as in game_logic

func NewRoomManager(NewGame gamemodel.GameFactory, BotProvider BotProvider, services Service, repo Repo) *RoomManager {
	return &RoomManager{
		Services: services,
		Repo:     repo,

		AllRooms:       make(map[int]*Room),
		RoomsLock:      sync.RWMutex{},
		AllCloseRooms:  make(map[int]*Room),
		CloseRoomsLock: sync.RWMutex{},
		AllSearcher:    make(map[string]*SearcherConn),

		SearcherLock: sync.RWMutex{},
		RoomsCount:   0,

		JoiningPlayersMutex:   sync.RWMutex{},
		PlayersAttemptingJoin: make(map[int]bool),

		MutexLock:   sync.RWMutex{},
		MutexLocked: map[string]string{},
		NewGame:     NewGame,
		BotProvider: BotProvider,
	}
}

func RoomStalker(rManager *RoomManager) {
	for {
		rManager.RoomsLock.RLock()
		for _, r := range rManager.AllRooms {
			go func(r *Room) {
				select {
				case r.PingChannel <- 1:
				case <-time.After(3 * time.Second):
					RoomDelete(r, rManager)
					errorString := fmt.Sprintf("Deleting stuck room %d \n with name %s and size %d len of players %d, \n with RoomData infoStuck: %s ;\n  RoomData infoStuckCause %s ; \n GameBridge infoStuck: %s ; \n GameBridge infoStuckCause: %s ;\n BetData infoStuck: %s ;\n BetData infoStuckCause: %s ;\n PlayData infoStuck: %s ;\n PlayData infoStuckCause: %s ; \n GameBridger is running: %t;\n  GameState: %s ;\n  GameGlobalState: %s; \n All Actions: %s", r.ID,
						r.RoomInfo.Name,
						r.RoomInfo.RoomSize,
						len(r.PlayerConns),
					)
					logrus.Errorf(errorString)
					go r.Service.SendDelete(errorString)
				}
			}(r)
		}
		rManager.RoomsLock.RUnlock()
		rManager.CloseRoomsLock.RLock()

		for _, r := range rManager.AllCloseRooms {
			go func(r *Room) {
				select {
				case r.PingChannel <- 1:
				case <-time.After(3 * time.Second):
					RoomDelete(r, rManager)
				}
			}(r)
		}

		rManager.CloseRoomsLock.RUnlock()
		<-time.After(10 * time.Second)
	}
}

// TryMarkJoining attempts to mark a player as "in the process of joining".
// It returns true if successful (player was not already marked and is now marked),
// false otherwise (player is already in the process).
func TryMarkJoining(playerID int, rm *RoomManager) bool {
	rm.JoiningPlayersMutex.Lock()
	defer rm.JoiningPlayersMutex.Unlock()

	if rm.PlayersAttemptingJoin[playerID] {
		return true // Already marked as joining, indicate failure to mark again by returning true for "already joining"
	}

	rm.PlayersAttemptingJoin[playerID] = true
	return false // Successfully marked
}

// ClearJoiningMark removes the "joining" mark for a player.
func ClearJoiningMark(playerID int, rm *RoomManager) {
	rm.JoiningPlayersMutex.Lock()
	defer rm.JoiningPlayersMutex.Unlock()

	delete(rm.PlayersAttemptingJoin, playerID)
}

func AlreadyPlaying(playerToken string, rManager *RoomManager) error {
	rManager.RoomsLock.Lock()
	defer rManager.RoomsLock.Unlock()
	for _, r := range rManager.AllRooms {
		for _, p := range r.PlayerConns {
			if p.Token == playerToken {
				return errors.New("player already playing")
			}
		}
	}
	for _, r := range rManager.AllCloseRooms {
		for _, p := range r.PlayerConns {
			if p.Token == playerToken {
				return errors.New("player already playing")
			}
		}
	}
	return nil
}

func RoomDelete(fRoom *Room, rManager *RoomManager) {
	if fRoom.RoomInfo.Password == "" {
		rManager.RoomsLock.Lock()
		delete(rManager.AllRooms, fRoom.ID)
		rManager.RoomsLock.Unlock()
	} else {
		rManager.CloseRoomsLock.Lock()
		delete(rManager.AllCloseRooms, fRoom.ID)
		rManager.CloseRoomsLock.Unlock()
	}
}

func RoomCloseChannels(fRoom *Room) {
	defer func() {
		if r := recover(); r != nil { //This used to solve closed channel issue
			logrus.Errorf("RoomCloseChannels %s", r)
		}
	}()

	close(fRoom.BroadcastChannel)
	close(fRoom.Join)
	close(fRoom.Leave)
	close(fRoom.PingChannel)
}

func RoomJoinByID(playerToken string, roomID int, ws *websocket.Conn, rManager *RoomManager) {
	user, err := rManager.Services.GetUserByToken(playerToken)
	if err != nil {
		ws.Close()
		return
	}

	if TryMarkJoining(user.UserID, rManager) {
		logrus.Warnf("Player %d is already attempting to join a room.", user.UserID)
		ws.Close()
		return
	}
	defer ClearJoiningMark(user.UserID, rManager)

	if err := AlreadyPlaying(user.Token, rManager); err != nil {
		ws.Close()
		return
	}

	var fRoom *Room

	fRoom = rManager.AllRooms[roomID]
	if fRoom == nil {
		fRoom = rManager.AllCloseRooms[roomID]
		if fRoom == nil {
			ws.Close()
			return
		}
	}

	if user.Inventory.Chips < fRoom.RoomInfo.InitialBet*3 {
		b := InstNotEnoughMoney()
		ws.WriteMessage(websocket.TextMessage, b)
		ws.Close()
		return
	}

	fRoom.PlayerConnsLock.RLock()
	roomCount := len(fRoom.PlayerConns)
	fRoom.PlayerConnsLock.RUnlock()

	if fRoom.RoomInfo.RoomSize <= roomCount {
		b := InstRoomIsFull()
		ws.WriteMessage(websocket.TextMessage, b)
		ws.Close()
		return
	}

	playerConn := NewPlayerConn(user, ws, fRoom)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)

	defer cancel()

	Join(ctx, fRoom, playerConn, rManager)
}

func RoomCreate(request *RequestJoinRoom, ws *websocket.Conn, rManager *RoomManager) {
	user, err := rManager.Services.GetUserByToken(request.PlayerToken)
	if err != nil {
		ws.Close()
		return
	}
	log.Print("RoomCreate: with initial bet: ", request.RoomInfo.InitialBet)

	if TryMarkJoining(user.UserID, rManager) {
		logrus.Warnf("Player %d is already attempting to join a room.", user.UserID)
		ws.Close()
		return
	}
	defer ClearJoiningMark(user.UserID, rManager)

	if err := AlreadyPlaying(user.Token, rManager); err != nil {
		ws.Close()
		return
	}

	if user.Inventory.Chips < request.RoomInfo.InitialBet*3 {
		// b := InstNotEnoughMoney()
		// ws.WriteMessage(websocket.TextMessage, b)

		ws.Close()
		return
	}

	mainContext := context.Background() //TODO: findout which one to use
	ctx, cancle := context.WithCancel(mainContext)

	fRoom := NewRoom(rManager, request.RoomInfo, ctx, cancle)

	go func() {
		if err := Run(fRoom, rManager); err != nil {
			log.Print("Closing room with err: " + err.Error())
		}

		RoomDelete(fRoom, rManager)
		cancle()
		RoomCloseChannels(fRoom)
	}()

	if request.RoomInfo.InitialBet == 100 || request.RoomInfo.InitialBet == 1000 || request.RoomInfo.InitialBet == 10000 {
		fRoom.RoomInfo.Password = roomAssignPassword(rManager)
		fRoom.RoomInfo.Name = "Password: " + fRoom.RoomInfo.Password
		rManager.CloseRoomsLock.Lock()
		rManager.AllCloseRooms[fRoom.ID] = fRoom
		rManager.CloseRoomsLock.Unlock()
	} else {
		rManager.RoomsLock.Lock()
		rManager.AllRooms[fRoom.ID] = fRoom
		rManager.RoomsLock.Unlock()
	}

	rManager.RoomsLock.Lock()
	rManager.AllRooms[fRoom.ID] = fRoom
	rManager.RoomsLock.Unlock()

	playerConn := NewPlayerConn(user, ws, fRoom)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)

	defer cancel()

	Join(ctx, fRoom, playerConn, rManager)

}

func RoomJoinPrivateV2(request *RequestJoinRoom, ws *websocket.Conn, rManager *RoomManager) {
	user, err := rManager.Services.GetUserByToken(request.PlayerToken)
	if err != nil {
		log.Print("RoomJoinPrivateV2: token")
		ws.Close()
		return
	}

	if TryMarkJoining(user.UserID, rManager) {
		logrus.Warnf("Player %d is already attempting to join a room.", user.UserID)
		ws.Close()
		return
	}
	defer ClearJoiningMark(user.UserID, rManager)

	if err := AlreadyPlaying(user.Token, rManager); err != nil {
		log.Print("RoomJoinPrivateV2: playing")
		ws.Close()
		return
	}

	var fRoom *Room
	rManager.CloseRoomsLock.RLock()
	for _, r := range rManager.AllCloseRooms {
		if r.RoomInfo.Password == request.RoomInfo.Password {
			fRoom = r
			break
		}
	}
	rManager.CloseRoomsLock.RUnlock()

	if fRoom == nil {
		ws.Close()
		return
	}

	if user.Inventory.Chips < fRoom.RoomInfo.InitialBet*3 {
		b := InstNotEnoughMoney()
		ws.WriteMessage(websocket.TextMessage, b)
		ws.Close()
		return
	}

	fRoom.PlayerConnsLock.RLock()
	roomCount := len(fRoom.PlayerConns)
	fRoom.PlayerConnsLock.RUnlock()

	if fRoom.RoomInfo.RoomSize <= roomCount {
		b := InstRoomIsFull()
		ws.WriteMessage(websocket.TextMessage, b)
		ws.Close()
		return
	}

	playerConn := NewPlayerConn(user, ws, fRoom)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)

	defer cancel()

	Join(ctx, fRoom, playerConn, rManager)
}

func RoomJoinAny(playerToken string, ws *websocket.Conn, rManager *RoomManager) {
	user, err := rManager.Services.GetUserByToken(playerToken)
	if err != nil {
		ws.Close()
		return
	}

	if TryMarkJoining(user.UserID, rManager) {
		logrus.Warnf("Player %d is already attempting to join a room.", user.UserID)
		ws.Close()
		return
	}
	defer ClearJoiningMark(user.UserID, rManager)

Restart:
	if err := AlreadyPlaying(playerToken, rManager); err != nil {
		ws.Close()

		return
	}

	var fRoom *Room
	rManager.RoomsLock.RLock()
	ltBet := int64(0)
	initialBet := int64(0)

	for {
		if bet, _, err := rManager.Services.GetAnyInitialBet(user.Inventory.Chips, ltBet); err == nil {
			for _, r := range rManager.AllRooms {
				if r.RoomInfo.InitialBet == bet && r.RoomInfo.TournamentID == 0 {
					r.PlayerConnsLock.RLock()
					roomCount := len(r.PlayerConns)
					r.PlayerConnsLock.RUnlock()

					if roomCount < r.RoomInfo.RoomSize {
						fRoom = r

						goto Found
					}
				}
			}
			ltBet = bet
			log.Printf("switching to lower bet %d", ltBet)
		} else { //need to take some actions
			if err.Error() == "bad request | no other options create room with bots" {
				ltBet = 0
				if bet, _, err := rManager.Services.GetAnyInitialBet(user.Inventory.Chips, ltBet); err == nil {

					initialBet = bet
					goto Found
				}
			}
			rManager.RoomsLock.RUnlock()
			return
		}
	}
Found:

	rManager.RoomsLock.RUnlock()

	if fRoom == nil { // create new room
		mainContext := context.Background() //TODO: findout which one to use
		ctx, cancle := context.WithCancel(mainContext)

		fRoom = NewRoom(rManager, &gamemodel.RoomInfo{
			RoomSize:   6,
			IsOpen:     true,
			InitialBet: initialBet,
		}, ctx, cancle)

		go func() {
			if err := Run(fRoom, rManager); err != nil {
				log.Print("Closing room with err: " + err.Error())
			}

			RoomDelete(fRoom, rManager)
			cancle()
			RoomCloseChannels(fRoom)
		}()

		rManager.RoomsLock.Lock()
		rManager.AllRooms[fRoom.ID] = fRoom
		rManager.RoomsLock.Unlock()
	}

	fRoom.PlayerConnsLock.RLock()
	roomCount := len(fRoom.PlayerConns)
	fRoom.PlayerConnsLock.RUnlock()

	if fRoom.RoomInfo.RoomSize <= roomCount {
		goto Restart
	}

	playerConn := NewPlayerConn(user, ws, fRoom)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)

	defer cancel()

	Join(ctx, fRoom, playerConn, rManager)
}

func RoomJoinAnyTournamentV2(playerToken string, request *RequestJoinRoom, ws *websocket.Conn, rManager *RoomManager) {
	user, err := rManager.Services.GetUserByToken(playerToken)
	if err != nil {
		log.Print("RoomJoinAnyTournamentV2: token")
		ws.Close()
		return
	}

	chips, err := rManager.Repo.GetTournamentChips(user.UserID, request.TournamentID)
	if err != nil {
		log.Print("RoomJoinAnyTournamentV2: GetTournamentChips")
		ws.Close()
		return
	}

	if chips <= 0 {
		log.Print("RoomJoinAnyTournamentV2: no tournament chips")
		ws.Close()
		return
	}

	user.Inventory.Chips = chips

	if TryMarkJoining(user.UserID, rManager) {
		logrus.Warnf("Player %d is already attempting to join a room.", user.UserID)
		ws.Close()
		return
	}
	defer ClearJoiningMark(user.UserID, rManager)

Restart:
	if err := AlreadyPlaying(playerToken, rManager); err != nil {
		ws.Close()
		return
	}

	var fRoom *Room
	rManager.RoomsLock.RLock()
	ltBet := int64(0)
	initialBet := int64(0)

	for {
		if bet, _, err := rManager.Services.GetAnyInitialBet(user.Inventory.Chips, ltBet); err == nil {
			for _, r := range rManager.AllRooms {
				if r.RoomInfo.InitialBet == bet && r.RoomInfo.TournamentID == request.TournamentID {
					r.PlayerConnsLock.RLock()
					roomCount := len(r.PlayerConns)
					r.PlayerConnsLock.RUnlock()
					if roomCount < 6 {
						fRoom = r
						goto Found
					}
				}
			}
			ltBet = bet
		} else { //need to take some actions
			if err.Error() == "bad request | no other options create room with bots" {
				ltBet = 0
				if bet, _, err := rManager.Services.GetAnyInitialBet(user.Inventory.Chips, ltBet); err == nil {
					initialBet = bet
					goto Found
				}
			}

			rManager.RoomsLock.RUnlock()
			return
		}
	}
Found:

	rManager.RoomsLock.RUnlock()
	if fRoom == nil { // create new room if possible with bots
		mainContext := context.Background() //TODO: findout which one to use
		ctx, cancle := context.WithCancel(mainContext)

		fRoom = NewRoom(rManager, &gamemodel.RoomInfo{
			RoomSize:     6,
			IsOpen:       true,
			InitialBet:   initialBet,
			TournamentID: request.TournamentID,
		}, ctx, cancle)

		go func() {
			if err := Run(fRoom, rManager); err != nil {
				log.Print("Closing room with err: " + err.Error())
			}

			RoomDelete(fRoom, rManager)
			cancle()
			RoomCloseChannels(fRoom)
		}()

		rManager.RoomsLock.Lock()
		rManager.AllRooms[fRoom.ID] = fRoom
		rManager.RoomsLock.Unlock()
	}

	fRoom.PlayerConnsLock.RLock()
	roomCount := len(fRoom.PlayerConns)
	fRoom.PlayerConnsLock.RUnlock()

	if fRoom.RoomInfo.RoomSize <= roomCount {
		goto Restart
	}

	playerConn := NewPlayerConn(user, ws, fRoom)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)

	defer cancel()

	Join(ctx, fRoom, playerConn, rManager)
}

func roomAssignPassword(rManager *RoomManager) string {
Restart:

	s1 := rand.NewSource(time.Now().UnixNano())
	r1 := rand.New(s1)
	randPassword := strconv.Itoa(r1.Intn(9000) + 1000)
	rManager.CloseRoomsLock.RLock()
	for _, r := range rManager.AllCloseRooms {
		if r.RoomInfo.Password == randPassword {
			goto Restart
		}
	}
	rManager.CloseRoomsLock.RUnlock()
	return randPassword
}

func InstNotEnoughMoney() []byte {
	inst := &gamemodel.PlayerLeftInstruction{Instruction: "ROOM_MONEY_LESS"}
	b, _ := json.Marshal(inst)
	return b
}

// InstRoomIsFull is used to send full room
func InstRoomIsFull() []byte {
	inst := &gamemodel.PlayerLeftInstruction{Instruction: "ROOM_IS_FULL"}
	b, _ := json.Marshal(inst)
	return b
}

func TournamentEnd(rManager *RoomManager, tournamentID int) {

	rManager.RoomsLock.RLock()
	for _, r := range rManager.AllRooms {
		if r.RoomInfo.TournamentID == tournamentID {
			log.Print("terminating room with tournament id: ", tournamentID, " room id: ", r.RoomInfo.Name)
			r.ContextCancel()
		}
	}

	rManager.RoomsLock.RUnlock()
}
